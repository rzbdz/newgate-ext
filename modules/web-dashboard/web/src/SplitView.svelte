<script lang="ts">
  // 同一个文件的**两半并排**：左边控件、右边原文。
  //
  // 为什么值得为它单开一块版面：改配置时最常见的动作是「这个值在文件里长什么样
  // / 我刚才那一下到底改了哪一行」，而瀑布流里这两张卡从来不在同一屏——看一眼
  // 原文要滚过去、再滚回来，回来时还得重新找到刚才那张卡。并排之后这件事变成
  // **零次操作**（两个都在屏幕上）。
  //
  // 配对由 App 按「写的是不是同一份文件」算好（见 nav.ts 的 fileOf），这里只管
  // 摆。所以它不认识任何模块，也不知道哪一半是「控件」。
  import type { Concept } from "./api";
  import ConceptCard from "./ConceptCard.svelte";
  import { t } from "./i18n";

  let {
    left,
    right,
    drafts,
    split,
    onEdit,
    onRevert,
    onToggleSplit,
  }: {
    left: Concept;
    right: Concept | undefined;
    drafts: Record<string, unknown>;
    split: boolean;
    onEdit: (id: string, v: unknown) => void;
    onRevert: (id: string) => void;
    onToggleSplit: () => void;
  } = $props();

  const two = $derived(!!right && split);
</script>

<div class="wrap">
  {#if right}
    <div class="bar">
      <button class="tiny ghost" onclick={onToggleSplit}>
        {two ? t("hide the raw file") : t("show the raw file")}
      </button>
    </div>
  {/if}
  <div class="split" class:two>
    <div class="pane-l">
      <ConceptCard
        concept={left}
        draft={drafts[left.id]}
        onEdit={(v) => onEdit(left.id, v)}
        onRevert={() => onRevert(left.id)}
      />
    </div>
    {#if two && right}
      <div class="pane-r">
        <ConceptCard
          concept={right}
          draft={drafts[right.id]}
          onEdit={(v) => onEdit(right.id, v)}
          onRevert={() => onRevert(right.id)}
        />
      </div>
    {/if}
  </div>
</div>

<style>
  .wrap {
    display: flex;
    flex-direction: column;
    min-height: 0;
    height: 100%;
  }
  /* 这一条只在有配对时才出现（App 不传 right 就没有它），而且只有一行高：
     它是一个「我可以把它收起来」的出口，不是一条工具栏。 */
  .bar {
    display: flex;
    justify-content: flex-end;
    padding: 0 0 6px;
  }
  .split {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 14px;
    min-height: 0;
    flex: 1;
  }
  /* 左控件、右原文。宽度按内容给：控件那一栏（一行是一个候选下拉 + 模型输入）
     最窄要 ~550px，原文那一栏窄一点也读得下去。 */
  .split.two { grid-template-columns: minmax(0, 1.15fr) minmax(0, 1fr); }
  .pane-l,
  .pane-r {
    min-width: 0;
    min-height: 0;
    overflow: auto;
  }
  /* 窄屏退化成上下堆叠（见 app.css 的媒体查询；这里给下面那一半一个高度上限，
     否则它又会变成一条瀑布）。 */
  @media (max-width: 1000px) {
    .split.two {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto minmax(180px, 45vh);
    }
  }
</style>
