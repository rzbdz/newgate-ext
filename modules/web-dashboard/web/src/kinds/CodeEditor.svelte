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
  // 登记成这个 effect 的依赖，于是**每敲一个字符**（value 跟着 draft 变）effect
  // 就重跑一遍：旧编辑器 destroy、新建一个、doc 从头灌进去——光标回到第 0 行，
  // 撤销栈清零。现场的症状是「在文本编辑框里按一下 d，它跳到第一行，根本没法
  // 编辑」（2026-09-20 实测）。data.language / readonly 同理：它们是**建的时候**
  // 才需要的参数，不是「变了要重建」的信号（换文件时 ConceptCard 的 `{#key}`
  // 已经把这个组件整个重建了）。
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

  // 外部换了内容（revert、重新拉快照、**同一份文件的另一半动了手**）才覆盖文档；
  // 相同就什么都不做，否则打字到一半会被自己刚发出去的那份草稿顶回去。
  //
  // # applying：这一下替换**必须**标记成程序性的
  //
  // dispatch 同样会走上面的 updateListener，而那条路会把新内容当成一次**用户输入**
  // 再交出去——于是一次「外部换内容」变成了一次「用户刚编辑过」，回声不断。
  //
  // 后果实测过（2026-09-21，一份文件的两半）：原文改完、再去动控件 →
  //   1. App 按 lastEdit 把原文那份草稿丢掉（那是对的：控件那一半的编辑载荷是
  //      整份文件，留着必然撞过期基线）；
  //   2. 这里的 `value` 于是回落成盘上内容，触发这一下替换；
  //   3. 替换被当成用户输入交回去 → 原文草稿**复活**，还把 lastEdit 抢回 raw；
  //   4. 保存写的是原文那一半，内容 = 盘上原样 —— **两笔编辑一起没了**，而且屏幕上
  //      连一句报错都没有（只看到「未保存」自己消失了）。
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
  {/if}
</div>
<div bind:this={host} class="editor"></div>

<style>
  .editor { margin-top: 8px; }
  .row { margin-bottom: 2px; }
</style>
