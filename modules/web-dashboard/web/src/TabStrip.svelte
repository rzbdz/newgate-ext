<script lang="ts">
  // 一节内部的导航：卡片不多时是**横向 tab 条**，很多时（config 那种十几张卡）是
  // **左侧一个竖向栏**。
  //
  // 为什么要有第二种形状：十几张横 tab 只能挤在一条横向滚动里，滚起来比翻目录还
  // 累——「找一张卡」本该是竖着浏览一眼的事。阈值是一处常量（App 的 TAB_OVERFLOW），
  // App 拿它决定布局，这里只负责渲染；只有一张卡的节两者都不出——一条只有一个按钮
  // 的工具条是纯噪音，还会把内容往下推。
  //
  // # 分组：两级的标题
  //
  // 卡可以带 `group`，两级、用 "/" 分开（见 lib/view 的 Concept.Group）：前一段是
  // **大档**（`档位`——把「一天天加出来的一堆」与「装完就要配的那两项」在版面上
  // 分开），后一段是**族**（`claude` 底下缩着 `claude-cheap`）。
  //
  // 判据只有一条：**某一段底下有 ≥2 张卡时，那一段才画标题**——一个人的「族」加
  // 一行标题是纯噪音，还会把那一行往下推。所以贡献者尽管给每张卡都写分组，不必
  // 自己先数一遍。一张卡缩进几格 = 它有几个**画出来了的**祖先（`ark` 在大档下缩
  // 一格，`claude-cheap` 在大档和族下缩两格）。
  //
  // 两种形状同用一份事件与同一份 rows：点一下切那张卡。卡片顺序都跟着后端来
  // （`(Source, Order, ID)` 排序，见 lib/view 的 Snapshot）：同一份装配跑两次，
  // 位置必须一样——否则「第 3 行是哪个档位」这件事每次刷新都在变。
  import type { Concept } from "./api";
  import { t } from "./i18n";

  let {
    cards,
    active,
    drafts,
    vertical,
    onPick,
  }: {
    cards: Concept[];
    active: string;
    drafts: Record<string, unknown>;
    vertical?: boolean;
    onPick: (id: string) => void;
  } = $props();

  /** 一张卡的分组路径（`档位/claude` → `["档位", "claude"]`）。空 = 不归任何一档。 */
  function segsOf(c: Concept): string[] {
    return (c.group ?? "").split("/").filter(Boolean);
  }

  type Row =
    | { kind: "head"; name: string; depth: number }
    | { kind: "card"; c: Concept; depth: number };

  const rows = $derived.by<Row[]>(() => {
    const join = (segs: string[], n: number) => segs.slice(0, n).join("/");
    // 先量一次：每一段前缀底下有几张卡（含后代）。只数不画。
    //
    // 为什么是「含后代」而不是「正好这一段」：`档位` 底下那十七张卡全都属于某个族，
    // 按「正好」数的话大档永远只有 0 张卡，标题一辈子画不出来。
    const size = new Map<string, number>();
    for (const c of cards) {
      const segs = segsOf(c);
      for (let i = 1; i <= segs.length; i++) {
        const p = join(segs, i);
        size.set(p, (size.get(p) ?? 0) + 1);
      }
    }
    const out: Row[] = [];
    const drawn = new Set<string>();
    for (const c of cards) {
      const segs = segsOf(c);
      // 会画出来的祖先。**一旦某一段不够两张，再往下更不可能够**（更深的段是它的
      // 子集），所以这里是 break 不是 continue。
      const shown: string[] = [];
      for (let i = 1; i <= segs.length; i++) {
        const p = join(segs, i);
        if ((size.get(p) ?? 0) < 2) break;
        shown.push(p);
      }
      for (const p of shown) {
        if (drawn.has(p)) continue;
        drawn.add(p);
        out.push({ kind: "head", name: p.slice(p.lastIndexOf("/") + 1), depth: shown.indexOf(p) });
      }
      out.push({ kind: "card", c, depth: shown.length });
    }
    return out;
  });

  /** 缩进：每一级 12px，基准 12px（与 .group 的 padding 同一个基数）。 */
  const indent = (depth: number) => `${12 + depth * 12}px`;
  const key = (r: Row, i: number) => (r.kind === "head" ? `h:${r.name}:${i}` : r.c.id);
</script>

{#if cards.length > 1}
  {#if vertical}
    <!-- 竖向栏：固定一列、纵向滚。行 = 卡标题（后端翻好的），title 是卡片 id——
         标题会撞名（十几个 `mt-xx — Gallium`），id 才是稳定的定位。 -->
    <nav class="v">
      {#each rows as r, i (key(r, i))}
        {#if r.kind === "head"}
          <div class="group" style="padding-left: {indent(r.depth)}">{r.name}</div>
        {:else}
          <button
            class="row"
            class:on={r.c.id === active}
            style="padding-left: {indent(r.depth)}"
            onclick={() => onPick(r.c.id)}
            title={r.c.id}
          >
            <span class="label">{r.c.title}</span>
            {#if drafts[r.c.id] !== undefined}<span class="dot" title={t("unsaved")}></span>{/if}
            {#if r.c.error}<span class="broken" title={r.c.error}>!</span>{/if}
          </button>
        {/if}
      {/each}
    </nav>
  {:else}
    <!-- 横条：同一个 rows，但标题缩成一段分隔（横条上摆不下缩进，也没必要——
         它只在卡少的节里出现，一眼看得完）。 -->
    <div class="tabs">
      {#each rows as r, i (key(r, i))}
        {#if r.kind === "head"}
          <span class="sep">{r.name}</span>
        {:else}
          <button
            class="tab"
            class:on={r.c.id === active}
            onclick={() => onPick(r.c.id)}
            title={r.c.id}
          >
            <span class="label">{r.c.title}</span>
            {#if drafts[r.c.id] !== undefined}<span class="dot" title={t("unsaved")}></span>{/if}
            {#if r.c.error}<span class="broken" title={r.c.error}>!</span>{/if}
          </button>
        {/if}
      {/each}
    </div>
  {/if}
{/if}

<style>
  /* ---- 横向 tab 条 ---- */
  /* 不换行（横向滚动）：一节多一张卡就换行的话，内容会被往下推一行。 */
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
  .tab {
    background: transparent;
    border: 0;
    border-bottom: 2px solid transparent;
    border-radius: 0;
    padding: 7px 10px;
    color: var(--dim);
    display: flex;
    align-items: center;
    gap: 6px;
    max-width: 260px;
  }
  .tab:hover { color: var(--ink); }
  .tab.on { color: var(--ink); border-bottom-color: var(--accent); }
  /* 横条里的分组标题：一小段竖线加一行小字，读作「下面这几张是一类」。 */
  .sep {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 4px 0 10px;
    padding-left: 10px;
    border-left: 1px solid var(--line);
    font-size: 11px;
    letter-spacing: 0.6px;
    text-transform: uppercase;
    color: var(--dim);
    flex: none;
  }

  /* ---- 竖向栏（卡多时） ---- */
  .v {
    overflow-y: auto;
    border-right: 1px solid var(--line);
    background: var(--panel);
    padding: 6px 0 10px;
    display: flex;
    flex-direction: column;
    gap: 1px;
    min-height: 0;
  }
  .row {
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
  .row:hover { background: var(--panel-2); }
  .row.on {
    background: var(--panel-2);
    box-shadow: inset 2px 0 0 var(--accent);
  }

  /* 分组标题：小一号、全大写式的分组感，但**不是按钮**——点它不该切卡
     （它代表的是一类，不是一张）。缩进由模板按层级给（见 indent）。 */
  .group {
    margin: 8px 0 2px;
    padding: 0 12px;
    font-size: 11px;
    letter-spacing: 0.6px;
    text-transform: uppercase;
    color: var(--dim);
  }

  .label {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--warn);
    flex: none;
  }
  .broken { color: var(--danger); font-weight: 700; }
</style>
