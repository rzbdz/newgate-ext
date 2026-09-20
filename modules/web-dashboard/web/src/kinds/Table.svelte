<script lang="ts">
  // 只读的表格（形状见内核 lib/view 的 Table）。
  //
  // 两个约定值得写下来，因为前端是它们唯一的执行者：
  //
  //  1. **格子是「列 ID → 格子」的 map，不是数组**。所以下面按列去取，取不到就当
  //     这一格是空的——**不是**按下标取。按列 ID 取，列顺序就是纯展示的事
  //     （将来按窄屏重排、或者让用户拖列），不会把格子串位。
  //
  //     行本身从 2026-09-21 起是一个**对象**（`{id, cells, actions}`），不再是
  //     裸的 map：行上可以挂按钮，而按钮回传时需要「哪一行」这个身份。
  //  2. **tone 是语义，不是颜色**。这里把 ok/warn/bad 翻成 CSS 变量；内核不传
  //     ANSI 也不传色值，否则同一份数据在终端与网页上就得各写一遍转义。
  //     认不出来的 tone 一律当没给：一个陌生的词不该让整格变成不可读的黑块。
  type Cell = { text: string; tone?: string };
  type Column = { id: string; label: string; align?: string };
  /** 行上的一个按钮。label 是**贡献者写好的那句人话**（后端已经翻过），界面不译。 */
  type Act = { id: string; label: string };
  type Row = { id?: string; cells: Record<string, Cell>; actions?: Act[] };
  type Data = { columns: Column[]; rows: Row[] };

  import { t } from "../i18n";
  import type { RowAction } from "../api";

  let {
    data,
    onAction,
  }: {
    data: Data;
    /**
     * 跑这一行上的一个按钮（见 api.ts 的 runRowAction）。
     *
     * 界面只把「哪一行、哪个按钮」转回去，**不知道那件事会干什么**：探一条
     * binding 要不要发请求、发完记到哪儿，是那一行拥有者的事。参数里没有「哪个
     * 概念」——那是卡片的事，App 手里有。
     */
    onAction?: (row: string, a: RowAction) => void;
  } = $props();

  const columns = $derived(data?.columns ?? []);
  const rows = $derived(data?.rows ?? []);

  /**
   * 有没有哪一行带按钮——决定要不要多画一列。
   *
   * 不无条件加一列：没有动作的表（模块清单、接管表）会多出一条空列，而那列的
   * 表头没有一个说得通的词可写（写「动作」等于给一张永远空着的列表头）。
   */
  const hasActions = $derived(rows.some((r) => (r?.actions?.length ?? 0) > 0));

  function cell(row: Row, id: string): Cell {
    return row?.cells?.[id] ?? { text: "" };
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
        {#if hasActions}
          <!-- 表头**留空**，不写一个词：这一列里放的是每行自己的按钮，没有
               一个能概括它们的名字（「操作」是最接近的，但它什么都没说）。 -->
          <th class="acts"></th>
        {/if}
      </tr>
    </thead>
    <tbody>
      {#each rows as row, i (row.id ?? i)}
        <tr>
          {#each columns as col (col.id)}
            {@const c = cell(row, col.id)}
            <td class:right={col.align === "right"} class={toneClass(c.tone)}>{c.text}</td>
          {/each}
          {#if hasActions}
            <td class="acts">
              {#each row.actions ?? [] as a (a.id)}
                <!-- 按钮的字由贡献者给（后端翻好），所以这里不套 t()。 -->
                <button
                  class="tiny ghost"
                  data-action={a.id}
                  title={a.label}
                  onclick={() => onAction?.(row.id ?? String(i), a)}
                >
                  {a.label}
                </button>
              {/each}
            </td>
          {/if}
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
  .acts { width: 1%; } /* 按钮那一列贴着右边，别把数据的列挤窄 */
  .acts button + button { margin-left: 4px; }
  .t-ok { color: var(--ok); }
  .t-warn { color: var(--warn); }
  .t-bad { color: var(--danger); }
</style>
