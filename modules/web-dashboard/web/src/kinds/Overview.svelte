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
  type Agent = { id: string; name: string; profile?: string; ready?: boolean };
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
  {#each cards as c (c.profile)}
    <section class="pc" class:on={isActive(c)}>
      <header class="phead">
        <!-- 链名（= profile 名）是这一张卡上最大的一行字：用户在这一屏上做的唯一
             一个决定是「用哪一份」，而名字就是那个决定的宾语。 -->
        <span class="pname">{c.profile}</span>
        {#if c.default}
          <span class="pill ok-pill">{t("default")}</span>
        {/if}
        <span class="spacer"></span>
        {#if isActive(c)}
          <!-- 「这一栏正用着它」是一句**陈述**，不是按钮：把它指给它是没有意义的
               事（所以后端根本不报那个动作）。与 Concept.Note 那条同源——「不画
               那个按钮」与「什么都不说」是两件事。 -->
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
        <div class="meta mono">{c.file}</div>
      {/if}

      <ul class="roles">
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
    padding: 10px 12px 12px;
  }
  /* 这一栏正在用的那一份：左边一条强调色 + 一圈亮一点的边。**只有一条线索**
     （颜色）不够——下面还有一句 `in use by …` 的陈述（见 .ok-pill）。 */
  .pc.on {
    border-color: color-mix(in srgb, var(--accent) 55%, var(--line));
    box-shadow: inset 3px 0 0 var(--accent);
  }
  .phead { display: flex; align-items: baseline; gap: 7px; flex-wrap: wrap; }
  .pname { font-size: 14px; font-weight: 700; }
  .meta { font-size: 11px; color: var(--dim); margin: 1px 0 7px; }

  .roles { list-style: none; margin: 0; padding: 0; }
  .roles > li + li { margin-top: 8px; }
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
  .tname { color: var(--dim); font-size: 12px; min-width: 58px; white-space: nowrap; }
  .head { font-size: 12px; white-space: nowrap; }
  /* 延迟那一格：一个**度量**，所以给它一点底色把它从文字里分出来，但只用最小
     的手段（没有边框、没有圆角之外的东西）。数字用等宽 + 表格数字，几行竖着比
     的时候位是对齐的。 */
  .ms {
    font-size: 11px;
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
    text-align: right;
    min-width: 56px;
    padding: 0 5px;
    border-radius: 5px;
    background: color-mix(in srgb, currentColor 12%, transparent);
  }
  .grade { font-size: 10px; white-space: nowrap; }

  .steps {
    list-style: none;
    margin: 3px 0 0;
    padding: 0 0 0 58px; /* 与上面的链头对齐：缩进量 = tname 的宽度 */
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
  .skip { font-size: 11px; margin: 3px 0 0 58px; }

  /* 状态说明那套语气（与 ConceptCard 的 note 同一份）：**只换颜色，不换形状**。 */
  .ok-pill { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 45%, transparent); }
  .t-ok { color: var(--ok); }
  .t-warn { color: var(--warn); }
  .t-bad { color: var(--danger); }
</style>
