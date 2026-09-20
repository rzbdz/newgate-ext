<script lang="ts">
  // 一节内部的 tab 条：**一次只画一张卡**。
  //
  // 一节的卡片数差别很大：配置有五张（四个档位 + 全局 + 每个源文件），熔断只有
  // 一张。只有一张的节**不出 tab 条**——一条只有一个按钮的工具条是纯噪音，而它
  // 还会把内容往下推一行。
  //
  // 卡片顺序跟着后端来（`(Source, ID)` 排序，见 lib/view 的 Snapshot）：同一份
  // 装配跑两次，位置必须一样。
  import type { Concept } from "./api";
  import { t } from "./i18n";

  let {
    cards,
    active,
    drafts,
    onPick,
  }: {
    cards: Concept[];
    active: string;
    drafts: Record<string, unknown>;
    onPick: (id: string) => void;
  } = $props();
</script>

{#if cards.length > 1}
  <!-- 不换行（横向滚动）：一节多一张卡就换行的话，内容会被往下推一行，
       而「同一份配置看两次，位置要一样」是这个界面的底线。 -->
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

<style>
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
  .tab.on {
    color: var(--ink);
    border-bottom-color: var(--accent);
  }
  .label {
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
