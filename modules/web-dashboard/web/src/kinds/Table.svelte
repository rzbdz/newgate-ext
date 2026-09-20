<script lang="ts">
  // 只读的表格（形状见内核 lib/view 的 Table）。
  //
  // 两个约定值得写下来，因为前端是它们唯一的执行者：
  //
  //  1. **行是「列 ID → 格子」的 map，不是数组**。所以下面按列去取，取不到就当
  //     这一格是空的——**不是**按下标取。按列 ID 取，列顺序就是纯展示的事
  //     （将来按窄屏重排、或者让用户拖列），不会把格子串位。
  //  2. **tone 是语义，不是颜色**。这里把 ok/warn/bad 翻成 CSS 变量；内核不传
  //     ANSI 也不传色值，否则同一份数据在终端与网页上就得各写一遍转义。
  //     认不出来的 tone 一律当没给：一个陌生的词不该让整格变成不可读的黑块。
  type Cell = { text: string; tone?: string };
  type Column = { id: string; label: string; align?: string };
  type Data = { columns: Column[]; rows: Record<string, Cell>[] };

  import { t } from "../i18n";

  let { data }: { data: Data } = $props();

  const columns = $derived(data?.columns ?? []);
  const rows = $derived(data?.rows ?? []);

  function cell(row: Record<string, Cell>, id: string): Cell {
    return row?.[id] ?? { text: "" };
  }

  function toneClass(tone: string | undefined): string {
    return tone === "ok" || tone === "warn" || tone === "bad" ? `t-${tone}` : "";
  }
</script>

<div class="wrap">
  <table>
    <thead>
      <tr>
        {#each columns as col (col.id)}
          <th class:right={col.align === "right"}>{col.label}</th>
        {/each}
      </tr>
    </thead>
    <tbody>
      {#each rows as row, i (i)}
        <tr>
          {#each columns as col (col.id)}
            {@const c = cell(row, col.id)}
            <td class:right={col.align === "right"} class={toneClass(c.tone)}>{c.text}</td>
          {/each}
        </tr>
      {/each}
    </tbody>
  </table>
</div>

{#if !rows.length}
  <!-- 空表要跟「表坏了」长得不一样：表头照在，下面说清楚是没数据。 -->
  <p class="dim">{t("no rows")}</p>
{/if}

<style>
  .wrap { overflow-x: auto; }
  table { border-collapse: collapse; width: 100%; font-size: 12px; }
  th, td {
    text-align: left;
    padding: 5px 10px;
    border-bottom: 1px solid var(--line);
    white-space: nowrap;
  }
  th { color: var(--dim); font-weight: 600; }
  td { font-family: var(--mono); }
  .right { text-align: right; }
  tbody tr:hover { background: var(--panel-2); }
  .t-ok { color: var(--ok); }
  .t-warn { color: var(--warn); }
  .t-bad { color: var(--danger); }
</style>
