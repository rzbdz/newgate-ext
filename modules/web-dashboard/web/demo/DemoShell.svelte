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
  import ThemePicker from "../src/ThemePicker.svelte";
  import { fileOf } from "../src/nav";
  import { t } from "../src/i18n";
  import { d } from "./strings";
  import { applyTheme } from "../src/theme";
  import type { Concept, ConceptAction, RowAction, Section, Themes } from "../src/api";
  import { snapshot, themes, useDemoLanguage } from "./data";

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
  const landingSec = sections.find((s) => s.default) ?? sections[0];
  const landing = landingSec.source;

  /**
   * 侧栏的行序：**声明了落点的那一节排第一**（不画分组标题），然后**按各节声明的
   * 用途分档**，档内按来源序。
   *
   * 与产品那份 Sidebar.svelte **同一条规则**（三个标题、同一个先后、没归类的排最后）。
   * 这里重写一份是当初的取舍（两个组件吃的 props 不同），但规则**分叉过两次**——
   * 一次是主页没置顶，一次是这里还在按贡献者写的 `group` 自由文本聚堆。演示页值得
   * 存在的地方正是「它就是那个界面」，所以规则必须逐字一样。
   */
  function sidebarRows(): ({ kind: "group"; name: string } | { kind: "sec"; s: Section })[] {
    const out: ({ kind: "group"; name: string } | { kind: "sec"; s: Section })[] = [];
    const landingSec = sections.find((s) => s.default) ?? sections[0];
    if (landingSec) out.push({ kind: "sec", s: landingSec });

    const ORDER = ["clients", "routes", "settings"];
    const head = (f: string): string =>
      f === "clients" ? t("Clients") : f === "routes" ? t("Routes") : t("Settings");

    const byField = new Map<string, Section[]>();
    for (const s of sections) {
      if (s === landingSec) continue;
      const f = s.fields || "_other";
      const g = byField.get(f);
      if (g) g.push(s);
      else byField.set(f, [s]);
    }
    for (const f of [...ORDER, "_other"]) {
      const members = byField.get(f);
      if (!members?.length) continue;
      out.push({ kind: "group", name: f === "_other" ? t("Other") : head(f) });
      for (const s of members) out.push({ kind: "sec", s });
    }
    return out;
  }
  const rows = sidebarRows();

  /**
   * 主页模式（与产品 App.svelte 的 `route.home` 同一条路）。
   *
   * **打开就是它**：这一页演的是那个界面，而那个界面今天开屏只剩卡片。进完整界面
   * 要点左边那颗齿轮（见下面模板里那一颗）——演示页没有地址栏可跳，所以它用状态
   * 而不是 hash，但两地**摆的东西**必须逐字一样。
   */
  let home = $state(true);

  let active = $state<string>(landing);
  /** 当前这一节里选中的那张卡（空 = 这一节的第一张，与产品一致）。 */
  let card = $state<string>("");
  let split = $state<boolean>(true);
  /** 本地草稿：按概念 id 存，与产品那边 App 的 `drafts` 是同一条路。 */
  let drafts = $state<Record<string, unknown>>({});
  let toast = $state<string>("");

  const section = $derived(sections.find((s) => s.source === active));
  const cards = $derived(concepts.filter((c) => c.source === active));
  /** 主页模式下画的就是落点那一节的**第一张**卡（与产品 App 的 homeConcept 同一条）。 */
  const homeCard = $derived(concepts.filter((c) => c.source === landing)[0]);

  const current = $derived(home ? homeCard : (cards.find((c) => c.id === card) ?? cards[0]));
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

  /**
   * 皮肤：演示页**只放在这个浏览器的内存里**。
   *
   * 真界面上这一格会 POST 回后端、记进 state.json（见 web-dashboard 的 theme.go）；
   * 演示页没有后端可存，所以挑了只当场生效、刷新回到出厂那套令牌。这是**唯一**
   * 一处与真界面行为不同的地方，而它不同得合理：演示页的每一处状态都是这种「点着
   * 玩、不留痕」的性子。
   */
  let theme = $state<Themes>({ ...themes(), active: "" });

  function pickTheme(id: string) {
    theme = { ...theme, active: id };
  }

  // 与 App.svelte 里那条同一条路（applyTheme 只碰 DOM），所以两边落下去的结果
  // 逐字一样——演示页演的就是真界面那套颜色。
  $effect(() => {
    applyTheme(theme);
  });

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

<div class="shell" class:home>
  <header class="top">
    <!-- 与产品那份**同一个版面**：主页模式下左边一颗齿轮（进完整界面），右边是
         这一节的全局动作 + 皮肤 + 保存；完整界面下左边是牌子 + 过滤框（演示页不给
         真的过滤，只在产品里才有），右边多一颗房子（回主页）。 -->
    {#if home}
      <button class="icon" title={t("settings")} aria-label={t("settings")} onclick={() => (home = false)}>
        <svg class="gear" viewBox="0 0 24 24" aria-hidden="true">
          <circle cx="12" cy="12" r="3.1" />
          <path
            d="M19.5 14.6a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 1 1-2.9 2.9l-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.5v.2a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.9.3l-.1.1a2 2 0 1 1-2.9-2.9l.1-.1a1.7 1.7 0 0 0 .3-1.9 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.9l-.1-.1a2 2 0 1 1 2.9-2.9l.1.1a1.7 1.7 0 0 0 1.9.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.9-.3l.1-.1a2 2 0 1 1 2.9 2.9l-.1.1a1.7 1.7 0 0 0-.3 1.9V9a1.7 1.7 0 0 0 1.5 1h.2a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"
          />
        </svg>
      </button>
    {/if}
    <strong class="brand">newgate</strong>
    <span class="spacer"></span>
    <!-- 主页模式下这一节的全局动作（今天只有 `auto fallback` 一颗）。演示页没有
         后端，所以点它只弹一句。 -->
    {#if home}
      {#each landingSec?.actions ?? [] as a (a.id)}
        {#if a.id === "fallback"}
          <button class="tiny ghost toned" data-tone={a.tone} onclick={() => noBackend(a.label)}>
            {a.label}
          </button>
        {/if}
      {/each}
    {/if}
    <!-- 皮肤切换：与真界面那一格同一个形状，只是它只在这里生效（见 pickTheme）。 -->
    {#if theme.themes.length}
      <ThemePicker doc={theme} onPick={pickTheme} />
    {/if}
    <button class="primary" onclick={onSave} disabled={!dirty}>
      {t("save")}{dirty ? ` (${dirty})` : ""}
    </button>
    {#if !home}
      <button class="icon" title={t("home mode")} aria-label={t("home mode")} onclick={() => (home = true)}>
        <svg viewBox="0 0 16 16" aria-hidden="true">
          <path d="M2.6 7.4 8 3l5.4 4.4V13a.6.6 0 0 1-.6.6h-3v-3.4h-3.6v3.4h-3A.6.6 0 0 1 2.6 13z" />
        </svg>
      </button>
    {/if}
  </header>

  {#if !home}
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
  {/if}

  <section class="content" class:subcol={vertical}>
    {#if !home}
    <div class="secbar">
      <span class="name">{section?.title}</span>
      <span class="spacer"></span>
      <!-- 这一节注入的动作（「再建一份档位文件」这类）。演示页把它们画出来——
           它们与卡片上那些按钮是同一类东西，都是贡献者给的。 -->
      {#each section?.actions ?? [] as a (a.id)}
        <button class="tiny ghost" onclick={() => noBackend(a.label)}>{a.label}</button>
      {/each}
    </div>
    {/if}

    {#if !home && cards.length > 1}
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
          bare={home}
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
  /* 主页模式：目录那一栏不画，内容占满整宽（与产品 app.css 的 `.shell.home`
     同一条）。 */
  .shell.home { grid-template-columns: minmax(0, 1fr); }
  .shell.home .content { grid-template-columns: minmax(0, 1fr); grid-template-rows: auto minmax(0, 1fr); }

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
