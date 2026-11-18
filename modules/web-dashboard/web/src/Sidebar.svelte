<script lang="ts">
  // This left column: **the directory**. One click switches section, no more scrolling down.
  //
  // Why it exists: all cards used to be grouped by source and laid out downwards (measured 4303px ≈ 4.8 screens),
  // so "change one switch", a four-step job, started with scrolling three screens. The sidebar turns that step into
  // one click, and at a **fixed position** - the same section is always in the same place, and muscle memory works.
  //
  // It knows no modules: section names are reported by each module at registration (the backend's `view.Sections`), and here it only
  // draws. The names **do not go through t()**: that is module content, already translated by the backend into the current language.
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
    counts: Map<string, { total: number; dirty: number; locked: number }>;
    onPick: (source: string) => void;
  } = $props();

  /**
   * A sidebar row: a few group headings + each section.
   *
   * **The ungrouped sections come first** (today only "config", which is also the largest and most used one):
   * the whole value of a group heading is separating it from the categories below, and when it is at the very top not even a heading needs
   * drawing - the position itself is that sentence.
   *
   * A group heading **is written where the contributor-declared group first appears**, and within a group the backend's source order is kept
   * (stable: run the same assembly twice, and the same section is always in the same place).
   *
   * The difference from TabStrip (there "draw the heading only with >=2 members") is deliberate: there the groups are **inferred** by the core from
   * tier names (`claude-cheap` belongs to `claude`), and a group of one is pure noise;
   * here the module **says** "which category I belong to" - and if it says so, it should be visible. And if headings were hidden by member count here too,
   * a group with only one declaration would make that section **jump to the top** (it becomes "ungrouped"),
   * and the position changing with the member count is much worse than one extra line of small text.
   */
  type Row = { kind: "head"; name: string } | { kind: "sec"; s: Section };

  /**
   * The three headings in the directory, and their order.
   *
   * The criterion is **the purpose the section declares itself** (`Section.fields`, see the core view.SectionInfo.Fields),
   * not the group name the contributor writes - that is free text (`In("Clients")` / `In("data plane")`), and two
   * modules writing it differently will not be grouped together. So the **words** shown are decided here (they must be translated and rewordings must be possible),
   * while **which bucket** it goes into is decided by the machine marker the backend gives.
   *
   * Sections that declare no purpose (none today) go in the last bucket, not hidden: a section disappearing from the directory is much worse
   * than it appearing under an imprecise heading.
   */
  const ORDER = ["clients", "routes", "settings"] as const;
  // The heading text is **looked up on every render** (not once at module load): the language `t()` reads lives in
  // a module-level variable in i18n.ts, and it is only set after the snapshot returns - looking it up once at the top level would leave these
  // three headings permanently in English in a Chinese UI (measured: Clients was English while the Claude Code under it was Chinese).
  const head = (f: string): string =>
    f === "clients" ? t("Clients") : f === "routes" ? t("Routes") : t("Settings");

  const rows = $derived.by<Row[]>(() => {
    const out: Row[] = [];

    // The section that declared a landing spot (the first screen) **sorts first in the directory**, and gets no group heading.
    //
    // Why use landing spot as the criterion rather than adding an Order to Section: the landing spot is already what the module itself declares as
    // "where the user should look first" (see Section.Default in lib/view), and the directory's first
    // entry says exactly that too - two statements of the same question, answered with the same fact. Adding a second field leaves room for
    // "landing spot in one place, rank in another, the two disagreeing".
    const landing = sections.find((s) => s.default);
    if (landing) out.push({ kind: "sec", s: landing });

    // Each bucket **clusters together**: first order the buckets by ORDER, then list all the members of a bucket at once.
    //
    // Cannot "insert headings as you go" - that version did that, and it was wrong: members come in source order, so
    // a bucket's second section would be cut off behind another bucket's heading (measured: `gateway` sorts after `claudecode`,
    // so the heading only had half the members under it). A heading says "the sections below are one category", so below it must really be
    // that category.
    const byField = new Map<string, Section[]>();
    for (const s of sections) {
      if (s === landing) continue;
      const f = s.fields || "_other";
      const g = byField.get(f);
      if (g) g.push(s);
      else byField.set(f, [s]);
    }
    for (const f of [...ORDER, "_other"]) {
      const members = byField.get(f);
      if (!members?.length) continue;
      out.push({ kind: "head", name: f === "_other" ? t("Other") : head(f) });
      for (const s of members) out.push({ kind: "sec", s });
    }
    return out;
  });
</script>

<nav class="side">
  <p class="head">{t("sections")}</p>
  {#each rows as r, i (r.kind === "head" ? `h:${r.name}:${i}` : r.s.source)}
    {#if r.kind === "head"}
      <div class="group">{r.name}</div>
    {:else}
      {@const n = counts.get(r.s.source)}
      <!-- The whole section is locked gray: **every card in this section is locked** (today it is "this client is not installed").
           Note that it only **looks** gray - it can still be clicked, because the details must still be viewable (the user's own words:
           "the details screen just cannot be operated, but to show off our features, I suggest still allowing viewing").
           So no disabled here: that means "cannot click", while what we want is "look and you know it cannot be operated,
           and to operate it you must install it first". -->
      <button
        class="row"
        class:on={r.s.source === active}
        class:locked={!!n && n.total > 0 && n.locked === n.total}
        onclick={() => onPick(r.s.source)}
        title={r.s.source}
      >
        <span class="name">{r.s.title}</span>
        {#if n?.dirty}
          <span class="badge dirty" title={t("unsaved")}>{n.dirty}</span>
        {:else if n?.total}
          <span class="badge">{n.total}</span>
        {/if}
      </button>
    {/if}
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
  /* The whole section locked gray (see the comment in the template): gray is a **statement**, not a disabled gesture - this section is still clickable,
     it just tells you "the things inside are not usable on this machine". So lower the opacity, not the interaction. */
  .row.locked { opacity: 0.5; }
  .row.locked:hover { opacity: 0.75; }
  /* Group heading: one size smaller than section names and more spaced out, read as "the sections below are one category". **Not a button** -
     clicking it is meaningless (it represents a category, not a section), and making it a button only adds a
     target that does nothing. */
  .group {
    margin: 10px 12px 3px;
    font-size: 11px;
    letter-spacing: 0.6px;
    text-transform: uppercase;
    color: var(--dim);
  }
  /* The current section: a vertical line on the left + background tint. No full inverted block - the sidebar is permanent, and being loud all the time tires the eye. */
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
  /* When there are unsaved changes, the badge switches to **the change count** and is colored: that is a state where things can be lost,
     so it must stand out more than "this card is here". */
  .badge.dirty {
    color: var(--bg);
    background: var(--warn);
    border-radius: 999px;
    padding: 0 6px;
  }
  .pad { padding: 0 12px; }

  /* Narrow screens: the vertical column folds into a horizontal band at the top (see the .shell media query in app.css, which turns the second row
     into auto). A 208px sidebar below 900px would squeeze mapping-editor until it is unreadable. */
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
