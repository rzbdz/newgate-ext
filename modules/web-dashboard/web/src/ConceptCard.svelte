<script lang="ts">
  // One card = one concept. It does only three things: pick a renderer by Kind, show the reason for "cannot read",
  // mark "this one has unsaved changes".
  //
  // It **knows no modules**: `source` is just a string used for grouping. Adding a module's UI means
  // adding a renderer for a Kind (or reusing an existing one), not adding an if to this card.
  import type { Concept, ConceptAction, RowAction } from "./api";
  import { t } from "./i18n";
  import CodeEditor from "./kinds/CodeEditor.svelte";
  import MappingEditor from "./kinds/MappingEditor.svelte";
  import Records from "./kinds/Records.svelte";
  import Toggles from "./kinds/Toggles.svelte";
  import Series from "./kinds/Series.svelte";
  import LogView from "./kinds/LogView.svelte";
  import Table from "./kinds/Table.svelte";
  import Chains from "./kinds/Chains.svelte";
  import Overview from "./kinds/Overview.svelte";
  import Fingerprints from "./kinds/Fingerprints.svelte";

  let {
    concept,
    draft,
    preview,
    onEdit,
    onRevert,
    onDeleteFile,
    onAction,
    onRowAction,
    onOpenFile,
    bare,
  }: {
    concept: Concept;
    draft: unknown;
    /**
     * "What this card should show when the **other half** of the same file has a draft like this" (see App.svelte's
     * previews and the core's Concept.Preview). Use it if present, otherwise use the one in the snapshot.
     *
     * Going through the data path (rather than opening a separate prop for renderers) is deliberate: renderers already only recognize
     * one shape, `data`, and one more path means every renderer has to learn "which of the two data sources wins" again.
     * And the fact that it **is a new object** is by itself enough for renderers to clear their own local edit cache
     * (see the `seen === data` effect in MappingEditor) - which is exactly what we want:
     * after the original text changes, the control half must be redrawn from the new content.
     */
    preview?: unknown;
    onEdit: (v: unknown) => void;
    onRevert: () => void;
    // Delete/create this file (no argument: acts on this card itself, see kinds/MappingEditor).
    onDeleteFile?: () => void;
    /**
     * Run an action on this card (see ConceptAction in api.ts).
     *
     * Same shape and same reason as `onDeleteFile`: the action acts on **this card**,
     * so the UI does not need to pass "which card" - App already has this card's id.
     *
     * Nor does it **need to pass what to change**: that lives on the contributor's side (lib/view's
     * Concept.Actions), and the UI only forwards the click and then re-reads the snapshot.
     */
    onAction?: (a: ConceptAction) => void;
    /**
     * Run a button on **a row in a table** (see runRowAction in api.ts).
     *
     * One more layer of "which row" than `onAction`, for the same reason as its extra layer of "which card": rows are
     * **grown from the data** (which bindings are in the health table today depends on which requests have run),
     * so only whoever produced that row knows "what this row can do", and the UI must carry the row's identity back.
     *
     * Only the table Kind uses it - but it hangs on the card rather than being stuffed into the renderer's
     * props: a card's job is "to hand App's hand to the renderer it picked".
     */
    onRowAction?: (row: string, a: RowAction) => void;
    /**
     * Jump to the card that **edits this file** (see openFile in App.svelte).
     *
     * It is not an "action": an action changes things on disk and needs a re-read when done, while this jump only swaps to a different card.
     * Mixed into `onAction`, clicking "edit" would clear the draft - that is the side effect of
     * card actions (they change the disk, so after the re-read every baseline is stale), and here nothing was changed.
     */
    onOpenFile?: (file: string) => void;
    /** When this one is the only thing on that screen, the title and concept ID are not drawn (see that part of the template). */
    bare?: boolean;
  } = $props();

  const isDirty = $derived(draft !== undefined);
  /** The data the renderer sees: draft preview takes priority over the snapshot. */
  const shown = $derived(preview !== undefined ? preview : concept.data);

  /**
   * The renderers for these Kinds **are forms** (input boxes, switches, dropdowns), and when `writable` is false
   * they are all disabled - only then is a sentence needed to explain "why it cannot be operated" (and give a reason).
   *
   * The other Kinds (overview / chains / table / series / log) **have nothing editable at all**:
   * they change that data through **actions** (buttons) on the card and on rows. Hanging a "read-only" on them
   * says "you cannot save this card" - and there is nothing on this card that could be saved in the first place. Measured on this screen:
   * 9 cards got it, 7 of which are this kind (health table, logs, counters, plugin table, takeover table, overview,
   * candidate chains), and hovering showed "may hold credentials" - a **made-up reason**,
   * and none of those cards had any credentials. The two that should really say something are providers.json / state.json.
   */
  const EDITOR_KINDS = new Set(["mapping-editor", "code", "toggles", "records"]);
  const canEdit = $derived(EDITOR_KINDS.has(concept.kind));

  function changed(v: unknown) {
    onEdit(v);
  }
</script>

<!-- `fills`: this Kind should fill the available height (today only log, see app.css for why). The criterion hangs on
     the card rather than letting CSS guess, because "how tall I want to be" is **a property of this Kind**, not the shape it happens
     to be drawn as. -->
<section
  class="card"
  class:dirty={isDirty}
  class:broken={!!concept.error}
  class:locked={!!concept.locked}
  class:fills={concept.kind === "log" && !concept.error}
>
  {#if !bare || concept.note}
  <header>
    <!-- `bare` = this one is **the only thing on that screen** (home mode); the title and concept ID are noise:
         they answer "which card am I looking at", and there is no second one to pick there. What remains is just that
         status note - that sentence is about **the data itself** (fallback is off, some tier cannot be read),
         unrelated to "how many cards there are". -->
    {#if !bare}
      <h3>{concept.title}</h3>
      <span class="meta">{concept.id}</span>
    {/if}
    <span class="spacer"></span>
    <!-- A status note (see Concept.Note in lib/view). **It is not a button**: no onclick,
         and it should not look like one - clicking it does nothing, and a button that does nothing when clicked is more confusing
         than no button at all.
         It fills an information gap: the tier card "already in effect" does not get that pair of apply buttons
         (having them would be noise), so of sixteen cards fifteen have two buttons and one has none, and the two kinds of card
         look almost the same, so the user cannot tell which one is in effect - and that is exactly what they most want to know when opening this section.
         "Not drawing that button" and "saying nothing" are two different things. -->
    {#if concept.note}
      <span
        class="pill note"
        class:note-ok={concept.note.tone === "ok"}
        class:note-warn={concept.note.tone === "warn"}
        class:note-bad={concept.note.tone === "bad"}
        data-note={concept.note.tone ?? ""}
      >
        {concept.note.text}
      </span>
    {/if}
    <!-- The actions on this card (like "make this one the default"). **Injected by the contributor**; the UI only draws
         the buttons and forwards the click - it does not know what that button will change, and does not need to.
         Not drawn when locked or unreadable: those actions mostly cannot be done either, and a button that does nothing when clicked is more confusing
         than no button at all. -->
    {#if !concept.locked && !concept.error}
      {#each concept.actions ?? [] as a (a.id)}
        <button class="tiny ghost" data-action={a.id} onclick={() => onAction?.(a)}>{a.label}</button>
      {/each}
    {/if}
    {#if isDirty}
      <span class="pill">{t("unsaved")}</span>
      <button class="tiny ghost" onclick={onRevert}>{t("revert")}</button>
    {/if}
    {#if concept.locked}
      <!-- The reason for the gray lock is in the card header: the whole card is disabled, and not saying why makes the user guess. -->
      <span class="pill locked-pill" title={concept.locked}>{t("locked")}</span>
    {:else if !concept.writable && canEdit && !concept.error}
      <span
        class="pill"
        title={t("the contributor offers no way to write this one back (it may hold credentials)")}
      >
        {t("read-only")}
      </span>
    {/if}
  </header>
  {/if}

  <!-- `{#key concept.id}`: **switching to another card rebuilds the renderer entirely**.
       MappingEditor / Toggles / Records each have local "have I edited this" state
       (`edited`), and it takes priority over props and draft. Svelte reuses the component instance in the same position,
       so when switching cards that local state **survives with it** - and then "changed A tier without saving -> switched to B"
       shows B's file name with A's tier, and clicking save writes A into B's file.
       With the key, local state is rebuilt with the card; and **real unsaved changes are not lost** - they are in App's
       `drafts` (stored by concept id), and switching back still has them. -->
  {#if concept.locked}
    <!-- A locked card **is still drawn as usual**: hiding it would make the user think that feature does not exist, when the truth is
         "it is there, just not usable on this machine". All controls are disabled (see the readonly part below),
         and this banner explains "why" and "what to do" - gray with no explanation only makes the user think the UI is broken. -->
    <div class="locked-bar">{concept.locked}</div>
  {/if}

  <div class="body">
   {#key concept.id}
    {#if concept.error}
      <!-- Unreadable also takes a card: not reporting it would make the user think the thing does not exist. -->
      <p class="dim">{concept.error}</p>
    {:else if concept.kind === "mapping-editor"}
      <MappingEditor
        data={shown}
        {draft}
        readonly={!concept.writable || !!concept.locked}
        onEdit={changed}
        {onDeleteFile}
      />
    {:else if concept.kind === "code"}
      <CodeEditor data={shown} {draft} readonly={!concept.writable || !!concept.locked} onEdit={changed} />
    {:else if concept.kind === "toggles"}
      <Toggles data={shown} {draft} readonly={!concept.writable || !!concept.locked} onEdit={changed} />
    {:else if concept.kind === "records"}
      <Records data={shown} {draft} readonly={!concept.writable || !!concept.locked} onEdit={changed} />
    {:else if concept.kind === "table"}
      <Table data={shown} onAction={(row, a) => onRowAction?.(row, a)} />
    {:else if concept.kind === "chains"}
      <!-- Row buttons go through `onRowAction` (same as table), not the card's `onAction`:
           it changes **a row inside this card**, the card is still there when done, and a snapshot re-read fixes it; while card actions are for things
           like "make this one the default" that need a different card after they finish. The reason is written in Chains.svelte. -->
      <Chains data={shown} onAction={(row, a) => onRowAction?.(row, a)} />
    {:else if concept.kind === "overview"}
      <!-- The first-screen card: client tabs + one card per profile. It only has actions on **the card header**
           ("use this one" and "probe the whole card"), none on rows - probing a single binding lives in the health table
           section, see the props notes in kinds/Overview.svelte. -->
      <Overview data={shown} onAction={(a) => onAction?.(a)} {onOpenFile} />
    {:else if concept.kind === "series"}
      <Series data={shown} />
    {:else if concept.kind === "fingerprints"}
      <!-- The fingerprints screen: the data is all the material for this section (profile / provider / last conclusion),
           and verifying once on the spot goes through its own extension endpoint (see kinds/Fingerprints.svelte).
           It has no Apply: this screen changes no config, it only answers "who is over there". -->
      <Fingerprints data={shown} />
    {:else if concept.kind === "log"}
      <LogView data={shown} />
    {:else}
      <!-- A Kind with no renderer yet (the kind added later): lay out the original text rather than pretend it does not
           exist. Adding a renderer is the frontend's job, and the backend should not wait for it.
           (Correction 2026-09-20: this comment used to list table and log as having no renderer too;
           their branches are a dozen lines above and their renderers are both in kinds/.) -->
      <p class="dim">{t("no renderer for kind “{kind}” yet — raw data:", { kind: concept.kind })}</p>
      <pre class="raw">{JSON.stringify(concept.data, null, 2)}</pre>
    {/if}
   {/key}
  </div>
</section>

<style>
  .raw {
    font-family: var(--mono);
    font-size: 12px;
    max-height: 320px;
    overflow: auto;
    margin: 0;
  }
  /* The tone of the status note (see Concept.Note): **change only the color, not the shape** - it is a statement, not a
     button, and should not look like a warning bar. `ok` is "everything is fine, this is the one in effect", the same green as
     that cell in the health table. The border is lighter too, so this cell does not stand out more than the things on the card you can really click. */
  .note-ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 45%, transparent); }
  .note-warn { color: var(--warn); border-color: color-mix(in srgb, var(--warn) 45%, transparent); }
  .note-bad { color: var(--danger); border-color: color-mix(in srgb, var(--danger) 45%, transparent); }
</style>
