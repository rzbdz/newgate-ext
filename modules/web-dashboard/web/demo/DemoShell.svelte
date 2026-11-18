<script lang="ts">
  // The demo page's shell: **a column of sections on the left, that section's cards on the right** - exactly how the product UI looks.
  //
  // # Why it does not reuse App.svelte
  //
  // App.svelte needs the full backend: the snapshot / apply / preview endpoints, a 3-second auto
  // refresh, the conflict dialog, cross-section filtering, shortcuts, URL routing. None of that is needed here - all it needs is
  // "one snapshot + a little local state". Forcing it into App would mean adding a pile of "if there is no backend,
  // then ..." branches to the product path, and those branches run less than once a year (they only ever run on the demo page).
  //
  // # What it reuses is **the cards and the side-by-side layout**
  //
  // ConceptCard and the seven kinds are pure (give data and they draw, no fetch anywhere), and so is SplitView
  // (it only lays out the two halves). So use them directly here - the cards on screen are **the same component and
  // the same CSS** as in the product. That is exactly why this demo page is worth existing: it is not a screenshot, and not a version
  // redrawn to look like one.
  //
  // The sidebar and the card column are **written here** (following the look of the product's Sidebar / TabStrip, but each written separately):
  // those two components take the product's props (hit counts, vertical toggle, shortcuts ...), and adding a
  // "what if there is no backend" switch to them for the demo page costs the same as the rule above.
  //
  // # The fake half
  //
  // Editing, switching section, switching card, and side by side all really take effect in local state; save / action / row buttons only pop
  // "this is a demo". **Not a line lands on disk** - there is no backend here to land on.
  import SplitView from "../src/SplitView.svelte";
  import ThemePicker from "../src/ThemePicker.svelte";
  import { fileOf } from "../src/nav";
  import { t } from "../src/i18n";
  import { d } from "./strings";
  import { applyTheme } from "../src/theme";
  import type { Concept, ConceptAction, RowAction, Section, Themes } from "../src/api";
  import { snapshot, themes, useDemoLanguage } from "./data";

  useDemoLanguage();

  const snap = snapshot();
  const sections: Section[] = snap.sections;
  const concepts: Concept[] = snap.concepts;

  /**
   * Landing spot: **the section that declares `default`** (see Section.default in api.ts).
   *
   * Same rule as the product's `defaultRoute()`, except it has none of that function's two fallbacks - the demo page's sections and
   * cards are hard-coded, and "a landing spot is declared but that section is empty" cannot happen. The product needs fallbacks because modules
   * can be turned off and profiles can be deleted.
   */
  const landingSec = sections.find((s) => s.default) ?? sections[0];
  const landing = landingSec.source;

  /**
   * The sidebar's row order: **the section that declared a landing spot comes first** (no group heading), then **by the purpose each
   * section declares**, and within a bucket by source order.
   *
   * **The same rule** as the product's Sidebar.svelte (three headings, the same order, unclassified last).
   * Rewriting it here was a tradeoff made at the time (the two components take different props), but the rule **has forked twice** -
   * once home was not pinned first, once this was still clustering by the contributor's `group` free text. The demo page is worth
   * existing precisely because "it is that UI", so the rule must be word for word the same.
   */
  function sidebarRows(): ({ kind: "group"; name: string } | { kind: "sec"; s: Section })[] {
    const out: ({ kind: "group"; name: string } | { kind: "sec"; s: Section })[] = [];
    const landingSec = sections.find((s) => s.default) ?? sections[0];
    if (landingSec) out.push({ kind: "sec", s: landingSec });

    const ORDER = ["clients", "routes", "settings"];
    const head = (f: string): string =>
      f === "clients" ? t("Clients") : f === "routes" ? t("Routes") : t("Settings");

    const byField = new Map<string, Section[]>();
    for (const s of sections) {
      if (s === landingSec) continue;
      const f = s.fields || "_other";
      const g = byField.get(f);
      if (g) g.push(s);
      else byField.set(f, [s]);
    }
    for (const f of [...ORDER, "_other"]) {
      const members = byField.get(f);
      if (!members?.length) continue;
      out.push({ kind: "group", name: f === "_other" ? t("Other") : head(f) });
      for (const s of members) out.push({ kind: "sec", s });
    }
    return out;
  }
  const rows = sidebarRows();

  /**
   * Home mode (the same path as the product App.svelte's `route.home`).
   *
   * **It opens on it**: this page shows that UI, and that UI today only has cards on the opening screen. To enter the full UI
   * press the gear on the left (see that one in the template below) - the demo page has no address bar to jump with, so it uses state
   * rather than hash, but what the two places **lay out** must be word for word the same.
   */
  let home = $state(true);

  let active = $state<string>(landing);
  /** The selected card in the current section (empty = the section's first card, same as the product). */
  let card = $state<string>("");
  let split = $state<boolean>(true);
  /** Local drafts: stored by concept id, the same path as App's `drafts` in the product. */
  let drafts = $state<Record<string, unknown>>({});
  let toast = $state<string>("");

  const section = $derived(sections.find((s) => s.source === active));
  const cards = $derived(concepts.filter((c) => c.source === active));
  /** In home mode it draws **the first** card of the landing section (same as the product App's homeConcept). */
  const homeCard = $derived(concepts.filter((c) => c.source === landing)[0]);

  const current = $derived(home ? homeCard : (cards.find((c) => c.id === card) ?? cards[0]));
  /** The shape of the card-order column: a horizontal strip when there are few cards, a vertical column on the left when many - same rule as the product's threshold. */
  const vertical = $derived(cards.length > 8);

  /**
   * The other half of **the same file** as the current card (control half <-> original-text half, see fileOf in nav.ts).
   *
   * The criterion is word for word the same as that part in the product (same file, one code one non-code). There is only one difference: the product
   * hides the original-text half from the nav (one card per file), while the demo page does not - here the point is to
   * make the existence of these two halves **visible**, so the side-by-side switch makes sense.
   */
  const pair = $derived.by(() => {
    if (!current) return undefined;
    const f = fileOf(current);
    if (!f) return undefined;
    return concepts.find(
      (x) =>
        x.id !== current.id &&
        x.source === current.source &&
        fileOf(x) === f &&
        (current.kind === "code" ? x.kind !== "code" : x.kind === "code"),
    );
  });

  /** When side by side, which half is on the left: the control half (same as the product). */
  const leftCard = $derived(current?.kind === "code" && pair ? pair : current);
  const rightCard = $derived(current?.kind === "code" && pair ? current : pair);

  const dirty = $derived(Object.keys(drafts).length);

  /**
   * Theme: the demo page **keeps it only in this browser's memory**.
   *
   * In the real UI this cell POSTs back to the backend and is recorded in state.json (see theme.go in web-dashboard);
   * the demo page has no backend to store it, so the pick takes effect only on the spot and a refresh returns to the factory token set. This is **the only**
   * place where behavior differs from the real UI, and it differs reasonably: every piece of state on the demo page has this "poke at it
   * and leave no trace" nature.
   */
  let theme = $state<Themes>({ ...themes(), active: "" });

  function pickTheme(id: string) {
    theme = { ...theme, active: id };
  }

  // The same path as the one in App.svelte (applyTheme only touches the DOM), so what the two sides land
  // is word for word the same - the demo page shows exactly the colors of the real UI.
  $effect(() => {
    applyTheme(theme);
  });

  function say(msg: string) {
    toast = msg;
    // The notice disappears by itself: the demo page should not leave something that must be closed by hand (it only says "there is no
    // backend here", not an error the user has to handle).
    setTimeout(() => (toast = ""), 2600);
  }

  function noBackend(action: string) {
    say(d("this is a demo — “{action}” would go to the real daemon; nothing is written here", { action }));
  }

  /** Switch section: switching a section resets the cards and drafts (drafts are stored per card, and keeping them when switching only confuses). */
  function pickSection(source: string) {
    active = source;
    card = "";
    drafts = {};
  }

  /** Switch card (switching clears the draft - same as switching cards in the product). */
  function pickCard(id: string) {
    card = id;
    drafts = {};
  }

  function onEdit(id: string, v: unknown) {
    drafts = { ...drafts, [id]: v };
  }

  function onRevert(id: string) {
    const next = { ...drafts };
    delete next[id];
    drafts = next;
  }

  /** The SplitView path needs the card id (it holds both halves), and the signature follows the product's. */
  function onAction(id: string, a: ConceptAction) {
    noBackend(a.label);
  }

  function onRowAction(id: string, row: string, a: RowAction) {
    say(
      d("this is a demo — “{action}” on “{row}” would talk to the real daemon", {
        action: a.label,
        row,
      }),
    );
  }

  function onDeleteFile() {
    say(d("this is a demo — no file would be deleted"));
  }

  /** The save in the header: it really writes to disk, so here too only a sentence is said. */
  function onSave() {
    say(d("this is a demo — save would write these files; here it writes nothing"));
  }
</script>

<div class="shell" class:home>
  <header class="top">
    <!-- **The same layout** as the product's: in home mode a gear on the left (enter the full UI), and on the right
         this section's global actions + theme + save; in the full UI the left is the brand + filter box (the demo page does not offer
         real filtering, which only exists in the product), and the right has one more home button (back to home). -->
    {#if home}
      <button class="icon" title={t("settings")} aria-label={t("settings")} onclick={() => (home = false)}>
        <svg class="gear" viewBox="0 0 24 24" aria-hidden="true">
          <circle cx="12" cy="12" r="3.1" />
          <path
            d="M19.5 14.6a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 1 1-2.9 2.9l-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.5v.2a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.9.3l-.1.1a2 2 0 1 1-2.9-2.9l.1-.1a1.7 1.7 0 0 0 .3-1.9 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.9l-.1-.1a2 2 0 1 1 2.9-2.9l.1.1a1.7 1.7 0 0 0 1.9.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.9-.3l.1-.1a2 2 0 1 1 2.9 2.9l-.1.1a1.7 1.7 0 0 0-.3 1.9V9a1.7 1.7 0 0 0 1.5 1h.2a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"
          />
        </svg>
      </button>
    {/if}
    <strong class="brand">newgate</strong>
    <span class="spacer"></span>
    <!-- This section's global actions in home mode (today just the `auto fallback` one). The demo page has no
         backend, so clicking it only pops a sentence. -->
    {#if home}
      {#each landingSec?.actions ?? [] as a (a.id)}
        {#if a.id === "fallback"}
          <button class="tiny ghost toned" data-tone={a.tone} onclick={() => noBackend(a.label)}>
            {a.label}
          </button>
        {/if}
      {/each}
    {/if}
    <!-- Theme switching: the same shape as that cell in the real UI, only it takes effect only here (see pickTheme). -->
    {#if theme.themes.length}
      <ThemePicker doc={theme} onPick={pickTheme} />
    {/if}
    <button class="primary" onclick={onSave} disabled={!dirty}>
      {t("save")}{dirty ? ` (${dirty})` : ""}
    </button>
    {#if !home}
      <button class="icon" title={t("home mode")} aria-label={t("home mode")} onclick={() => (home = true)}>
        <svg viewBox="0 0 16 16" aria-hidden="true">
          <path d="M2.6 7.4 8 3l5.4 4.4V13a.6.6 0 0 1-.6.6h-3v-3.4h-3.6v3.4h-3A.6.6 0 0 1 2.6 13z" />
        </svg>
      </button>
    {/if}
  </header>

  {#if !home}
  <nav class="side">
    <p class="head">{t("sections")}</p>
    {#each rows as r, i (`${r.kind}:${r.kind === "group" ? r.name : r.s.source}:${i}`)}
      {#if r.kind === "group"}
        <!-- A group heading **is not a button** (it represents a category, not a section). -->
        <div class="group">{r.name}</div>
      {:else}
        <button
          class="row"
          class:on={r.s.source === active}
          title={r.s.source}
          onclick={() => pickSection(r.s.source)}
        >
          <span class="label">{r.s.title}</span>
          <span class="badge">{concepts.filter((c) => c.source === r.s.source).length}</span>
        </button>
      {/if}
    {/each}
  </nav>
  {/if}

  <section class="content" class:subcol={vertical}>
    {#if !home}
    <div class="secbar">
      <span class="name">{section?.title}</span>
      <span class="spacer"></span>
      <!-- The actions this section injects (like "create another tier file"). The demo page draws them -
           they are the same kind of thing as the buttons on cards, both given by the contributor. -->
      {#each section?.actions ?? [] as a (a.id)}
        <button class="tiny ghost" onclick={() => noBackend(a.label)}>{a.label}</button>
      {/each}
    </div>
    {/if}

    {#if !home && cards.length > 1}
      {#if vertical}
        <!-- When there are many cards (config's kind): one column on the left, scanned at a glance. -->
        <nav class="v">
          {#each cards as c (c.id)}
            <button
              class="row"
              class:on={c.id === current?.id}
              title={c.id}
              onclick={() => pickCard(c.id)}
            >
              <span class="label">{c.title}</span>
              {#if drafts[c.id] !== undefined}<span class="dot" title={t("unsaved")}></span>{/if}
            </button>
          {/each}
        </nav>
      {:else}
        <!-- When there are few cards: a horizontal strip on top. -->
        <div class="tabs">
          {#each cards as c (c.id)}
            <button
              class="tab"
              class:on={c.id === current?.id}
              title={c.id}
              onclick={() => pickCard(c.id)}
            >
              {c.title}
            </button>
          {/each}
        </div>
      {/if}
    {/if}

    <div class="pane">
      {#if current}
        <SplitView
          left={leftCard}
          right={rightCard}
          {drafts}
          previews={{}}
          {split}
          onEdit={onEdit}
          onRevert={onRevert}
          {onDeleteFile}
          {onAction}
          {onRowAction}
          bare={home}
          onToggleSplit={() => (split = !split)}
        />
      {:else}
        <p class="dim">{t("nothing in this section matches.")}</p>
      {/if}
    </div>
  </section>
</div>

{#if toast}
  <div class="toast">{toast}</div>
{/if}

<style>
  /* The demo page's layout is written here (the product's is in app.css's .shell, for App's grid).
     Shape follows it: a 208px directory on the left, a full-width header on top, content bottom right. */
  .shell {
    display: grid;
    grid-template-columns: 208px minmax(0, 1fr);
    grid-template-rows: auto minmax(0, 1fr);
    height: 100%;
  }
  /* Home mode: the directory column is not drawn, and content takes the full width (same as `.shell.home`
     in the product's app.css). */
  .shell.home { grid-template-columns: minmax(0, 1fr); }
  .shell.home .content { grid-template-columns: minmax(0, 1fr); grid-template-rows: auto minmax(0, 1fr); }

  .top {
    grid-column: 1 / -1;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 9px 14px;
    border-bottom: 1px solid var(--line);
    background: var(--panel);
  }
  .brand { font-size: 13px; }
  .spacer { flex: 1; }

  .side {
    overflow-y: auto;
    border-right: 1px solid var(--line);
    background: var(--panel);
    padding: 8px 0 12px;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .side .head {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.7px;
    color: var(--dim);
    margin: 4px 12px 6px;
  }
  .side .group {
    margin: 10px 12px 3px;
    font-size: 11px;
    letter-spacing: 0.6px;
    text-transform: uppercase;
    color: var(--dim);
  }
  .side .row {
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
  .side .row:hover { background: var(--panel-2); }
  .side .row.on { background: var(--panel-2); box-shadow: inset 2px 0 0 var(--accent); }
  .side .label { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .side .badge { font-family: var(--mono); font-size: 11px; color: var(--dim); }

  .content {
    display: grid;
    grid-template-rows: auto auto minmax(0, 1fr);
    min-height: 0;
    overflow: hidden;
  }
  /* Sections with many cards: one more 210px card directory column (same layout as the product, the rule is >8 cards). */
  .content.subcol {
    grid-template-columns: 210px minmax(0, 1fr);
    grid-template-rows: auto minmax(0, 1fr);
  }
  .content.subcol > .secbar { grid-column: 1 / -1; }
  .content.subcol > .pane { grid-column: 2; grid-row: 2; min-height: 0; min-width: 0; }

  .secbar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 16px;
    border-bottom: 1px solid var(--line);
    background: var(--panel);
  }
  .secbar .name {
    font-size: 11px;
    letter-spacing: 0.6px;
    text-transform: uppercase;
    color: var(--dim);
  }

  /* The vertical card directory (when there are many cards). Scrolling lands on it: it is a definitely sized container on a grid row. */
  .v {
    grid-column: 1;
    grid-row: 2;
    overflow-y: auto;
    border-right: 1px solid var(--line);
    background: var(--panel);
    padding: 6px 0 10px;
    display: flex;
    flex-direction: column;
    gap: 1px;
    min-height: 0;
  }
  .v .row {
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
  .v .row:hover { background: var(--panel-2); }
  .v .row.on { background: var(--panel-2); box-shadow: inset 2px 0 0 var(--accent); }
  .v .label { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dot { width: 6px; height: 6px; border-radius: 50%; background: var(--warn); flex: none; }

  /* The horizontal card strip (when there are few cards). */
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
  .tabs .tab {
    background: transparent;
    border: 0;
    border-bottom: 2px solid transparent;
    border-radius: 0;
    padding: 7px 10px;
    color: var(--dim);
  }
  .tabs .tab:hover { color: var(--ink); }
  .tabs .tab.on { color: var(--ink); border-bottom-color: var(--accent); }

  /* min-height: 0 and minmax(0, 1fr) are the load-bearing walls of this layout (see the same explanation in app.css):
     without them, the scrollable thing inside pushes the whole page taller instead of scrolling itself. */
  .pane {
    overflow: auto;
    padding: 14px 16px;
    min-height: 0;
    min-width: 0;
  }

  /* Narrow screens: the sidebar folds into a horizontal band on top, and the card directory column stacks too (same fallback set as the product). */
  @media (max-width: 900px) {
    .shell {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto auto minmax(0, 1fr);
    }
    .top { grid-column: 1; }
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
    .side .head, .side .group { display: none; }
    .side .row { width: auto; }
    .side .row.on { box-shadow: inset 0 -2px 0 var(--accent); }
    .content.subcol { grid-template-columns: minmax(0, 1fr); grid-template-rows: auto auto minmax(0, 1fr); }
    .content.subcol > .secbar { grid-column: 1; }
    .v {
      grid-column: 1;
      grid-row: auto;
      flex-direction: row;
      overflow-x: auto;
      overflow-y: hidden;
      border-right: 0;
      border-bottom: 1px solid var(--line);
    }
    .content.subcol > .pane { grid-column: 1; grid-row: 3; }
  }
</style>
