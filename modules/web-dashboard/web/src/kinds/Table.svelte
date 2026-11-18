<script lang="ts">
  // A read-only table (shape in the core lib/view's Table).
  //
  // Two conventions are worth writing down, because the frontend is their only enforcer:
  //
  //  1. **A cell is a "column ID -> cell" map, not an array**. So things are fetched by column below, and a miss means
  //     this cell is empty - **not** fetched by index. Fetching by column ID makes column order purely a display matter
  //     (reordering for narrow screens later, or letting users drag columns), without shifting cells around.
  //
  //     The row itself has been an **object** (`{id, cells, actions}`) since 2026-09-21, no longer a
  //     bare map: a row can host buttons, and a button needs "which row" as its identity when it reports back.
  //  2. **tone is semantics, not color**. Here ok/warn/bad is translated into CSS variables; the core passes neither
  //     ANSI nor color values, otherwise the same data would need its escaping written twice, once for the terminal and once for the web.
  //     Any unrecognized tone is treated as not given: an unfamiliar word should not turn the whole cell into an unreadable black block.
  // A non-empty href = this cell is a link (see view.Cell). The address is given by the contributor - it is usually
  // computed, and only the contributor knows the computation.
  type Cell = { text: string; tone?: string; href?: string };
  type Column = { id: string; label: string; align?: string };
  /** A button on a row. label is **the human sentence written by the contributor** (already translated by the backend); the UI does not translate it. */
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
     * Run a button on this row (see runRowAction in api.ts).
     *
     * The UI only forwards "which row, which button", and **does not know what that will do**: whether probing a
     * binding sends a request and where the result is recorded is the row owner's business. The parameter has no "which
     * concept" - that is the card's business, and App has it.
     */
    onAction?: (row: string, a: RowAction) => void;
  } = $props();

  const columns = $derived(data?.columns ?? []);
  const rows = $derived(data?.rows ?? []);

  /**
   * Whether any row has a button - decides whether to draw an extra column.
   *
   * Do not add a column unconditionally: tables without actions (module list, takeover table) would get an empty column, and that column's
   * header has no sensible word to write (writing "action" would be a header on a permanently empty list).
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
          <!-- Leave the table header **empty**, do not write a word: this column holds each row's own buttons, and there is no
               name that covers them ("actions" is the closest, but it says nothing). -->
          <th class="acts"></th>
        {/if}
      </tr>
    </thead>
    <tbody>
      {#each rows as row, i (row.id ?? i)}
        <tr>
          {#each columns as col (col.id)}
            {@const c = cell(row, col.id)}
            <td class:right={col.align === "right"} class={toneClass(c.tone)}>
              {#if c.href}
                <!-- target="_blank" is **fixed**: this is a single-page app with drafts,
                     and a link that replaces the current page equals throwing away that unsaved edit. -->
                <a href={c.href} target="_blank" rel="noopener noreferrer">{c.text}</a>
              {:else}{c.text}{/if}
            </td>
          {/each}
          {#if hasActions}
            <td class="acts">
              {#each row.actions ?? [] as a (a.id)}
                <!-- The button text is given by the contributor (translated by the backend), so no t() here. -->
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
  <!-- An empty table must look different from "the table is broken": the header stays, and below it says clearly that there is no data. -->
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
  a { color: inherit; text-decoration: underline; text-underline-offset: 2px; }
  a:hover { color: var(--accent); }
  tbody tr:hover { background: var(--panel-2); }
  .acts { width: 1%; } /* the button column hugs the right edge, do not squeeze the data columns */
  .acts button + button { margin-left: 4px; }
  .t-ok { color: var(--ok); }
  .t-warn { color: var(--warn); }
  .t-bad { color: var(--danger); }
</style>
