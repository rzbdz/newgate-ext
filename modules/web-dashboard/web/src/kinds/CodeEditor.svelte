<script lang="ts">
  // Source file editor (the hardcore user's tab): CodeMirror 6.
  //
  // It edits **the raw text on disk**, not the "config model" - the UI does not understand a profile's fields, it just
  // lays out the bytes for a person to change. So there is no schema validation here, nothing beyond field-level completion:
  // adding a layer of semantics would mean the UI starts knowing about that file.
  //
  // Files with credentials are **read-only** (`redacted`): in their text api_key is already replaced with ***,
  // and writing back would land *** on disk - that is data loss, far worse than "cannot edit".
  import { untrack } from "svelte";
  import { basicSetup } from "codemirror";
  import { EditorState } from "@codemirror/state";
  import { EditorView } from "@codemirror/view";
  import { json } from "@codemirror/lang-json";
  import { t } from "../i18n";

  type Data = { path: string; language: string; text: string; redacted?: boolean };

  let {
    data,
    draft,
    readonly,
    onEdit,
  }: {
    data: Data;
    draft: unknown;
    readonly: boolean;
    onEdit: (v: unknown) => void;
  } = $props();

  const value = $derived((draft as { text?: string })?.text ?? data.text);

  let host: HTMLDivElement;
  let view: EditorView | undefined;

  const theme = EditorView.theme({
    "&": { fontSize: "12px", border: "1px solid var(--line)", borderRadius: "8px" },
    ".cm-content": { fontFamily: "var(--mono)", caretColor: "var(--accent)" },
    ".cm-gutters": { background: "transparent", border: "0", color: "var(--dim)" },
    "&.cm-focused": { outline: "none", borderColor: "var(--accent)" },
  });

  // The editor is **created once** - and this must be said explicitly with untrack.
  //
  // Without untrack, the `new EditorView({ doc: value, ... })` below would register value
  // as a dependency of this effect, so **on every character typed** (value follows draft) the effect
  // re-runs: destroy the old editor, create a new one, pour doc in from the start - the cursor goes back to line 0,
  // and the undo stack is cleared. The live symptom was "pressing d in the text editor jumps to the first line, and it is impossible to
  // edit" (measured 2026-09-20). Same for data.language / readonly: they are parameters needed **when creating it**,
  // not signals that "this changed so rebuild" (when switching files ConceptCard's `{#key}`
  // already rebuilds this whole component).
  $effect(() => {
    const target = host;
    if (!target) return;
    const { lang, ro, doc } = untrack(() => ({
      lang: data.language,
      ro: readonly,
      doc: value,
    }));
    const ext = [
      basicSetup,
      EditorView.lineWrapping,
      theme,
      EditorView.updateListener.of((u) => {
        // `!applying`: a programmatic replacement (the effect below) also goes through here, and it is not user
        // input - see the comment on applying.
        if (u.docChanged && !applying) onEdit({ text: u.state.doc.toString() });
      }),
    ];
    if (lang === "json") ext.push(json());
    if (ro) ext.push(EditorState.readOnly.of(true));
    const v = new EditorView({ doc, extensions: ext, parent: target });
    view = v;
    return () => {
      v.destroy();
      view = undefined;
    };
  });

  // Only overwrite the document when the content was changed externally (revert, a fresh snapshot pull, **the other half of the same file acted**);
  // if identical do nothing, otherwise typing halfway gets pushed back by the draft we just sent out.
  //
  // # applying: this replacement **must** be marked as programmatic
  //
  // dispatch also goes through the updateListener above, and that path treats the new content as a **user input**
  // and submits it again - so one "external content change" becomes one "the user just edited", an endless echo.
  //
  // The consequence was measured (2026-09-21, the two halves of one file): change the original text, then touch a control ->
  //   1. App drops the original text's draft by lastEdit (which is right: the control half's edit payload is
  //      the whole file, and keeping it necessarily hits a stale baseline);
  //   2. here `value` then falls back to the disk content, triggering this replacement;
  //   3. the replacement is treated as user input and submitted back -> the original-text draft **comes back to life**, and grabs lastEdit back to raw;
  //   4. saving writes the original-text half, with content = the disk as is - **both edits are gone**, and on screen
  //      there is not even an error (you only see "unsaved" disappear by itself).
  //
  // Set only during replacement: CodeMirror's dispatch is synchronous, and the updateListener runs inside it.
  let applying = false;
  $effect(() => {
    const next = value;
    const v = view;
    if (!v) return;
    if (v.state.doc.toString() === next) return;
    applying = true;
    try {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: next } });
    } finally {
      applying = false;
    }
  });
</script>

<div class="row">
  <span class="mono dim">{data.path}</span>
  <span class="pill">{data.language}</span>
  {#if data.redacted}
    <span
      class="pill"
      title={t("credentials are replaced with *** before they leave the daemon")}
    >{t("redacted")}</span>
  {/if}
</div>
<div bind:this={host} class="editor"></div>

<style>
  .editor { margin-top: 8px; }
  .row { margin-bottom: 2px; }
</style>
