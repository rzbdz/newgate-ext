<script lang="ts">
  // 首屏：**让哪个客户端走哪一份档位**（形状见内核 lib/view 的 Overview）。
  //
  // # 这一屏和 chains 那一屏的分工
  //
  // 它在同一节里排在 Chains 前面。那一张是「逐条读」——每一档的完整链、被跳过的是
  // 谁、为什么；这一张是「拿它做决定」：顶上一条客户端标签，下面一份 profile 一张
  // 卡，卡上是**此刻会走的那几步**加上每一站的健康（延迟、颜色、上次探活的结论）。
  //
  // # 三条约定，与 chains 那边同源
  //
  //  1. **不认识任何模块**。`agents` 是后端给的机器标记 + 显示名，`cards` 是每一份
  //     profile 的链结论。这一屏不 import 任何 config/breaker 的东西。
  //  2. **颜色是语义，不是数值**。延迟那一格的颜色由后端给（`latency_tone`）——
  //     多少毫秒算慢是 breaker 的判据，只有一处实现。写死阈值在这一屏会让改阈值的
  //     那一天有两处要改，而漏掉的那处不会有任何东西变红。
  //  3. **`use:` 前缀是契约**。卡片动作里 id 以 `use:` 开头的那些是「就用它」，
  //     后面那段是 agent 的机器标记；其余（今天就是探活）原样画成按钮。这条拼法是
  //     后端写死的（见 view.OverviewCard.Actions），两边各拼一次就得拼成同一个串。
  type Step = {
    provider: string;
    model: string;
    profile?: string;
    latency_ms?: number;
    latency_tone?: string;
    probe?: string;
    note?: string;
  };
  type Act = { id: string; label: string };
  type Row = {
    id?: string;
    tier: string;
    label?: string;
    head?: string;
    steps?: Step[];
    note?: string;
    tone?: string;
  };
  type Card = {
    profile: string;
    file?: string;
    default?: boolean;
    roles: Row[];
    actions?: Act[];
  };
  type Agent = { id: string; name: string; profile?: string; own?: boolean; ready?: boolean };
  type Data = { agents: Agent[]; cards: Card[] };

  import { t } from "../i18n";
  import type { ConceptAction } from "../api";

  let {
    data,
    onAction,
  }: {
    data: Data;
    /**
     * 卡片头上的按钮（「就用它」/「整张卡探一遍」），见 api.ts 的 ConceptAction。
     *
     * **没有 `onRowAction`**：这一屏的行上没有动作。单条 binding 的探活在健康表
     * 那一节（`breaker.health` 每行一个按钮），而这里是按卡与按屏的——理由见
     * modules/home/overview.go 的 probeSet。
     */
    onAction?: (a: ConceptAction) => void;
  } = $props();

  const agents = $derived(data?.agents ?? []);
  const cards = $derived(data?.cards ?? []);

  /**
   * 现在看的是哪一栏（agent 的机器标记；空串 = 那一档「全部客户端」）。
   *
   * 默认落在**第一个装了的**客户端上：一个都没装时落在那档空的上面（那时它是唯一
   * 能把链换掉的手段，见 overview.go 的 useActions）。
   */
  let tab = $state<string | undefined>(undefined);
  const current = $derived<Agent | undefined>(
    agents.find((a) => a.id === tab) ?? agents.find((a) => a.ready) ?? agents[0],
  );

  /** 这一栏此刻在用的那一份 profile（空 = 没有 / 跟随全局默认）。 */
  const using = $derived(current?.profile ?? "");

  /** 这一张卡是不是**这一栏正在用的**那一份。 */
  function isActive(c: Card): boolean {
    return using ? c.profile === using : !!c.default;
  }

  /**
   * 这一张卡上该画哪几个按钮。
   *
   * `use:` 那一族是「把这份 profile 指给某个客户端」，而一次只该画**一个**：当前
   * 这一栏的那个。指给它自己的那个后端根本不报（见 useActions：已经用着它的不
   * 出现），所以「这一栏已经用它」这件事由下面那句陈述说出来。
   *
   * ID 是 `use:<agent>:<profile>`（见内核 view.OverviewCard.Actions）：**profile
   * 那一段是必须的**——同一个 agent 的同一个动作在十几张卡上各有一个，而账本按 ID
   * 找人取的是第一个匹配。界面于是按「当前这一栏的 agent + 这张卡的 profile」拼出
   * 唯一那一个（与链卡那边拼行 ID 是同一条规矩）。
   *
   * 空 id 那一档（没装任何客户端，或者用户点了「全部客户端」）画的是全局那一个。
   */
  function actions(c: Card): Act[] {
    const out: Act[] = [];
    const who = current?.id ? current.id : "global";
    for (const a of c.actions ?? []) {
      if (a.id.startsWith("auto:")) {
        // 「改回自动」（`auto:<agent>`）：后端只把它挂在**这一家正固定着**的那张卡
        // 上，但切到别家时不该还留着——那是别人家的状态，点下去改的是别人。
        if (a.id === `auto:${who}`) out.push(a);
        continue;
      }
      if (!a.id.startsWith("use:")) {
        out.push(a); // 探活那类：与标签无关，照画。
        continue;
      }
      // 全局那一档只在「全部客户端」里画：每张卡都挂一个「所有客户端都用它」，
      // 会让这一屏上最常用的那个按钮淹在一堆同义词里。
      if (a.id === `use:${who}:${c.profile}`) out.push(a);
    }
    return out;
  }

  /**
   * 链上要展开的那几站：**链头之后的头三站**，链头自己单独占一格。
   *
   * 用户要的是「链头是谁、后面还排着谁」——排到第四站以后在读这一屏时没有意义
   * （那是 `newgate tier` 与 Chains 那一张的事），而每张卡多三行就是几十行。
   */
  const FALLBACKS = 3;
  function rest(r: Row): Step[] {
    return (r.steps ?? []).slice(1, 1 + FALLBACKS);
  }

  /** 链上还有几站没画出来（`+N` 那个角标）。 */
  function hidden(r: Row): number {
    return Math.max(0, (r.steps?.length ?? 0) - 1 - FALLBACKS);
  }

  /**
   * 哪几张卡摊开了。**默认全收着**（见下面 groups 的说明）。
   *
   * 摊开是**按卡**的，不是按档的：这一屏上要摊开一张卡时的那个问题永远是「这条链
   * 到底怎么走」，而那条链的五档是一起读的（比着看才知道哪档先掉下去）。按档摊开
   * 一次只能看一档，反而要开五次。
   */
  let open = $state<Record<string, boolean>>({});

  /** 系统的「减少动效」偏好。它只影响**滚过去的方式**，不影响滚不滚（见 toggle）。 */
  function reduceMotion(): boolean {
    return window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
  }

  /**
   * 摊开/收起一张卡。
   *
   * # 摊开之后要滚一下
   *
   * 摊开的那一张会**高好几倍**（实测 5 档全摊开约 900px，收起来约 200px），而且它
   * 占满整行——于是它自己那一行会往下推、它上方的卡也跟着挪，用户点开的那一条的
   * 标题很容易就跑到视野外面去。所以摊开之后把它顶到视野里。
   *
   * 为什么要等一帧：这一刻 DOM 还是收起的样子，`scrollIntoView` 量到的是**旧的**
   * 位置（收起时 200px 高、摊开后 900px，差出好几百像素）。下一帧才量得对。
   *
   * 为什么收起时**不滚**：收起是一个「我不看了」的动作，把人从原地挪走正好相反。
   */
  function toggle(profile: string, ev: MouseEvent) {
    const next = !open[profile];
    open = { ...open, [profile]: next };
    if (!next) return;
    const card = (ev.currentTarget as HTMLElement | null)?.closest(".pc");
    if (!card) return;
    requestAnimationFrame(() =>
      card.scrollIntoView({
        block: "start",
        behavior: reduceMotion() ? "auto" : "smooth",
      }),
    );
  }

  /**
   * 链头后面还站着几站（收起时那个 `+N`）。
   *
   * 收起时给的是**深度**，不是那几站是谁——那几站的归属是摊开才读的东西（也是
   * `newgate tier` 与 Chains 那一屏的东西）。摊开后 `rest()` / `hidden()` 接手。
   */
  function depth(r: Row): number {
    return Math.max(0, (r.steps?.length ?? 0) - 1);
  }

  function toneClass(tone: string | undefined): string {
    return tone === "ok" || tone === "warn" || tone === "bad" ? `t-${tone}` : "";
  }

  /**
   * 延迟那一格：数字 + 颜色。没样本时**什么都不画**（不是画 0ms）——「没有样本」
   * 与「快得没有延迟」是两句话，而这一屏正是拿它比快慢的。
   *
   * 上一次探活的结论（fluent / laggy / …）走 title：它是机器标记，是这一格背后的
   * 判据，鼠标停上去该看得到，但不该占版面（那一列是拿来竖着比数字的）。
   */
  function msTitle(s: Step): string {
    const key = `${s.provider}/${s.model}`;
    return s.probe ? `${key} · ${s.probe}` : key;
  }
</script>

<!-- 客户端标签。没有客户端模块时 agents 是**一档空的**（后端补的），这里照常画
     一个标签——它不是装饰，它决定了卡片上那个「就用它」写的是哪个客户端。 -->
{#if agents.length}
  <div class="agents" role="tablist">
    {#each agents as a (a.id)}
      <button
        class="agent"
        class:on={current?.id === a.id}
        class:off={a.ready === false}
        role="tab"
        aria-selected={current?.id === a.id}
        onclick={() => (tab = a.id)}
      >
        <!-- 图标：一个终端形状的记号。**客户端是机器取值**（claude / codex），没有
             一套现成的图标可挑，而这一格要的是「一眼分出几个客户端」——形状统一、
             颜色跟着装没装走就够了，比给每家编一个图标诚实。 -->
        <svg class="ico" viewBox="0 0 16 16" aria-hidden="true">
          <rect x="1.5" y="2.5" width="13" height="11" rx="2.5" />
          <path d="M4.6 6.2 6.6 8l-2 1.8M8.4 10.2h3" />
        </svg>
        <span class="nm">{a.name}</span>
        {#if a.profile}
          <!-- 「自动」与「固定」是**两个状态**，不能合成一句话：固定选 ds 之后改掉
               全局默认不会动它；跟着默认走的则会被一起改掉。所以跟着走时说「自动」，
               并把解析到的那一份另起一格写出来（那是它此刻实际走谁）；只有固定住的
               才直接写那一份。 -->
          {#if a.own}
            <span class="cur mono">{a.profile}</span>
          {:else}
            <span class="auto">{t("auto")}</span>
            <span class="cur mono dim">{a.profile}</span>
          {/if}
        {/if}
        {#if a.ready === false}
          <span class="dim off">{t("not installed")}</span>
        {/if}
      </button>
    {/each}
  </div>
{/if}

<div class="grid">
  {#each cards as c (c.profile)}
    <section class="pc" class:on={isActive(c)} class:folded-open={!!open[c.profile]}>
      <header class="phead">
        <!-- 链名（= profile 名）是这一张卡上最大的一行字：用户在这一屏上做的唯一
             一个决定是「用哪一份」，而名字就是那个决定的宾语。
             它同时是**摊开/收起**的开关（仿 Clash 的组头）：整行都点得动，比在角落
             放一个 12px 的小三角好按得多，而这一屏上最频繁的动作就是「扫一遍」和
             「摊开看这条」。 -->
        <button class="tog" aria-expanded={!!open[c.profile]} onclick={(ev) => toggle(c.profile, ev)}>
          <svg class="chev" viewBox="0 0 16 16" aria-hidden="true">
            <path d="M6 4l4 4-4 4" />
          </svg>
          <span class="pname">{c.profile}</span>
        </button>
        {#if c.default}
          <span class="pill ok-pill">{t("default")}</span>
        {/if}
        <span class="spacer"></span>
        {#if isActive(c)}
          <!-- 「这一栏正用着它」是一句**陈述**，不是按钮：把它指给它是没有意义的
               事（所以后端根本不报那个动作）。与 Concept.Note 那条同源——「不画
               那个按钮」与「什么都不说」是两件事。 -->
          <span class="pill ok-pill" data-using="1">
            {#if !current?.id}
              {t("in use")}
            {:else if current.own}
              <!-- 固定住的那一份：它不会跟着全局默认动。 -->
              {t("in use by {client}", { client: current.name })}
            {:else}
              <!-- 跟着默认走、而默认恰好是这一份：**不是**同一句话。它与上面那一条
                   只差一个字，但改掉全局默认之后一个会走、一个不会。 -->
              {t("auto in use by {client}", { client: current.name })}
            {/if}
          </span>
        {/if}
        {#each actions(c) as a (a.id)}
          <button class="tiny ghost" data-action={a.id} onclick={() => onAction?.(a)}>
            {a.label}
          </button>
        {/each}
      </header>

      {#if c.file}
        <div class="meta mono">{c.file}</div>
      {/if}

      <ul class="roles" class:flat={!open[c.profile]}>
        {#if !open[c.profile]}
          <!-- 收起来的样子：**一档一行**，只说这一档从谁起、后面还垫着几站、多快。
               那几站是谁是摊开才读的东西（也是 `newgate tier` 与 Chains 那一屏的
               东西）；把它画在默认视图里，一张卡就是三十行，一屏放不下两张卡。

               这里**不把共享链头的档位并成一行**：并起来更短，但每个组占几行是变的，
               于是卡片高度参差、行也参差——而这一屏是拿来**竖着比**的（同一张卡里
               哪档快、两张卡之间谁快）。等高、等宽的行比少几行值钱。 -->
          {#each c.roles as r (r.id ?? r.tier)}
            <li class="grp">
              <span class="tname" title={r.tier}>{r.label ?? r.tier}</span>
              <span class="headline">
                {#if r.head}
                  <!-- 窄卡里链头会被截掉（`minimax/MiniMax-M2.7-hig…`），而它是这一行
                       最要紧的那个值——全名走 title，鼠标停上去看得到。 -->
                  <span class="head mono {toneClass(r.tone)}" title={r.head}>{r.head}</span>
                  {#if depth(r)}
                    <span class="depth mono" title={t("+{n} more on the chain", { n: depth(r) })}>
                      +{depth(r)}
                    </span>
                  {/if}
                {:else}
                  <!-- 一档都没有候选举不起来：那**为什么**举不起来才是这一行的内容
                       （空档位在界面上不能只显示一个「空」字，那看起来像加载失败）。 -->
                  <span class="dim">{t("no steps")}</span>
                  {#if r.note}<span class="dim note" title={r.note}>{r.note}</span>{/if}
                {/if}
              </span>
              {#if r.steps?.[0]?.latency_ms}
                <span
                  class="ms mono {toneClass(r.steps[0].latency_tone)}"
                  title={msTitle(r.steps[0])}
                >
                  {r.steps[0].latency_ms}{t("ms")}
                </span>
              {/if}
            </li>
          {/each}
        {:else}
          {#each c.roles as r (r.id ?? r.tier)}
            <li>
              <div class="tier">
                <span class="tierline">
                  <span class="tname" title={r.tier}>{r.label ?? r.tier}</span>
                  {#if r.head}
                    <span class="head mono {toneClass(r.tone)}">{r.head}</span>
                  {:else}
                    <span class="dim">{t("no steps")}</span>
                  {/if}
                </span>
                <!-- 延迟单独占一列、右对齐：这一列是**拿来竖着比**的（同一张卡里哪一档
                     快、两张卡之间谁快），所以它得像表格里的数字列那样对齐。跟在链头
                     后面会随名字长短左右横跳，也就没法比了。 -->
                {#if r.steps?.[0]?.latency_ms}
                  <span class="ms mono {toneClass(r.steps[0].latency_tone)}" title={msTitle(r.steps[0])}>
                    {r.steps[0].latency_ms}{t("ms")}
                  </span>
                {/if}
              </div>

              <!-- 链头之后的前三站：小一号、缩进，与链头对齐。链头自己已经在上面那
                   一行了，所以这里从第二站开始（不是把链头再说一遍）。 -->
              {#if rest(r).length}
                <ol class="steps">
                  {#each rest(r) as s, i (i)}
                    <li>
                      <span class="idx mono">{i + 2}</span>
                      <span class="mono binding">
                        <span class="prov">{s.provider}</span><span class="slash">/</span
                        ><span class="model">{s.model}</span>
                      </span>
                      {#if s.profile && s.profile !== c.profile}
                        <span class="pill from">{s.profile}</span>
                      {/if}
                      {#if s.latency_ms}
                        <span class="ms mono {toneClass(s.latency_tone)}" title={msTitle(s)}>
                          {s.latency_ms}{t("ms")}
                        </span>
                      {:else if s.probe}
                        <!-- 没有延迟样本但有探活结论（比如刚探过、样本还没落）。
                             画结论而不是留空：这两件事都是「这条站此刻怎么样」。 -->
                        <span class="dim grade mono" title={msTitle(s)}>{s.probe}</span>
                      {/if}
                    </li>
                  {/each}
                  {#if hidden(r)}
                    <li class="more dim">
                      {t("+{n} more on the chain", { n: hidden(r) })}
                    </li>
                  {/if}
                </ol>
              {/if}

              {#if r.note}
                <p class="dim skip">{r.note}</p>
              {/if}
            </li>
          {/each}
        {/if}
      </ul>
    </section>
  {/each}
</div>

{#if !cards.length}
  <p class="dim">{t("no profiles")}</p>
{/if}

<style>
  /* ---- 客户端标签 ---- */
  .agents {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-wrap: wrap;
    margin-bottom: 12px;
  }
  .agent {
    display: flex;
    align-items: center;
    gap: 7px;
    background: transparent;
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    padding: 5px 10px;
    color: var(--dim);
  }
  .agent:hover { color: var(--ink); border-color: var(--line); }
  /* 选中那一栏：底色 + 强调边，与侧栏/TabStrip 的 `.on` 同一套语气。 */
  .agent.on {
    color: var(--ink);
    background: var(--panel-2);
    border-color: var(--accent);
  }
  /* 没装的客户端**照常画出来**，只是暗一档：藏掉的话用户会以为它不被支持，而真相
     是「它在，只是这台机器上没有」（与 Concept.Locked 那条规矩同源）。 */
  .agent.off { opacity: 0.55; }
  .ico {
    width: 14px;
    height: 14px;
    flex: none;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.4;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .nm { font-size: 12px; font-weight: 600; }
  .cur { font-size: 11px; color: var(--dim); }
  /* 「自动」那一格：它不是一份档位的名字，是一种**状态**，所以给它一个记号（虚边框）
     而不是等宽字——旁边紧跟着的那一份才是名字。 */
  .auto {
    font-size: 10px;
    padding: 0 5px;
    border: 1px dashed color-mix(in srgb, currentColor 45%, transparent);
    border-radius: 999px;
    opacity: 0.85;
  }
  .off { font-size: 10px; }

  /* ---- 卡片 ---- */
  /* 自适应网格：一份 profile 一张卡，窄屏一列、宽屏两列。360px 是「档位名 + 链头
     + 延迟 + 一个按钮」排得下、又不会让一行长得读不完的那个宽度。 */
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
    gap: 12px;
    align-items: start;
  }
  .pc {
    background: var(--panel);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: 8px 10px 9px;
    /* 摊开时滚到它这里（见 toggle）：留一点空，别让卡边贴着滚动区上沿——贴住了
       看起来像被裁掉了一截。 */
    scroll-margin-top: 8px;
  }
  /* 摊开的那一张**占满整行**。
     为什么：它是网格里的一员，而摊开之后它比邻居高好几倍——留在自己那一列的话，
     同一行的另外两张卡下面会空出一大片（实测约 1400px 宽、几百像素高的空洞），
     而且那几站的名字挤在 360px 里还要截断。占满整行之后洞没有了，链也读得开。
     代价是下面所有卡会往下挪——但那本来就是「我要读这一条」这个动作该有的样子。 */
  .pc.folded-open { grid-column: 1 / -1; }
  /* 这一栏正在用的那一份：左边一条强调色 + 一圈亮一点的边。**只有一条线索**
     （颜色）不够——下面还有一句 `in use by …` 的陈述（见 .ok-pill）。 */
  .pc.on {
    border-color: color-mix(in srgb, var(--accent) 55%, var(--line));
    box-shadow: inset 3px 0 0 var(--accent);
  }
  .phead { display: flex; align-items: baseline; gap: 7px; flex-wrap: wrap; }
  /* 卡名那一格同时是摊开/收起开关。做成按钮之后要**把浏览器给按钮的默认样子全
     退掉**，否则它会变成一个小方块，而这行字在这张卡上是最大的那一行。 */
  .tog {
    display: flex;
    align-items: center;
    gap: 2px;
    margin-left: -4px;
    padding: 1px 4px 1px 2px;
    background: none;
    border: 0;
    border-radius: var(--radius-sm);
    color: inherit;
    cursor: pointer;
  }
  .tog:hover { background: var(--panel-2); }
  .chev {
    width: 12px;
    height: 12px;
    flex: none;
    fill: none;
    stroke: var(--dim);
    stroke-width: 1.8;
    stroke-linecap: round;
    stroke-linejoin: round;
    transition: transform 120ms ease;
  }
  /* 尖角指哪边 = 点了会往哪边去：收着时指右（摊开），摊开时指下。 */
  .tog[aria-expanded="true"] .chev { transform: rotate(90deg); }
  /* 动效只用来交代「这一下点到了」，所以两种状态都尊重系统的减少动效偏好。 */
  @media (prefers-reduced-motion: reduce) {
    .chev { transition: none; }
  }
  .pname { font-size: 13.5px; font-weight: 700; }
  .meta { font-size: 10.5px; color: var(--dim); margin: 0 0 5px; }

  .roles { list-style: none; margin: 0; padding: 0; }
  .roles > li + li { margin-top: 8px; }
  /* 收起来的一行：[档位] [链头 +N] [延迟]，三列。
     档位那一列**固定宽**：它是这一屏竖着对位的那条基准线，跟着名字长短走就散了。 */
  .grp {
    display: grid;
    grid-template-columns: 52px minmax(0, 1fr) auto;
    align-items: center;
    gap: 7px;
    /* 固定行高，不是 padding：两张卡的同一档要落在同一水平线上，卡的高度也要能算
       （头部 + 19px × 档位数）。 */
    height: 19px;
  }
  /* **每张卡收起来时一样高**：标准档位是五档（heavy/normal/mid/light/vision），
     而少写几档的 profile（只写 normal+light 那种）本来会矮一截，几张卡摆在一起就
     参差不齐——而这一屏正是拿来横向比的。按五档留够，少的那几张底下空着。
     比「每张都刚好贴合内容」值钱：参差的高度会让眼睛每次都要重新找行。 */
  .roles.flat { min-height: 95px; }
  .headline { display: flex; align-items: baseline; gap: 6px; min-width: 0; }
  .headline .head {
    font-size: 11.5px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  /* 链头之后还有几站。它是**深度**，不是内容——摊开才知道那几站是谁，所以这里
     只给个数（与摊开时那个 `+N more on the chain` 同一句话）。 */
  .depth { font-size: 10px; color: var(--dim); opacity: 0.7; flex: none; }
  .note { font-size: 10px; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  /* 一行 = [档位 + 链头] [延迟]，两列。
     - 前两样**必须包在一个格子里**（.tierline）：三样平铺进两列时，第三个（延迟）
       会被挤到下一行去，而那一行看起来像「这一档有两个链头」。
     - 延迟单独占一列、**右对齐**：这一列是拿来竖着比的（这一档比那一档快），跟着
       链头走就会随名字长短左右横跳，也就没法比了。min-width 让几张卡的这一列也
       对得上，tabular-nums 让数字本身对齐。
     - 链头不许被拆成两截（`nowrap`）：拆开之后那一行变成两行，一行高矮不齐。
       provider/model 中间那个斜杠是分隔符，读的时候是**两个**可以分别去查的名字。 */
  .tier {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: baseline;
    gap: 8px;
  }
  .tierline { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
  /* 档位名占固定宽度：不固定的话每行的链头会参差不齐，同上。 */
  .tname { color: var(--dim); font-size: 11.5px; min-width: 52px; white-space: nowrap; }
  .head { font-size: 11.5px; white-space: nowrap; }
  /* 延迟那一格：一个**度量**，所以给它一点底色把它从文字里分出来，但只用最小
     的手段（没有边框、没有圆角之外的东西）。数字用等宽 + 表格数字，几行竖着比
     的时候位是对齐的。 */
  .ms {
    font-size: 10.5px;
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
    text-align: right;
    min-width: 50px;
    padding: 0 5px;
    border-radius: 5px;
    background: color-mix(in srgb, currentColor 12%, transparent);
  }
  .grade { font-size: 10px; white-space: nowrap; }

  .steps {
    list-style: none;
    margin: 3px 0 0;
    padding: 0 0 0 59px; /* 与上面的链头对齐：缩进量 = tname 的宽度 + 那个 gap */
  }
  .steps > li {
    display: flex;
    align-items: baseline;
    gap: 6px;
    font-size: 11px;
    color: var(--dim);
    padding: 1px 0;
    flex-wrap: wrap;
  }
  .idx { min-width: 10px; opacity: 0.5; font-size: 10px; }
  .binding { color: var(--ink); opacity: 0.72; white-space: nowrap; }
  .slash { opacity: 0.45; }
  .from { font-size: 10px; padding: 0 5px; }
  .more { font-size: 10px; padding-left: 16px; }
  .skip { font-size: 11px; margin: 3px 0 0 59px; }

  /* 状态说明那套语气（与 ConceptCard 的 note 同一份）：**只换颜色，不换形状**。 */
  .ok-pill { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 45%, transparent); }
  .t-ok { color: var(--ok); }
  .t-warn { color: var(--warn); }
  .t-bad { color: var(--danger); }
</style>
