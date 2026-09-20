<script lang="ts">
  // 一节内部的导航：卡片不多时是**横向 tab 条**，很多时（config 那种 38 张卡）是
  // **左侧一个竖向栏**。
  //
  // 为什么要有第二种形状：38 张横 tab 只能挤在一条横向滚动里，滚起来比翻目录还
  // 累——「找一张卡」本该是竖着浏览一眼的事。阈值（>卡片数太多就走竖栏）是一处
  // 常量，App 拿它决定布局，这里只负责渲染；选阈值时按「横条里能一眼扫完」来定，
  // 超过一屏的卡数就该竖着列。只有一张卡的节两者都不出——一条只有一个按钮的
  // 工具条是纯噪音，还会把内容往下推。
  //
  // 两种形态同用一份事件：点一下切那张卡。卡片顺序都跟着后端来（`(Source, ID)`
  // 排序，见 lib/view 的 Snapshot）：同一份装配跑两次，位置必须一样。
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

  // 竖栏里按 `group` 归拢：一族档位（`claude` 与 `claude-cheap`）现在长这样——
  //
  //   claude
  //     claude — Claude 家族
  //     claude-cheap — …
  //
  // 平的十六行看不出谁是谁的变体（用户的原话是「为什么不做一下缩进分类」）。
  // **只有 ≥2 个成员的族才出标题**：一个人的「族」加一行标题是纯噪音，还会把那
  // 一行往下推。分组的判据由后端给（lib/view 的 Concept.Group），这里不认识任何
  // 模块，也不知道「档位」是什么。
  type Row = { kind: "head"; name: string } | { kind: "card"; c: Concept; in: boolean };
  const rows = $derived.by<Row[]>(() => {
    const members = new Map<string, number>();
    for (const c of cards) if (c.group) members.set(c.group, (members.get(c.group) ?? 0) + 1);
    const out: Row[] = [];
    const done = new Set<string>();
    for (const c of cards) {
      const g = c.group;
      const grouped = !!g && (members.get(g!) ?? 0) > 1;
      if (grouped && !done.has(g!)) {
        done.add(g!);
        out.push({ kind: "head", name: g! });
      }
      out.push({ kind: "card", c, in: grouped });
    }
    return out;
  });
</script>

{#if cards.length > 1}
  {#if vertical}
    <!-- 竖向栏：固定一列、纵向滚。行 = 卡标题（后端翻好的），副标是卡片 id——
         标题会撞名（十几个 `mt-xx — Gallium`），id 才是稳定的定位。 -->
    <nav class="v">
      {#each rows as r, i (r.kind === "head" ? "h:" + r.name + ":" + i : r.c.id)}
        {#if r.kind === "head"}
          <div class="group">{r.name}</div>
        {:else}
          <button
            class="row"
            class:on={r.c.id === active}
            class:in={r.in}
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
    <div class="tabs">
      {#each cards as c (c.id)}
        <button
          class="tab"
          class:on={c.id === active}
          onclick={() => onPick(c.id)}
          title={c.id}
        >
          <span class="label">{c.title}</span>
          {#if drafts[c.id] !== undefined}<span class="dot" title={t("unsaved")}></span>{/if}
          {#if c.error}<span class="broken" title={c.error}>!</span>{/if}
        </button>
      {/each}
    </div>
  {/if}
{/if}

<style>
  /* ---- 横向 tab 条 ---- */
  /* 不换行（横向滚动）：一节多一张卡就换行的话，内容会被往下推一行。 */
  .tabs {
    display: flex;
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

  /* 家族的标题：小一号、全大写式的分组感，但**不是按钮**——点它不该切卡
     （它代表的是一族，不是一张）。 */
  .group {
    margin: 8px 0 2px;
    padding: 0 12px;
    font-size: 11px;
    letter-spacing: 0.6px;
    text-transform: uppercase;
    color: var(--dim);
  }
  /* 组内的成员缩进一级：缩进就是「我属于上面那一族」的全部表达。 */
  .row.in { padding-left: 24px; }

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