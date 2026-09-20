<script lang="ts">
  // 源文件编辑器（硬核用户那条 tab）：CodeMirror 6。
  //
  // 编的是**磁盘上那份原文**，不是「配置模型」——界面不懂 profile 的字段，它只
  // 把字节摆出来让人改。所以这里没有 schema 校验、没有字段级补全之外的东西：
  // 加一层语义就等于界面开始认识那个文件。
  //
  // 带凭据的文件是**只读**的（`redacted`）：它的 text 里 api_key 已经被换成 ***，
  // 写回去就是把 *** 落盘——那是数据丢失，比「不能编辑」严重得多。
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

  // 编辑器**只建一次**——而这件事必须用 untrack 明确说出来。
  //
  // 不 untrack 的话，下面那句 `new EditorView({ doc: value, … })` 会把 value
  // 登记成这个 effect 的依赖，于是 value 一变 effect 就重跑：旧编辑器 destroy、
  // 新建一个、doc 从头灌进去——**滚动位置和选区一起没**。这一栏今天是**只读**的
  // （见 fileConcepts：一份文件只有一个可写的面），所以触发它的是外部更新
  // （revert、重新拉快照、切回来时草稿已经不在），而不是打字。data.language /
  // readonly 同理：它们是**建的时候**才需要的参数，不是「变了要重建」的信号
  // （换文件时 ConceptCard 的 `{#key}` 已经把这个组件整个重建了）。
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
        // `!applying`：程序性的替换（下面那个 effect）也会走这里，而它不是用户
        // 输入——见 applying 的注释。
        //
        // `!readonly`：**只读的编辑器一个字都不回报**。这一条是「一份文件只有一个
        // 可写的面」那半边的保险（见 core/modules/config/view.go 的 fileConcepts）：
        // 只读的那一半即使因为某个 effect 动了一下文档，也不该冒出一份草稿来——
        // 那种草稿只会在保存时撞上「这个概念是只读的」，而用户根本没打过字。
        if (u.docChanged && !applying && !readonly) onEdit({ text: u.state.doc.toString() });
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

  // 外部换了内容（revert、重新拉快照、切回来时草稿已经不在）才覆盖文档；相同就
  // 什么都不做，否则打字到一半会被自己刚发出去的那份草稿顶回去。
  //
  // # applying：这一下替换**必须**标记成程序性的
  //
  // dispatch 同样会走上面的 updateListener，而那条路会把新内容当成一次**用户输入**
  // 再交出去——于是一次「外部换内容」变成了一次「用户刚编辑过」，回声不断。实测的
  // 后果是：`revert` 之后文档被重新灌回旧内容，那一次灌回又被当成一次编辑交上去，
  // 草稿立刻复活——用户点了「撤销」，屏幕上却还是脏的，而且看不出为什么。
  //
  // 只在替换期间置位：CodeMirror 的 dispatch 是同步的，updateListener 就在里面跑完。
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
  {:else if readonly}
    <!-- 只读**不是**因为凭据，而是因为这份文件已经有结构化的编辑面了（见
         core/modules/config/view.go 的 fileConcepts）。不说这句的话，用户对着一个
         打不进字的框只会以为界面坏了。 -->
    <span class="dim">{t("read-only — the controls for this file are in the other pane")}</span>
  {/if}
</div>
<div bind:this={host} class="editor"></div>

<style>
  .editor { margin-top: 8px; }
  .row { margin-bottom: 2px; }
</style>
