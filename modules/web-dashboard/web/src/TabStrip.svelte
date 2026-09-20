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
</script>

{#if cards.length > 1}
  {#if vertical}
    <!-- 竖向栏：固定一列、纵向滚。行 = 卡标题（后端翻好的），副标是卡片 id——
         标题会撞名（十几个 `mt-xx — Gallium`），id 才是稳定的定位。 -->
    <nav class="v">
      {#each cards as c (c.id)}
        <button
          class="row"
          class:on={c.id === active}
          onclick={() => onPick(c.id)}
          title={c.id}
        >
          <span class="label">{c.title}</span>
          {#if drafts[c.id] !== undefined}<span class="dot" title={t("unsaved")}></span>{/if}
          {#if c.error}<span class="broken" title={c.error}>!</span>{/if}
        </button>
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