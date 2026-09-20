<script lang="ts">
  // 源文件编辑器（硬核用户那条 tab）：CodeMirror 6。
  //
  // 编的是**磁盘上那份原文**，不是「配置模型」——界面不懂 profile 的字段，它只
  // 把字节摆出来让人改。所以这里没有 schema 校验、没有字段级补全之外的东西：
  // 加一层语义就等于界面开始认识那个文件。
  //
  // 带凭据的文件是**只读**的（`redacted`）：它的 text 里 api_key 已经被换成 ***，
  // 写回去就是把 *** 落盘——那是数据丢失，比「不能编辑」严重得多。
  import { basicSetup } from "codemirror";
  import { EditorState } from "@codemirror/state";
  import { EditorView } from "@codemirror/view";
  import { json } from "@codemirror/lang-json";

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

  // 编辑器只建一次。重建成「每次输入都新建一个」会丢光标与撤销栈——而撤销栈没了
  // 这件事，用户是在按了 Ctrl+Z 之后才发现。
  $effect(() => {
    const target = host;
    const ext = [
      basicSetup,
      EditorView.lineWrapping,
      theme,
      EditorView.updateListener.of((u) => {
        if (u.docChanged) onEdit({ text: u.state.doc.toString() });
      }),
    ];
    if (data.language === "json") ext.push(json());
    if (readonly) ext.push(EditorState.readOnly.of(true));
    const v = new EditorView({ doc: value, extensions: ext, parent: target });
    view = v;
    return () => {
      v.destroy();
      view = undefined;
    };
  });

  // 外部换了内容（revert、重新拉快照）才覆盖文档；相同就什么都不做，否则打字到
  // 一半会被自己刚发出去的那份草稿顶回去。
  $effect(() => {
    const next = value;
    const v = view;
    if (!v) return;
    if (v.state.doc.toString() !== next) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: next } });
    }
  });
</script>

<div class="row">
  <span class="mono dim">{data.path}</span>
  <span class="pill">{data.language}</span>
  {#if data.redacted}
    <span class="pill" title="credentials are replaced with *** before they leave the daemon">redacted</span>
  {/if}
</div>
<div bind:this={host} class="editor"></div>

<style>
  .editor { margin-top: 8px; }
  .row { margin-bottom: 2px; }
</style>
