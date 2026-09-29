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
    /**
     * 这一张是不是**「默认」那张**（见内核 view.OverviewCard.Auto）。
     *
     * 它与 profile 卡长得几乎一样，说的却是两件事：在这一张上点「给 claude 用」
     * 是让 claude **跟着全局默认走**；在 ds 那张上点同一句话是把 claude **固定**在
     * ds 上。两者的路由此刻相同，改掉全局之后一个跟着变、一个不变——所以它们必须
     * 是两张卡（Clash 里一个 group 既能直接选节点、又能引用另一个 group）。
     */
    auto?: boolean;
    file?: string;
    default?: boolean;
    roles: Row[];
    actions?: Act[];
  };
  type Agent = { id: string; name: string; profile?: string; own?: boolean; ready?: boolean };
  type Data = { agents: Agent[]; auto?: Card; cards: Card[] };

  import { t } from "../i18n";
  import type { ConceptAction } from "../api";

  let {
    data,
    onAction,
    onOpenFile,
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
    /**
     * 跳到**编辑这份文件**的那张卡上（配置那一节里对应 profile 的那张）。
     *
     * 一趟导航，不是动作：它不改盘上的东西，所以不该走 `onAction`（那条路跑完会
     * 清草稿、整份重读——对「我只是想去看看那个文件」这件事来说全是副作用）。
     */
    onOpenFile?: (file: string) => void;
  } = $props();

  const agents = $derived(data?.agents ?? []);
  const cards = $derived(data?.cards ?? []);
  /** 「默认」那张卡（见 Card.auto）。没有可选的 profile 时后端不报它。 */
  const auto = $derived<Card | undefined>(data?.auto);

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

  /**
   * 这一栏此刻**是不是跟着默认走**。
   *
   * 两件不同的事都落在这里：`own` 为假 = 这一家没自己设过、跟着全局；而「全部客户端」
   * 那一档**本身就是全局默认**，所以它也在跟着（它没有「上一级」可跟，那个默认就是
   * 它自己）。
   */
  const follows = $derived(!current?.id || !current?.own);

  /**
   * 这一张卡是不是**这一栏此刻在用的**那一个。
   *
   * 高亮落在**哪一张上**，正是「跟着」与「固定」的区别所在：跟着默认时高亮在「默认」
   * 那张上，固定住时高亮在被固定的那一份上。两张卡的内容可能一模一样（默认恰好指向
   * 它），而那正是需要这条规则的原因——颜色说的是**关系**，不是内容。
   */
  function isActive(c: Card): boolean {
    return follows ? !!c.auto : !c.auto && c.profile === current?.profile;
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
      // 「默认」那张卡上的 `use:global:<profile>` **不是按钮**：它们是这张卡**里面**
      // 那一排成员（见模板里的 .members）。摆到卡片头上会与「给谁用」那一类混成
      // 一堆同义词，而它们做的事完全不同（一个改全局默认，一个把这一家固定住）。
      if (c.auto && a.id.startsWith("use:")) continue;
      if (a.id.startsWith("auto:")) {
        // 「跟着默认走」（`auto:<agent>`）：它只长在「默认」那张卡上（那件事只有在
        // 那张卡上说得通），而点它改的是**当前这一栏**的状态——所以切到别家时不该
        // 还留着，那是别人家的状态，点下去改的是别人。
        if (c.auto && a.id === `auto:${who}`) out.push(a);
        continue;
      }
      if (!a.id.startsWith("use:")) {
        out.push(a); // 探活那类：与标签无关，照画。
        continue;
      }
      // 把**这一份**指给当前这一栏（`use:<agent>:<profile>`）。这一族只长在
      // profile 卡上——「默认」那张有它自己的 `use:global:*`，在上面被跳过了。
      if (!c.auto && a.id === `use:${who}:${c.profile}`) out.push(a);
    }
    return out;
  }

  /** 网格里摆的几张卡：「默认」在最前，后面是每一份 profile。 */
  const shown = $derived<Card[]>(auto ? [auto, ...cards] : cards);

  /**
   * 一张卡的键（each 的 key、摊开状态按它存）。
   *
   * 「默认」那张**没有 profile 名可用**——它的 profile 是它此刻指向的那一份，会跟着
   * 变；拿它当键的话，切一次成员整张卡就被当成另一张重建了（摊开状态当场丢掉）。
   * 所以它用固定的一个记号。
   */
  function keyOf(c: Card): string {
    return c.auto ? "\u0000auto" : c.profile;
  }

  /**
   * 「默认」那张卡**里面**那一排成员：它能指向的每一份 profile。
   *
   * 判据是后端报没报那个动作（`use:global:<profile>`），不是前端自己把 cards 列一
   * 遍：能指向哪几份是贡献者的事（比如**此刻指向的那一份不出现**——选它自己什么都
   * 不会变）。前端列一遍就等于把那条规则抄了第二份，而两处迟早会不一致。
   */
  function members(c: Card): { profile: string; act?: Act }[] {
    return cards.map((m) => ({
      profile: m.profile,
      act: (c.actions ?? []).find((a) => a.id === `use:global:${m.profile}`),
    }));
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
          <!-- 标签上只写它此刻实际走的那一份。**不在这里说「自动/固定」**：那是
               「选了哪一个」这件事，而它由下面网格里**高亮落在哪一张卡上**回答
               （见 isActive）——两处各说一遍，迟早会有一处说错。 -->
          <span class="cur mono">{a.profile}</span>
        {/if}
        {#if a.ready === false}
          <span class="dim off">{t("not installed")}</span>
        {/if}
      </button>
    {/each}
  </div>
{/if}

<div class="grid">
  {#each shown as c (keyOf(c))}
    <section class="pc" class:on={isActive(c)} class:folded-open={!!open[keyOf(c)]}>
      <header class="phead">
        <!-- 链名（= profile 名）是这一张卡上最大的一行字：用户在这一屏上做的唯一
             一个决定是「用哪一份」，而名字就是那个决定的宾语。
             它同时是**摊开/收起**的开关（仿 Clash 的组头）：整行都点得动，比在角落
             放一个 12px 的小三角好按得多，而这一屏上最频繁的动作就是「扫一遍」和
             「摊开看这条」。 -->
        <button class="tog" aria-expanded={!!open[keyOf(c)]} onclick={(ev) => toggle(keyOf(c), ev)}>
          <svg class="chev" viewBox="0 0 16 16" aria-hidden="true">
            <path d="M6 4l4 4-4 4" />
          </svg>
          {#if c.auto}
            <!-- 「默认」那张：名字是**默认**，后面括着它此刻指向哪一份。
                 括号里那个值会跟着全局默认变——这正是它与 ds 那张卡的分别：
                 两张此刻显示同一条链，但这一张说的是「大家都跟着它」，那一张说的是
                 「这一份本身」。 -->
            <span class="pname">{t("default")}</span>
            {#if c.profile}
              <span class="cur">{t("(currently {profile})", { profile: c.profile })}</span>
            {/if}
          {:else}
            <span class="pname">{c.profile}</span>
          {/if}
        </button>
        <span class="spacer"></span>
        {#if isActive(c)}
          <!-- 「这一栏正用着它」是一句**陈述**，不是按钮：把它指给它是没有意义的
               事（所以后端根本不报那个动作）。与 Concept.Note 那条同源——「不画
               那个按钮」与「什么都不说」是两件事。 -->
          <!-- 「正在使用」是**同一句话**，两张卡上都这么写——谁被选中不是靠这张纸上
               的字，而是靠**高亮落在哪一张卡上**（见 isActive）。这样读的人看到的
               是「这一栏选了哪一个」，而不是两句要互相比较才分得出差别的话。 -->
          <span class="pill ok-pill" data-using="1">
            {current?.id ? t("in use by {client}", { client: current.name }) : t("in use")}
          </span>
        {/if}
        {#each actions(c) as a (a.id)}
          <button class="tiny ghost" data-action={a.id} onclick={() => onAction?.(a)}>
            {a.label}
          </button>
        {/each}
      </header>

      {#if c.file}
        <!-- 文件路径同时是**去编辑它**的入口：这一屏说的是「此刻走哪条链」，改链要
             去配置那一节里这份文件的那张卡。路径本身是最自然的落点——用户看着它想
             的就是「我要改的就是这个文件」。
             不能跳时（没有这个回调，或者那份文件此刻没有可编辑的卡）就照旧画成一
             行字，而不是一个点了没反应的链接。 -->
        {#if onOpenFile}
          <button class="meta mono link" onclick={() => onOpenFile(c.file!)} title={t("edit this file")}>
            {c.file}
          </button>
        {:else}
          <div class="meta mono">{c.file}</div>
        {/if}
      {/if}

      {#if c.auto}
        <!-- 这一张**里面**那一排成员：点一个就把「默认」指到那一份上（整张卡的内容
             跟着换成它的链）。此刻指向的那一个没有动作，所以它画成选中态而不是按钮
             ——见 members()（那条规则住在后端，前端只照着画）。 -->
        <div class="members">
          {#each members(c) as m (m.profile)}
            <button
              class="member mono"
              class:on={m.profile === c.profile}
              disabled={!m.act}
              data-action={m.act?.id}
              onclick={() => m.act && onAction?.(m.act)}
            >
              {m.profile}
            </button>
          {/each}
        </div>
      {/if}

      <ul class="roles" class:flat={!open[keyOf(c)]}>
        {#if !open[keyOf(c)]}
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
  /* 路径那一行做成按钮，但要**长得跟原来那行字一样**：它是这一张卡上最不起眼的
     东西，而它现在是入口了——把它做成一个显眼的按钮，会让每一张卡上多出一个比
     「给谁用」还抢眼的东西。悬停时才加下划线。 */
  .meta.link {
    display: block;
    padding: 0;
    background: none;
    border: 0;
    border-radius: 3px;
    text-align: left;
    cursor: pointer;
  }
  .meta.link:hover { color: var(--ink); text-decoration: underline; }

  /* 「默认」那张卡里面那一排成员：一份 profile 一个。摆成一条会换行的横排——
     它们是一个**集合**（此刻能指向哪几份），横着读比竖着读快；换行而不是横向滚动：
     十几种 profile 在一张卡里滚动条没人找得到。 */
  .members {
    display: flex;
    flex-wrap: wrap;
    gap: 3px;
    margin: 0 0 5px;
  }
  .member {
    font-size: 10.5px;
    padding: 1px 6px;
    background: transparent;
    border: 1px solid var(--line);
    border-radius: 999px;
    color: var(--dim);
    cursor: pointer;
  }
  .member:hover:not(:disabled) { color: var(--ink); border-color: var(--accent); }
  /* 此刻指向的那一个：它没有动作（选它自己什么都不会变），所以画成**选中的样子**
     而不是一个按下去没反应的按钮。 */
  .member.on {
    color: var(--ink);
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 16%, transparent);
    cursor: default;
  }

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
