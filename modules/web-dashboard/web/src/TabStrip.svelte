<script lang="ts">
  // Navigation within a section: **a horizontal tab strip** when there are few cards, and when there are many (config's dozen cards)
  // **a vertical column on the left**.
  //
  // Why a second shape: a dozen horizontal tabs can only be squeezed into one horizontal scroll, and scrolling through it is more tiring than paging through a directory
  // - "find a card" should be a one-glance vertical browse. The threshold is one constant (App's TAB_OVERFLOW),
  // App uses it to decide the layout and this only renders; a section with one card shows neither - a toolbar with only one button
  // is pure noise, and pushes content down too.
  //
  // # Grouping: two-level headings
  //
  // A card can carry `group`, two levels separated by "/" (see Concept.Group in lib/view): the first segment is
  // the **major bucket** (`tiers` - separating "the pile added one day at a time" from "the two things you configure right after install"
  // in the layout), and the second segment is the **family** (`claude-cheap` tucked under `claude`).
  //
  // There is only one rule: **a segment draws a heading only when it has >=2 cards under it** - a "family" of one plus
  // a line of heading is pure noise, and pushes that line down too. So contributors may write a group on every card without
  // counting first. A card is indented by a few cells = how many **drawn** ancestors it has (`ark` is indented
  // one cell under the major bucket, `claude-cheap` two cells under the major bucket and the family).
  //
  // Both shapes share one event and one set of rows: one click switches that card. Card order follows the backend
  // (`(Source, Order, ID)` ordering, see Snapshot in lib/view): run the same assembly twice, and the
  // position must be the same - otherwise "which tier is on row 3" changes on every refresh.
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

  /** A card's group path (`tiers/claude` -> `["tiers", "claude"]`). Empty = not in any bucket. */
  function segsOf(c: Concept): string[] {
    return (c.group ?? "").split("/").filter(Boolean);
  }

  type Row =
    | { kind: "head"; name: string; depth: number }
    | { kind: "card"; c: Concept; depth: number };

  const rows = $derived.by<Row[]>(() => {
    const join = (segs: string[], n: number) => segs.slice(0, n).join("/");
    // Count once first: how many cards are under each prefix segment (descendants included). Count only, do not draw.
    //
    // Why "including descendants" rather than "exactly this segment": all seventeen cards under `tiers` belong to some family,
    // and counting by "exactly" would leave the major bucket with 0 cards forever, and its heading never drawn.
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
      // The ancestors that will be drawn. **Once a segment has fewer than two, deeper ones certainly cannot have two** (deeper segments are its
      // subset), so this is break, not continue.
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

  /** Indentation: 12px per level, base 12px (the same base as .group's padding). */
  const indent = (depth: number) => `${12 + depth * 12}px`;
  const key = (r: Row, i: number) => (r.kind === "head" ? `h:${r.name}:${i}` : r.c.id);
</script>

{#if cards.length > 1}
  {#if vertical}
    <!-- Vertical column: one fixed column, scrolling vertically. A row = the card title (translated by the backend), title is the card id -
         titles collide (a dozen `mt-xx — Gallium`), and only the id is a stable locator. -->
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
    <!-- Horizontal strip: the same rows, but the titles shrink to a separator (indentation does not fit on a strip, and is unnecessary -
         it only appears in sections with few cards, which can be seen at a glance). -->
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
  /* ---- horizontal tab strip ---- */
  /* No wrapping (horizontal scroll): if the strip wrapped each time a section gained a card, content would be pushed down a row. */
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
  /* Group headings inside the strip: a small vertical line plus a line of small text, read as "the cards below are one category". */
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

  /* ---- vertical column (when there are many cards) ---- */
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

  /* Group heading: one size smaller, with an all-caps grouping feel, but **not a button** - clicking it must not switch cards
     (it represents a category, not a card). Indentation is given by the template per level (see indent). */
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
