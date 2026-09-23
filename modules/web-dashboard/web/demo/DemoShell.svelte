<script lang="ts">
  // 演示页的外壳：**左边一列节，右边那一节的卡**——就是产品界面本来的样子。
  //
  // # 它为什么不复用 App.svelte
  //
  // App.svelte 要全套后端：snapshot / apply / preview 三个端点、每 3 秒一次的自动
  // 刷新、冲突对话框、跨节过滤、快捷键、URL 路由。这里全都不需要——需要的只有
  // 「一份快照 + 一点本地状态」。硬塞进 App 的结果是给产品那条路加一堆「如果没后端
  // 就……」的分支，而那些分支一年也跑不到一次（它们只在演示页上跑）。
  //
  // # 它复用的是**卡片与并排**
  //
  // ConceptCard 与七个 kinds 是纯的（给数据就画，没有一处 fetch），SplitView 也是
  // （它只管把两半摆开）。所以这里直接用它们——屏幕上那些卡与产品里是**同一个组件、
  // 同一份 CSS**。这正是这个演示页值得存在的地方：它不是一张截图，也不是照着重画
  // 的一版。
  //
  // 侧栏与卡片那一列是**自己写的**（照产品的 Sidebar / TabStrip 的外观，但各写一遍）：
  // 那两个组件吃的是产品的 props（命中数、竖排开关、快捷键…），为了演示页给它们加
  // 「没有后端时怎么办」的开关，代价与上面那条一样。
  //
  // # 假的那一半
  //
  // 编辑、切节、切卡、并排都在本地 state 里真生效；点保存 / 动作 / 行上的按钮只弹
  // 一句「这是演示」。**一行都不落盘**——这里没有后端可落。
  import SplitView from "../src/SplitView.svelte";
  import { fileOf } from "../src/nav";
  import { t } from "../src/i18n";
  import { d } from "./strings";
  import type { Concept, ConceptAction, RowAction, Section } from "../src/api";
  import { snapshot, useDemoLanguage } from "./data";

  useDemoLanguage();

  const snap = snapshot();
  const sections: Section[] = snap.sections;
  const concepts: Concept[] = snap.concepts;

  /**
   * 落点：**声明了 `default` 的那一节**（见 api.ts 的 Section.default）。
   *
   * 与产品的 `defaultRoute()` 同一条判据，只是这里没有它那两条回落——演示页的节与
   * 卡都是写死的，不会出现「声明了落点但那一节是空的」。产品那边要回落，是因为模块
   * 可以被关掉、profile 可以被删。
   */
  const landing = sections.find((s) => s.default)?.source ?? sections[0].source;

  /** 侧栏的行序：不归组的那几节排最前，其余按分组**第一次出现**的先后聚在一起。 */
  function sidebarRows(): ({ kind: "group"; name: string } | { kind: "sec"; s: Section })[] {
    const out: ({ kind: "group"; name: string } | { kind: "sec"; s: Section })[] = [];
    for (const s of sections) if (!s.group) out.push({ kind: "sec", s });
    const seen: string[] = [];
    for (const s of sections) {
      if (!s.group) continue;
      if (seen.includes(s.group)) continue;
      seen.push(s.group);
      out.push({ kind: "group", name: s.group });
      for (const g of sections) if (g.group === s.group) out.push({ kind: "sec", s: g });
    }
    return out;
  }
  const rows = sidebarRows();

  let active = $state<string>(landing);
  /** 当前这一节里选中的那张卡（空 = 这一节的第一张，与产品一致）。 */
  let card = $state<string>("");
  let split = $state<boolean>(true);
  /** 本地草稿：按概念 id 存，与产品那边 App 的 `drafts` 是同一条路。 */
  let drafts = $state<Record<string, unknown>>({});
  let toast = $state<string>("");

  const section = $derived(sections.find((s) => s.source === active));
  const cards = $derived(concepts.filter((c) => c.source === active));
  const current = $derived(cards.find((c) => c.id === card) ?? cards[0]);
  /** 卡片顺序那一栏的形状：卡少就横着一条，多了就左侧竖着列——与产品的阈值同一条。 */
  const vertical = $derived(cards.length > 8);

  /**
   * 与当前这张卡**说的是同一份文件**的另一半（控件半 ↔ 原文半，见 nav.ts 的 fileOf）。
   *
   * 判据与产品里那一段逐字一样（同一份文件、一 code 一非 code）。差别只有一处：产品
   * 会把原文那一半从导航里收起来（同一份文件只列一张卡），演示页不收起——这里就是要
   * 让人**看得见**这两半的存在，并排开关才点得明白。
   */
  const pair = $derived.by(() => {
    if (!current) return undefined;
    const f = fileOf(current);
    if (!f) return undefined;
    return concepts.find(
      (x) =>
        x.id !== current.id &&
        x.source === current.source &&
        fileOf(x) === f &&
        (current.kind === "code" ? x.kind !== "code" : x.kind === "code"),
    );
  });

  /** 并排时哪一半在左：控件那一半（与产品一致）。 */
  const leftCard = $derived(current?.kind === "code" && pair ? pair : current);
  const rightCard = $derived(current?.kind === "code" && pair ? current : pair);

  const dirty = $derived(Object.keys(drafts).length);

  function say(msg: string) {
    toast = msg;
    // 提示自己消失：演示页不该留下一个要手动关的东西（它只是说一句「这里没有
    // 后端」，不是一条要用户处理的错误）。
    setTimeout(() => (toast = ""), 2600);
  }

  function noBackend(action: string) {
    say(d("this is a demo — “{action}” would go to the real daemon; nothing is written here", { action }));
  }

  /** 切节：换一节就把卡与草稿一起重置（草稿按卡存，但换节时留着只会让人困惑）。 */
  function pickSection(source: string) {
    active = source;
    card = "";
    drafts = {};
  }

  /** 切卡（换一张就把草稿清掉——与产品那边切卡的行为一致）。 */
  function pickCard(id: string) {
    card = id;
    drafts = {};
  }

  function onEdit(id: string, v: unknown) {
    drafts = { ...drafts, [id]: v };
  }

  function onRevert(id: string) {
    const next = { ...drafts };
    delete next[id];
    drafts = next;
  }

  /** SplitView 那条路要带卡 id（它同时拿着两半），签名照产品给。 */
  function onAction(id: string, a: ConceptAction) {
    noBackend(a.label);
  }

  function onRowAction(id: string, row: string, a: RowAction) {
    say(
      d("this is a demo — “{action}” on “{row}” would talk to the real daemon", {
        action: a.label,
        row,
      }),
    );
  }

  function onDeleteFile() {
    say(d("this is a demo — no file would be deleted"));
  }

  /** 报头上那个保存：真的会写盘，所以这里也只说一句。 */
  function onSave() {
    say(d("this is a demo — save would write these files; here it writes nothing"));
  }
</script>

<div class="shell">
  <header class="top">
    <b class="brand">newgate</b>
    <span class="dim">
      {d("this page is the real interface with made-up data — nothing you click leaves the browser")}
    </span>
    <span class="spacer"></span>
    <span class="dim">{dirty ? t("unsaved") : t("save")}</span>
    <button class="primary" onclick={onSave}>
      {t("save")}{dirty ? ` (${dirty})` : ""}
    </button>
  </header>

  <nav class="side">
    <p class="head">{t("sections")}</p>
    {#each rows as r, i (`${r.kind}:${r.kind === "group" ? r.name : r.s.source}:${i}`)}
      {#if r.kind === "group"}
        <!-- 分组标题**不是按钮**（它代表的是一类，不是一节）。 -->
        <div class="group">{r.name}</div>
      {:else}
        <button
          class="row"
          class:on={r.s.source === active}
          title={r.s.source}
          onclick={() => pickSection(r.s.source)}
        >
          <span class="label">{r.s.title}</span>
          <span class="badge">{concepts.filter((c) => c.source === r.s.source).length}</span>
        </button>
      {/if}
    {/each}
  </nav>

  <section class="content" class:subcol={vertical}>
    <div class="secbar">
      <span class="name">{section?.title}</span>
      <span class="spacer"></span>
      <!-- 这一节注入的动作（「再建一份档位文件」这类）。演示页把它们画出来——
           它们与卡片上那些按钮是同一类东西，都是贡献者给的。 -->
      {#each section?.actions ?? [] as a (a.id)}
        <button class="tiny ghost" onclick={() => noBackend(a.label)}>{a.label}</button>
      {/each}
    </div>

    {#if cards.length > 1}
      {#if vertical}
        <!-- 卡多（config 那种）时：左侧一列，一眼扫完。 -->
        <nav class="v">
          {#each cards as c (c.id)}
            <button
              class="row"
              class:on={c.id === current?.id}
              title={c.id}
              onclick={() => pickCard(c.id)}
            >
              <span class="label">{c.title}</span>
              {#if drafts[c.id] !== undefined}<span class="dot" title={t("unsaved")}></span>{/if}
            </button>
          {/each}
        </nav>
      {:else}
        <!-- 卡少时：顶上一条横的。 -->
        <div class="tabs">
          {#each cards as c (c.id)}
            <button
              class="tab"
              class:on={c.id === current?.id}
              title={c.id}
              onclick={() => pickCard(c.id)}
            >
              {c.title}
            </button>
          {/each}
        </div>
      {/if}
    {/if}

    <div class="pane">
      {#if current}
        <SplitView
          left={leftCard}
          right={rightCard}
          {drafts}
          previews={{}}
          {split}
          onEdit={onEdit}
          onRevert={onRevert}
          {onDeleteFile}
          {onAction}
          {onRowAction}
          onToggleSplit={() => (split = !split)}
        />
      {:else}
        <p class="dim">{t("nothing in this section matches.")}</p>
      {/if}
    </div>
  </section>
</div>

{#if toast}
  <div class="toast">{toast}</div>
{/if}

<style>
  /* 演示页的版面自己写一份（产品那份在 app.css 的 .shell 里，是给 App 的网格用
     的）。形状照它：左边 208px 的目录、顶上一整条报头、右下内容。 */
  .shell {
    display: grid;
    grid-template-columns: 208px minmax(0, 1fr);
    grid-template-rows: auto minmax(0, 1fr);
    height: 100%;
  }
  .top {
    grid-column: 1 / -1;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 9px 14px;
    border-bottom: 1px solid var(--line);
    background: var(--panel);
  }
  .brand { font-size: 13px; }
  .spacer { flex: 1; }

  .side {
    overflow-y: auto;
    border-right: 1px solid var(--line);
    background: var(--panel);
    padding: 8px 0 12px;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .side .head {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.7px;
    color: var(--dim);
    margin: 4px 12px 6px;
  }
  .side .group {
    margin: 10px 12px 3px;
    font-size: 11px;
    letter-spacing: 0.6px;
    text-transform: uppercase;
    color: var(--dim);
  }
  .side .row {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    text-align: left;
    background: transparent;
    border: 0;
    border-radius: 0;
    padding: 6px 12px;
    color: var(--ink);
    cursor: pointer;
  }
  .side .row:hover { background: var(--panel-2); }
  .side .row.on { background: var(--panel-2); box-shadow: inset 2px 0 0 var(--accent); }
  .side .label { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .side .badge { font-family: var(--mono); font-size: 11px; color: var(--dim); }

  .content {
    display: grid;
    grid-template-rows: auto auto minmax(0, 1fr);
    min-height: 0;
    overflow: hidden;
  }
  /* 卡多的节：多一栏 210px 的卡片目录（与产品同一套版面，判据是 >8 张卡）。 */
  .content.subcol {
    grid-template-columns: 210px minmax(0, 1fr);
    grid-template-rows: auto minmax(0, 1fr);
  }
  .content.subcol > .secbar { grid-column: 1 / -1; }
  .content.subcol > .pane { grid-column: 2; grid-row: 2; min-height: 0; min-width: 0; }

  .secbar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 16px;
    border-bottom: 1px solid var(--line);
    background: var(--panel);
  }
  .secbar .name {
    font-size: 11px;
    letter-spacing: 0.6px;
    text-transform: uppercase;
    color: var(--dim);
  }

  /* 竖着的卡片目录（卡多时）。滚动落在它自己身上：它是网格里一行确定高度的容器。 */
  .v {
    grid-column: 1;
    grid-row: 2;
    overflow-y: auto;
    border-right: 1px solid var(--line);
    background: var(--panel);
    padding: 6px 0 10px;
    display: flex;
    flex-direction: column;
    gap: 1px;
    min-height: 0;
  }
  .v .row {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    text-align: left;
    background: transparent;
    border: 0;
    border-radius: 0;
    padding: 6px 12px;
    color: var(--ink);
    cursor: pointer;
  }
  .v .row:hover { background: var(--panel-2); }
  .v .row.on { background: var(--panel-2); box-shadow: inset 2px 0 0 var(--accent); }
  .v .label { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dot { width: 6px; height: 6px; border-radius: 50%; background: var(--warn); flex: none; }

  /* 横着的卡片条（卡少时）。 */
  .tabs {
    display: flex;
    align-items: center;
    gap: 2px;
    overflow-x: auto;
    white-space: nowrap;
    border-bottom: 1px solid var(--line);
    padding: 0 10px;
    background: var(--panel);
  }
  .tabs .tab {
    background: transparent;
    border: 0;
    border-bottom: 2px solid transparent;
    border-radius: 0;
    padding: 7px 10px;
    color: var(--dim);
  }
  .tabs .tab:hover { color: var(--ink); }
  .tabs .tab.on { color: var(--ink); border-bottom-color: var(--accent); }

  /* min-height: 0 与 minmax(0, 1fr) 是这块版面的承重墙（见 app.css 里同一段说明）：
     不写它们的话，里面会滚的东西会把整页撑高，而不是自己滚。 */
  .pane {
    overflow: auto;
    padding: 14px 16px;
    min-height: 0;
    min-width: 0;
  }

  /* 窄屏：侧栏收成顶上一条横带，卡片的目录栏也退回叠加（与产品同一套退法）。 */
  @media (max-width: 900px) {
    .shell {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto auto minmax(0, 1fr);
    }
    .top { grid-column: 1; }
    .side {
      flex-direction: row;
      align-items: center;
      overflow-x: auto;
      overflow-y: hidden;
      border-right: 0;
      border-bottom: 1px solid var(--line);
      padding: 4px 8px;
      white-space: nowrap;
    }
    .side .head, .side .group { display: none; }
    .side .row { width: auto; }
    .side .row.on { box-shadow: inset 0 -2px 0 var(--accent); }
    .content.subcol { grid-template-columns: minmax(0, 1fr); grid-template-rows: auto auto minmax(0, 1fr); }
    .content.subcol > .secbar { grid-column: 1; }
    .v {
      grid-column: 1;
      grid-row: auto;
      flex-direction: row;
      overflow-x: auto;
      overflow-y: hidden;
      border-right: 0;
      border-bottom: 1px solid var(--line);
    }
    .content.subcol > .pane { grid-column: 1; grid-row: 3; }
  }
</style>
