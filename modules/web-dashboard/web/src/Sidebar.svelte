<script lang="ts">
  // 左边这一栏：**目录**。点一下切一节，不再往下滚。
  //
  // 它为什么存在：原来所有卡片按来源分组、一路往下铺（实测 4303px ≈ 4.8 屏），
  // 于是「改一个开关」这件四步就能做完的事，第一步是滚三屏。侧栏把这一步变成
  // 一次点击，而且是**位置固定**的——同一节永远在同一个地方，肌肉记忆有效。
  //
  // 它不认识任何模块：栏目名由各模块在登记时报（后端 `view.Sections`），这里只
  // 负责画。名字**不走 t()**：那是模块的内容，后端已经按当时的语言翻好了。
  import type { Section } from "./api";
  import { t } from "./i18n";

  let {
    sections,
    active,
    counts,
    onPick,
  }: {
    sections: Section[];
    active: string;
    counts: Map<string, { total: number; dirty: number }>;
    onPick: (source: string) => void;
  } = $props();
</script>

<nav class="side">
  <p class="head">{t("sections")}</p>
  {#each sections as s (s.source)}
    {@const n = counts.get(s.source)}
    <button
      class="row"
      class:on={s.source === active}
      onclick={() => onPick(s.source)}
      title={s.source}
    >
      <span class="name">{s.title}</span>
      {#if n?.dirty}
        <span class="badge dirty" title={t("unsaved")}>{n.dirty}</span>
      {:else if n?.total}
        <span class="badge">{n.total}</span>
      {/if}
    </button>
  {/each}
  {#if !sections.length}
    <p class="dim pad">{t("nothing is contributing a view in this process.")}</p>
  {/if}
</nav>

<style>
  .side {
    overflow-y: auto;
    border-right: 1px solid var(--line);
    background: var(--panel);
    padding: 8px 0 12px;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .head {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.7px;
    color: var(--dim);
    margin: 4px 12px 6px;
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
  /* 当前那一节：一条左侧竖线 + 底色。不用整块反白——侧栏是常驻的，抢眼会疲劳。 */
  .row.on {
    background: var(--panel-2);
    box-shadow: inset 2px 0 0 var(--accent);
  }
  .name {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .badge {
    font-family: var(--mono);
    font-size: 11px;
    color: var(--dim);
  }
  /* 有没保存的改动时，徽标换成**改动数**并着色：那是一个会丢东西的状态，
     它得比「这张卡在这儿」显眼。 */
  .badge.dirty {
    color: var(--bg);
    background: var(--warn);
    border-radius: 999px;
    padding: 0 6px;
  }
  .pad { padding: 0 12px; }

  /* 窄屏：竖栏收成顶上一条横带（见 app.css 的 .shell 媒体查询，那边把第二行
     改成了 auto）。208px 的侧栏在 900px 以下会把 mapping-editor 挤到读不了。 */
  @media (max-width: 900px) {
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
    .head { display: none; }
    .row { width: auto; }
    .row.on { box-shadow: inset 0 -2px 0 var(--accent); }
  }
</style>
