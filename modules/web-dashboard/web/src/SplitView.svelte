<script lang="ts">
  // **Two halves of the same file side by side**: controls on the left, original text on the right.
  //
  // Why it deserves a layout of its own: the most common action when changing config is "what does this value look like in the file
  // / which line did I just change", and in the waterfall these two cards are never on the same screen - to look at
  // the original text you scroll over, then scroll back, and on the way back you have to find that card again. Side by side this becomes
  // **zero operations** (both are on screen).
  //
  // Pairing is computed by App on "do they write the same file" (see fileOf in nav.ts); this component only
  // arranges. So it knows no module, and does not know which half is the "control".
  import type { Concept, ConceptAction, RowAction } from "./api";
  import ConceptCard from "./ConceptCard.svelte";
  import { t } from "./i18n";

  let {
    left,
    right,
    drafts,
    previews,
    split,
    onEdit,
    onRevert,
    onDeleteFile,
    onAction,
    onRowAction,
    onOpenFile,
    bare,
    onToggleSplit,
  }: {
    left: Concept;
    right: Concept | undefined;
    drafts: Record<string, unknown>;
    // "What this card should show when the other half of the same file has a draft like this", stored by card id (see
    // previews in App.svelte). **Only the control half has it** - the original-text half is itself the original text.
    previews: Record<string, unknown>;
    split: boolean;
    onEdit: (id: string, v: unknown) => void;
    onRevert: (id: string) => void;
    // They all act on **the card's own file**, so they take no argument (see kinds/MappingEditor).
    onDeleteFile?: () => void;
    // Run an action on a card (see ConceptAction in api.ts). **It needs an id**: this component
    // holds two cards at once (the control half and the original-text half), and the action belongs to only one of them.
    onAction?: (id: string, a: ConceptAction) => void;
    // Run an action on a row (see RowAction in api.ts). Same reason for the id: two
    // cards are in hand, and that row belongs to only one of them.
    onRowAction?: (id: string, row: string, a: RowAction) => void;
    // Jump to the card that **edits this file** (see openFile in App.svelte). No card id:
    // where to go is decided by the file, and both cards talk about the same file - carrying an id would only tempt the caller
    // to branch on "which half", and that is exactly what this jump must avoid.
    onOpenFile?: (file: string) => void;
    /** See ConceptCard.bare (home mode has only this one card on screen). */
    bare?: boolean;
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
        preview={previews[left.id]}
        onEdit={(v) => onEdit(left.id, v)}
        onRevert={() => onRevert(left.id)}
        {onDeleteFile}
        onAction={(a) => onAction?.(left.id, a)}
        onRowAction={(row, a) => onRowAction?.(left.id, row, a)}
        {onOpenFile}
        {bare}
      />
    </div>
    {#if two && right}
      <div class="pane-r">
        <ConceptCard
          concept={right}
          draft={drafts[right.id]}
          onEdit={(v) => onEdit(right.id, v)}
          onRevert={() => onRevert(right.id)}
          {onDeleteFile}
          onAction={(a) => onAction?.(right.id, a)}
          onRowAction={(row, a) => onRowAction?.(right.id, row, a)}
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
  /* This one only appears when there is a pair (App passes no right and it is absent), and it is only one row tall:
     it is an exit that says "I can tuck this away", not a toolbar. */
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
  /* Controls on the left, original text on the right. Widths follow content: the control column (one row is a candidate dropdown + a model input)
     needs ~550px at its narrowest, and the original-text column is still readable a bit narrower. */
  .split.two { grid-template-columns: minmax(0, 1.15fr) minmax(0, 1fr); }
  .pane-l,
  .pane-r {
    min-width: 0;
    min-height: 0;
    overflow: auto;
  }
  /* On narrow screens it degrades to stacking (see the media query in app.css; here the lower half gets a height cap,
     otherwise it turns into a waterfall again). */
  @media (max-width: 1000px) {
    .split.two {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto minmax(180px, 45vh);
    }
  }
</style>
