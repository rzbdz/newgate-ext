<script lang="ts">
  // The UI shell: fetch the snapshot, render cards by Kind, **accumulate** changes, and only land on disk on save.
  //
  // Why "accumulate + one save" rather than "send once per field change": each concept is backed by one
  // file, and writing a file is one CAS (compare the baseline -> atomic write). Field-by-field writing would split one edit into
  // several submissions that cannot see each other, and if any one of them collides with someone else's change, the file stops halfway. Accumulating
  // and writing once means there is only one comparison between the frontend's baseline and the file.
  //
  // # Layout: directory on the left / content on the right / a splitter in the middle
  //
  // The first version laid all cards out as one waterfall by source (measured 4303px ≈ 4.8 screens), so "change one
  // switch", a four-step job, started with scrolling three screens. Now: **sections are a directory on the left** (click
  // to switch section), **multiple cards in one section go through tabs**, **the controls and original text of the same file sit side by side**. The acceptance bar is
  // "anything reachable within 4-5 operations", and the operation count is written next to each action below.
  import {
    apply,
    preview,
    runConceptAction,
    runRowAction,
    runSectionAction,
    snapshot,
    type Concept,
    type ConceptAction,
    type Conflict,
    type Section,
    type RowAction,
    type SectionAction,
    type Themes,
    themes,
    setTheme,
  } from "./api";
  import { setLang, t } from "./i18n";
  import { applyTheme } from "./theme";
  import {
    emptyRoute,
    fileOf,
    homeRoute,
    parseHash,
    writeHash,
    type Action,
    type Route,
  } from "./nav";
  import ConflictDialog from "./ConflictDialog.svelte";
  import Shortcuts from "./Shortcuts.svelte";
  import Sidebar from "./Sidebar.svelte";
  import SplitView from "./SplitView.svelte";
  import TabStrip from "./TabStrip.svelte";
  import ThemePicker from "./ThemePicker.svelte";
  import PluginSlot from "./PluginSlot.svelte";

  let concepts = $state<Concept[]>([]);
  let sections = $state<Section[]>([]);
  /** The **accumulated** changes for each concept. Empty = nothing unsaved. */
  let drafts = $state<Record<string, unknown>>({});
  let conflicts = $state<Conflict[]>([]);
  let error = $state("");
  let busy = $state(false);
  let filter = $state("");
  /**
   * Auto refresh is **always on** (the checkbox was removed on 2026-09-29).
   *
   * The user's own words were "the auto-refresh feature is pretty lame too, let's not have it, just auto refresh by default",
   * and "keep rarely used control buttons to a minimum". That box was a switch that could only be turned off and never used: this screen is for
   * **leaving up and watching** (probe latencies, config changed from the command line must all land here), and with it off
   * the only remedy was to press "reload" by hand, and that button was removed too - two controls serving the same thing.
   *
   * The two cadences are in the effect below: the parts that change by themselves every 3 seconds, the first-screen section every 15 seconds (it is expensive).
   */
  const auto = true;
  let note = $state("");
  let filterBox = $state<HTMLInputElement | undefined>(undefined);
  /**
   * lang is the language **resolved by the backend** (carried in the snapshot). Why it is stored separately here is explained by
   * the `{#key}` outside the template: the language `t()` reads lives in a module-level variable in i18n.ts, and that
   * is not Svelte reactive state - so switching language must land by **re-rendering the whole tree**.
   */
  let lang = $state("");

  /**
   * themeDoc is the theme table loaded at this moment (see Themes in api.ts and the backend
   * modules/web-dashboard/api.go）。
   *
   * It **does not go into the snapshot**: themes are the UI's own preference, not any module's contribution (who contributes
   * "what colors this screen uses"?). So it goes through its own endpoint, and **a read failure is not an error** - a
   * backend that cannot read the theme table (an old version) must not make the whole UI fail to open: an empty table means "only the factory theme",
   * and the UI renders as usual.
   */
  let themeDoc = $state<Themes>({ active: "", themes: [] });

  /**
   * Which section, which card, side by side or collapsed, is being viewed now.
   *
   * It lives in App rather than some child component for a hard reason: the `{#key lang}` outside
   * **rebuilds the whole subtree** when the language changes, and everything stored in child components is lost. App's own state survives
   * (`drafts` has survived to this day thanks to this).
   */
  let route = $state<Route>({ ...emptyRoute });

  const dirty = $derived(Object.keys(drafts));

  /** Filtering is **cross-section**: type in one place, it applies everywhere (when you forget which section a card is in, this is the fastest route). */
  function matches(c: Concept): boolean {
    if (!filter) return true;
    const hay = (c.id + " " + c.title + " " + c.source + " " + c.kind).toLowerCase();
    return hay.includes(filter.toLowerCase());
  }

  /**
   * A nav unit = **one** card per file. The same file often has two halves: the control half (mapping-editor /
   * toggles / records) and the original-text half (code) - those are SplitView's left and right columns. Listing the original-text half
   * (`config.file.*`) as its own tab too would make the config section pile up a screenful of `config.file.*.json`
   * / `*.kv`, looking like the same thing appears twice and taking up a large part of the nav - it is not a config that belongs to the user
   * on its own, it is the **side-by-side right column** of that config, and appears only when the control is opened and split is open.
   *
   * So: when this file has a "non-code control card", list only the control card and hide the original-text half (open split and it
   * appears on its own); when this file has only one card (directory card, table, log ...), list it as usual. Unknown modules work the same way
   * - this rule imports no module name, the criterion is just "same relative path + is the kind code".
   */
  /**
   * **The first-screen section** (the section that declared a landing spot, see Section.default in api.ts).
   *
   * Home mode draws exactly it, and only draws its **first card**: that section has only one today (`home.chains`
   * was deleted on 2026-09-29), and even if a few more come back later, home mode should not have a tab strip - it is the "see it all on one screen"
   * mode. To see anything else, go to the full UI.
   */
  const homeSection = $derived(sections.find((s) => s.default) ?? sections[0]);
  const homeConcept = $derived(
    concepts.filter((c) => c.source === homeSection?.source)[0],
  );

  const navUnits = $derived.by(() => {
    const byFile = new Map<string, Concept[]>();
    for (const c of concepts) {
      const f = fileOf(c);
      const key = f ? c.source + "|" + f : null;
      if (key === null) continue;
      const a = byFile.get(key) ?? [];
      a.push(c);
      byFile.set(key, a);
    }
    const out: Concept[] = [];
    for (const c of concepts) {
      const f = fileOf(c);
      if (!f) {
        out.push(c);
        continue;
      }
      const group = byFile.get(c.source + "|" + f)!;
      const control = group.find((g) => g.kind !== "code");
      // When there is a control card, the original text is just its right column and takes no nav slot.
      if (control && c.kind === "code") continue;
      out.push(c);
    }
    return out;
  });

  /** Sidebar badge: hit count + unsaved count. While filtering it shows the hit count - otherwise searching into a section with 3 cards
   *  would still show 12 on the badge, looking like the search did not work. Count with navUnits (the original-text half is not counted as a separate card). */
  const counts = $derived.by(() => {
    const m = new Map<string, { total: number; dirty: number; locked: number }>();
    for (const s of sections) m.set(s.source, { total: 0, dirty: 0, locked: 0 });
    for (const c of navUnits) {
      const e = m.get(c.source);
      if (!e || !matches(c)) continue;
      e.total++;
      if (drafts[c.id] !== undefined) e.dirty++;
      if (c.locked) e.locked++;
    }
    return m;
  });

  /** The cards of the current section (after filtering). The order follows the backend ((Source, ID) ordering). */
  const sectionCards = $derived(navUnits.filter((c) => c.source === route.section && matches(c)));

  /**
   * When a section has too many cards for the horizontal tab strip to scroll, switch to the **second vertical column on the left** (see TabStrip).
   *
   * The threshold is taken at the lower bound: above it, list vertically - config has 38 cards, and a horizontal strip needs several screens
   * to scan through, while a vertical column is one glance. Sections below it (plugin-manager has two, gateway one)
   * are more compact as a horizontal strip. This name is a magic number, so this sentence is here to explain it; changing it needs no other change.
   */
  const TAB_OVERFLOW = 8;
  const verticalTabs = $derived(sectionCards.length > TAB_OVERFLOW);

  /** The current card. When route.card is empty (or lands on an id that no longer exists), take the first one -
   *  "the section's first card" is the section's default view, and the keyboard and the URL both depend on it being deterministic. */
  /**
   * Which card is being drawn right now.
   *
   * In home mode it is **the first card of the first-screen section**, and it does not go through `sectionCards` - that one has to pass
   * `matches()` (the top-bar filter), and home mode has no filter; it also does not look at `route.card`
   * (that section has only one card, see homeConcept).
   */
  const active = $derived(
    route.home
      ? homeConcept
      : (sectionCards.find((c) => c.id === route.card) ?? sectionCards[0]),
  );

  /**
   * The other half of the **same file** as the current card (see fileOf in nav.ts).
   *
   * It looks the same no matter which half you come in from: if you come in on the original text (code), it is paired with the control half,
   * so side by side is always "controls on the left, original text on the right". If there is no pair, it is a single column - no error, no empty column.
   */
  const pair = $derived.by(() => {
    if (!active) return undefined;
    const f = fileOf(active);
    if (!f) return undefined;
    return concepts.find(
      (x) =>
        x.id !== active.id &&
        x.source === active.source &&
        fileOf(x) === f &&
        (active.kind === "code" ? x.kind !== "code" : x.kind === "code"),
    );
  });

  /** The current section: its name and the actions it injects (see SectionAction in api.ts). */
  /**
   * The column at this moment (in the full UI it is pointed at by route.section; in home mode it is the landing section).
   *
   * **In home mode it must fall back to the landing section**: at that point route.section is empty, so `sections.find`
   * finds nothing - and then the **addresses** of the actions on that column (`probe everything` / `auto fallback`)
   * are lost too. The buttons are still drawn (they read homeSection directly), but clicking sends with an empty source, and the backend replies
   * "no action named fallback" - an error that only explodes on click.
   */
  const section = $derived(
    route.home ? homeSection : sections.find((s) => s.source === route.section),
  );
  const sectionActions = $derived(section?.actions ?? []);

  /** When side by side, which half is on the left: the control half. */
  const leftCard = $derived(active?.kind === "code" && pair ? pair : active);
  const rightCard = $derived(active?.kind === "code" && pair ? active : pair);

  /** A concept's baseline (content hash). A concept's data defines its own shape, and the baseline lives inside it. */
  function baseOf(c: Concept): string {
    const b = (c.data as { base?: unknown } | null)?.base;
    return typeof b === "string" ? b : "";
  }

  /** Only the parts that **change by themselves** deserve a refresh every few seconds. What counts as "changing" is declared by the contributor
      (`Concept.Live`); the UI no longer guesses by Kind: that guess turned "is reading expensive" -
      something only the contributor knows - into a by-product of the render shape, and left no room for other Kinds
      (a table showing "how long until auto-close" would stay stuck on that number as long as the page is left open).
      The config one does not declare it: it has to re-read and re-parse every profile and every source file,
      and making "refresh a counter" pay that bill is spending money where nobody is looking (see snapshot in api.ts). */
  const liveSources = $derived([
    ...new Set(concepts.filter((c) => c.live).map((c) => c.source)),
  ]);

  /**
   * The source of **the first-screen section** (the section that declared a landing spot, see Section.Default in lib/view).
   *
   * It is listed separately because it refreshes on **another cadence**: that screen's output must re-read and re-parse every
   * profile (see Concept.Live in core/lib/view - the criterion for declaring it is "is reading expensive", and the config
   * one therefore does not declare Live). Following the counter's 3-second path would turn "glance at the request count" into
   * "parse all config every 3 seconds", and while this screen sits open and idle, that money buys no new information.
   *
   * But it **does change**, and it is this UI's own actions that change it: probe latencies land in the health table,
   * and the color of every stop on the first screen reads that table. So it must have its own cadence, and cannot rely only on "the user manually clicking
   * reload" - that screen is meant to be left up and watched.
   */
  const homeSources = $derived(sections.filter((s) => s.default).map((s) => s.source));

  /**
   * Sort a column of concepts into **the backend's order**: `(source, order, id)`.
   *
   * Only used after a partial refresh (`load(sources)`) merges: at that point we hold "the old untouched ones + the ones just received"
   * stitched together, and the backend's sorted array order is lost in that step, so it must be sorted again here.
   *
   * The three keys align word for word with `lib/view`'s Snapshot - **the same order must have the same criterion**.
   * None may be missing: sorting only by `(source, id)` (which is what it used to do) sends "global settings" and "upstream",
   * whose `Order` is 0/1, below a dozen tier cards (Order is 10) after every silent refresh,
   * and those two happen to be **the two you configure right after install**; the symptom the user sees is "they keep moving
   * down by themselves". A missing `order` defaults to 0, the same as the core (contributors that do not declare Order sort first).
   */
  function bySourceId(list: Concept[]): Concept[] {
    // Use `<` rather than localeCompare: the backend sorts by **Go's byte order** (`Source < Source`),
    // and localeCompare is locale-sensitive - ICU collation treats `-` as a variable character, so
    // the relative position of `plugin-manager` and `pluginmanager` may differ between the two sides. Module names are machine
    // markers, and comparing by bytes is the same criterion; the two criteria agreeing is exactly why this function exists.
    const cmp = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0);
    return [...list].sort(
      (a, b) => cmp(a.source, b.source) || (a.order ?? 0) - (b.order ?? 0) || cmp(a.id, b.id),
    );
  }

  /**
   * localTime displays the timestamp the backend gives in **the timezone of the person viewing the page**.
   *
   * The backend gives RFC3339 with `Z` (UTC). Shown as is, someone at +08:00 locally reads
   * "10:34" while the wall clock says 18:34 - a display that makes people wonder if the data is stale, and the whole point of this line
   * is to answer "how fresh is this snapshot". Timezone conversion belongs to the render layer: the backend only knows
   * its own timezone, and the viewer may be elsewhere.
   *
   * If it is not today, include the date too: the page can sit open and idle (with auto refresh off), and showing only a
   * clock time reads as "just now".
   *
   * If it cannot be parsed, return it as is - showing a string you cannot read is better than showing `Invalid Date`.
   *
   * **The format follows the UI language, not the browser**: tag is the language the backend resolved (the same
   * one as the text around it). The difference is visible - with a browser at en-US and the UI in Chinese, passing no tag
   * renders `6:35:10 PM`, which looks out of place after "data time".
   */
  function localTime(iso: string, tag?: string): string {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return d.toDateString() === new Date().toDateString()
      ? d.toLocaleTimeString(tag || undefined)
      : d.toLocaleString(tag || undefined);
  }

  /**
   * Default location: **the section that declared a landing spot** (see Section.Default in lib/view).
   *
   * Before this it was "the first section with cards", and section order follows Source alphabetically - so where the first screen
   * lands depended purely on whose name sorted first (today it is `breaker`). To make the "everything on one screen" section
   * appear first, the only way was to name its source `aaa-home` or the like to jump the queue.
   *
   * Two fallbacks, both of which really happen:
   *   - no section declares a landing spot (an assembly without that module, and the core's own test charts) - go back to the old
   *     rule, the first one with cards. **Byte for byte the same as before the change**, which is the safety rope for this change;
   *   - a landing spot is declared, but that section has no card at this moment (for example its only card appears only when some client is not
   *     installed) - entering an empty section only shows "there is nothing here", so give way to the first one with cards.
   *     "Landing spot" is a preference, not a mandate: better to land elsewhere than to throw the user into an empty page.
   */
  function defaultRoute(): Route {
    const withCards = sections.filter((s) => concepts.some((c) => c.source === s.source));
    const src =
      (withCards.find((s) => s.default) ?? withCards[0] ?? sections.find((s) => s.default) ?? sections[0])
        ?.source ?? "";
    return { home: false, section: src, card: "", split: route.split };
  }

  /**
   * Correct the position to a place that **really exists**, and write it back to the address bar.
   *
   * All three kinds of invalidation must be caught, and all three are routine, not failures: a module is turned off (that section is gone), a profile
   * is deleted (that card is gone), a hand-typed/truncated link. After falling back, **rewrite the hash** - the URL should not say
   * something that is not on screen (refresh and it jumps back, which is the confusing part).
   */
  function resolveRoute() {
    // **Home mode does not go through this correction**: its `section` is empty to begin with (that screen is decided by the landing
    // section, see homeSection), and this correction would judge "empty section" as "that section is gone", casually
    // pick a landing spot and write it as the full UI - so home mode is displaced by itself the moment it opens. Measured, hit this.
    if (route.home) {
      writeHash(route);
      return;
    }
    if (sections.some((s) => s.source === route.section)) {
      if (route.card && !concepts.some((c) => c.id === route.card)) {
        route = { ...route, card: "" };
      }
    } else {
      route = defaultRoute();
    }
    writeHash(route);
  }

  async function load(sources?: string[], quiet = false) {
    if (!quiet) busy = true;
    error = "";
    try {
      const doc = await snapshot(sources);
      // The language follows the backend (set on every snapshot: after the user runs `newgate lang zh-Hans` on the command line,
      // refreshing the page should show the change). Concept titles are translated by the backend; this step only handles the UI skeleton.
      setLang(doc.lang);
      // Assigning to $state is what makes the `{#key}` below swap the whole tree (it does not swap when the value is the same).
      lang = doc.lang;
      // <html lang> must follow too: it does not take part in rendering, but screen readers pick pronunciation by it, and browsers pick
      // line breaking and spell checking by it - writing en while the UI is Chinese is telling assistive technology the wrong thing.
      document.documentElement.lang = doc.lang;
      // The section table **changes every time** (both kinds of refresh carry it): modules can be turned off, and "is this section still
      // here" is exactly what a refresh should track best.
      sections = doc.sections;
      if (sources) {
        const byId = new Map(concepts.map((c) => [c.id, c]));
        // Cards with drafts are not swapped: that may be something unexpected beyond read-only concepts (reads and writes colliding on the same
        // card), and having what the user is editing displaced by a background refresh is the most unforgivable kind of loss.
        for (const c of doc.concepts) if (drafts[c.id] === undefined) byId.set(c.id, c);
        concepts = bySourceId([...byId.values()]);
      } else {
        concepts = doc.concepts;
        // After a full re-read, concepts that no longer exist on disk (module turned off) have nowhere to go,
        // and their drafts should go too - keeping one only makes "save" fire at an id that no longer exists.
        const alive = new Set(doc.concepts.map((c) => c.id));
        for (const id of Object.keys(drafts)) if (!alive.has(id)) delete drafts[id];
        drafts = { ...drafts };
        conflicts = [];
        // After a full re-read, both halves have been re-read from disk - "the other half's draft got displaced" is
        // already past (whoever needed to see it has seen it), and keeping that sentence only turns it into a notice that can never be erased
        // (it describes a situation that no longer exists). Same for previews: the content on disk is already
        // what it looks like "after the draft takes effect", and overlaying the draft on it again shows something that does not exist.
        dropped = "";
        previews = {};
      }
      resolveRoute();
      note = localTime(doc.generated_at, doc.lang);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      if (!quiet) busy = false;
    }
  }

  /**
   * Reload = **throw away everything in hand and read a fresh one from the BFF**.
   *
   * Drafts (unsaved changes) are thrown away too - that is exactly what "reload" means: everything on screen returns
   * to what is on disk right now. Before, reload only swapped concepts and kept drafts, so "I clicked reload and the UI
   * still looks like my edits" - that feels like reload did not work, when in fact we put the user's changes back on top.
   *
   * Saved ones were already removed from drafts (see saveAll), so what is dropped here is **only what was not saved**,
   * and dropping them is exactly what the user asked for by pressing this button.
   */
  function reloadAll() {
    drafts = {};
    conflicts = [];
    void load();
  }

  /**
   * The two halves of one file: **which half was changed last** (`"ui"` control / `"raw"` original text).
   *
   * Why it must exist: the control half and the original-text half are two concepts, two drafts, two baselines, and they write
   * **the same file**. After editing the original text, the control side still holds "the disk content from before the edit" - saving both
   * together means the one written later necessarily hits a stale baseline (reporting "this file was changed by someone else after the page loaded"),
   * and what the user sees is "no matter how I change it, it will not save".
   *
   * So: whoever changed last has the final say. The other half's draft is discarded the moment **this side** changes - it was rendered from
   * the disk content from before the change, and keeping it only leads into a conflict. The full re-read after saving (see
   * saveAll) is the "sync the other half right after editing" step: both halves are re-read from disk.
   */
  let lastEdit = $state<Record<string, "ui" | "raw">>({});

  /**
   * dropped is "which file's other-half draft was just dropped" - a sentence for the user to read, not an error.
   *
   * Why it must exist: the other half's draft was displaced by **this half's** edit (see edit), and it was text the user
   * had just typed. Dropping it silently violates this repo's hard rule (no silence), and they would most likely
   * think those words are still there - by the time they come back to look, the screen already shows the old content from disk.
   */
  let dropped = $state("");

  /**
   * previews is "what the **control** half should show when the original-text half's draft looks like this" - stored by the control
   * card's id (see preview in api.ts, Concept.Preview in the core lib/view).
   *
   * Why it must exist: both halves of a file can be edited, and the control half's edit payload is **the whole file**
   * (an entire tier table is submitted, not that one cell). So if "paste a whole file in the original text, then change one dropdown"
   * did not ask this, what gets submitted is the old table **from before the change** - what was just pasted vanishes on the spot, and
   * the screen never showed it from start to finish, so the user does not feel they are overwriting anything.
   *
   * Its lifecycle follows **the original-text draft**: draft present, preview present; draft displaced/saved/reverted,
   * preview disappears with it (otherwise the control half would sit on content that does not exist on disk).
   */
  let previews = $state<Record<string, unknown>>({});

  /**
   * Jump to the card that **edits this file**.
   *
   * This jump works because "file identity" is already in the frontend (`fileOf`, see nav.ts);
   * and the rule for "which card is its editor" also exists in only one place (controlOf, a few lines below) - writing it in two places
   * would mean jumping from the first screen may land on **the original-text half**, and those two halves are two views
   * of the same file; landing on the wrong half makes the user click once more.
   *
   * When no target is found, **do nothing**: that means this file has no editable card right now (for example the module contributing it
   * was turned off). Jumping to a nonexistent address is worse than not jumping - it loses the current screen too.
   */
  function openFile(f: string) {
    const target = controlOf(f) ?? concepts.find((c) => fileOf(c) === f);
    if (!target) return;
    route = { home: false, section: target.source, card: target.id, split: route.split };
    writeHash(route);
  }

  /** The "control half" of this file: the card that is not code and points at the same file. */
  function controlOf(f: string): Concept | undefined {
    return concepts.find((c) => c.kind !== "code" && fileOf(c) === f);
  }

  /** The original-text half's current draft text (undefined when there is no draft). */
  function rawDraftOf(f: string): string | undefined {
    const raw = concepts.find((c) => c.kind === "code" && fileOf(c) === f);
    const d = raw ? (drafts[raw.id] as { text?: unknown } | undefined) : undefined;
    return typeof d?.text === "string" ? d.text : undefined;
  }

  function dropPreview(f: string) {
    const ctl = controlOf(f);
    if (ctl && previews[ctl.id] !== undefined) {
      delete previews[ctl.id];
      previews = { ...previews };
    }
  }

  /**
   * The original text changed -> ask what the control half should look like now.
   *
   * **200ms debounce**: CodeMirror fires onEdit on every character typed, and firing a request on every keystroke
   * is wasted - a half-typed KV cannot be parsed anyway (the backend returns an error then, and the UI keeps the previous
   * look, see the BFF's preview). 200ms is a rough judgment of "stopped typing": short enough to feel instant, long enough that
   * one continuous burst of input asks only once.
   *
   * If it comes back late, judge once by **content**: the draft may have been displaced or changed in the meantime, and then the answer to this question
   * belongs to the previous version, and drawing it shows a draft that does not exist.
   */
  let previewTimer: ReturnType<typeof setTimeout> | undefined;

  function schedulePreview(f: string, text: string) {
    const ctl = controlOf(f);
    if (!ctl?.previewable) return;
    clearTimeout(previewTimer);
    const id = ctl.id;
    previewTimer = setTimeout(() => {
      void (async () => {
        if (rawDraftOf(f) !== text) return;
        const res = await preview(id, text);
        if (res.error || rawDraftOf(f) !== text) return;
        previews[id] = res.data;
        previews = { ...previews };
      })();
    }, 200);
  }

  function edit(id: string, value: unknown) {
    const c = concepts.find((x) => x.id === id);
    const f = c ? fileOf(c) : undefined;
    if (c && f) {
      const side: "ui" | "raw" = c.kind === "code" ? "raw" : "ui";
      lastEdit[f] = side;
      let lost = "";
      for (const other of concepts) {
        if (other.id === id || fileOf(other) !== f) continue;
        const otherSide = other.kind === "code" ? "raw" : "ui";
        if (otherSide === side || drafts[other.id] === undefined) continue;
        lost = f;
        delete drafts[other.id];
      }
      dropped = lost
        ? t("both panes edit {file}, and only the one you touched last is saved — what was pending in the other pane has been dropped", {
            file: lost,
          })
        : "";
      // The original-text half was changed -> make the control half follow (see the comment on previews). The control half was changed
      // -> the original-text draft has just been displaced, the preview has no basis either, withdraw it too.
      if (side === "raw") {
        const text = (value as { text?: unknown } | null)?.text;
        if (typeof text === "string") schedulePreview(f, text);
      } else if (lost) {
        dropPreview(f);
      }
    }
    drafts[id] = value;
    drafts = { ...drafts };
  }

  function revert(id: string) {
    delete drafts[id];
    drafts = { ...drafts };
    // Revert = put this card back to "not changed". Besides dropping the draft, do a full re-read - so that what it shows
    // is necessarily **the value on disk right now**, not the value at the last snapshot (the command line may have changed it in the meantime).
    // Same as saveAll: the local BFF costs nothing, no chained computation.
    void load();
  }

  async function saveAll() {
    busy = true;
    error = "";
    const stillConflicting: Conflict[] = [];
    for (const id of dirty) {
      const c = concepts.find((x) => x.id === id);
      if (!c) continue;
      // The two halves of one file can have only one in charge (see the comment on lastEdit in edit): if by any chance both
      // still carry drafts (say, stuffed in from elsewhere), submit only the **later-changed** half - submitting both necessarily leaves one half
      // hitting a stale baseline, and the user sees "saving always errors". The other half's draft is dropped right there: it writes
      // old content of the same file, and keeping it only makes the same mistake again.
      const f = fileOf(c);
      if (f && lastEdit[f]) {
        const side = c.kind === "code" ? "raw" : "ui";
        if (side !== lastEdit[f]) {
          delete drafts[id];
          continue;
        }
      }
      const res = await apply(id, baseOf(c), drafts[id]);
      if (res.conflict) {
        stillConflicting.push(res.conflict);
        continue;
      }
      if (res.error) {
        // The contributor's error is shown as is and names which card it is: mixing several concepts' errors into one
        // "save failed" leaves the user not knowing which one to look at.
        error = `${c.title}: ${res.error}`;
        continue;
      }
      delete drafts[id];
    }
    drafts = { ...drafts };
    conflicts = stillConflicting;
    busy = false;
    if (!stillConflicting.length && !error) note = localTime(new Date().toISOString(), lang);
    // On success **re-read everything** (no per-source increment): saving writes a file, and any card on screen may
    // have changed because of this write - the right-column original text, other cards from the same source, even the provider list. Rather than working out which ones
    // change, just pull a fresh snapshot blindly: simple, correct, (the local BFF has no performance cost at all).
    if (!error && !stillConflicting.length) {
      void load();
      // **The theme table must be re-read too**, together with the snapshot.
      //
      // It is a separate endpoint (themes are not in the ledger, see Themes in api.ts), so `load()`
      // cannot reach it. Without this line the symptom is "switched to another theme on the theme card in the config section, and after saving the UI
      // does not change at all" - while the top-bar dropdown changes on the spot (it goes through pickTheme, which re-reads). The same
      // setting with two entry points, only one of which works, is the hardest kind of inconsistency to track down.
      void loadThemes();
    }
  }

  /**
   * Delete **the tier file that the current card represents**.
   *
   * It fires on **this card's own apply** (`{delete:true}` + the baseline from load time for CAS): one
   * file one card, and a card can delete itself - before 2026-09-20 this step detoured through another "tier file"
   * directory card, and the files that card listed correspond one to one with these cards, so it was the same thing said twice (the user asked to delete that
   * card entirely). If the baseline is wrong (someone just changed it), let the backend report a conflict rather than force the delete.
   */
  async function deleteActive() {
    if (!active) return;
    busy = true;
    error = "";
    const id = active.id;
    const res = await apply(id, baseOf(active), { delete: true });
    busy = false;
    if (res.conflict) {
      conflicts = [...conflicts, res.conflict];
      return;
    }
    if (res.error) {
      error = `${active.title}: ${res.error}`;
      return;
    }
    delete drafts[id];
    drafts = { ...drafts };
    // That card no longer exists, do not stay on it (active falls back to the section's first card).
    if (route.card === id) route = { ...route, card: "" };
    void load();
  }

  /**
   * Run an action on the current section (see runSectionAction in api.ts).
   *
   * The UI **does not know what that action does**, and should not: how to pick the name "create another tier file",
   * what shape it takes, is entirely the business of whoever owns that section. The UI does only two things - forward the click,
   * then re-read (same as the save path, pull a fresh snapshot when the work is done).
   */
  /**
   * Run an action on **a card** (see runConceptAction in api.ts).
   *
   * It is the same thing as runAction at two different landing points: that one recognizes "this section", this one recognizes "this card".
   * Both **do not know what that action does** - label is a sentence the contributor wrote, and only that one knows
   * what it changes on disk. The UI does only three things: forward the click, drop the drafts in hand (the disk changed and
   * all baselines are stale), and re-read.
   *
   * Why not go through drafts / CAS like saving: an action is **not** "write this draft down". It is
   * "do a thing" - for example "make this one the default", which changes state.json and has nothing to do with this card's
   * file. So it has no base, no edit, and should not make this card dirty.
   */
  async function runCardAction(id: string, a: ConceptAction) {
    busy = true;
    error = "";
    const res = await runConceptAction(id, a.id);
    busy = false;
    if (res.error) {
      error = res.error;
      return;
    }
    drafts = {};
    await load();
    const target = res.focus ? concepts.find((c) => c.id === res.focus) : undefined;
    if (target) {
      route = { home: false, section: target.source, card: target.id, split: route.split };
      writeHash(route);
    }
  }

  /**
   * Run an action on **a row in a table** (see runRowAction in api.ts).
   *
   * The key difference from runCardAction: **do not clear drafts**.
   *
   * The action on that card ("make this one the default") **changes the disk**, so after the re-read every draft's baseline
   * in hand is stale and must be dropped; but an action on a row ("probe this binding")
   * changes state **in the daemon's memory** - it has nothing to do with the file this card reads. Clearing them
   * would silently wipe out an edit the user has open with one "click the test button", a pure loss.
   *
   * Re-read as usual: this table is Live (tripping, latency, cooldown were all updated by this probe), and without a re-read
   * the UI would keep talking about the thing from minutes ago.
   */
  async function runRow(id: string, row: string, a: RowAction) {
    busy = true;
    error = "";
    const res = await runRowAction(id, row, a.id);
    // **Re-read whether or not it succeeded**, and before showing the error: the action on this row changes the daemon's
    // state (one probe, the conclusion recorded in the health table), and those columns - probe, failures, cooldown until when
    // - are exactly this button's result. Without a re-read on failure, the banner says "cannot connect" while the table still shows how it looked before the click,
    // two statements contradicting each other, and the user has no way to tell which is true.
    //
    // The order cannot be reversed: `load()` clears `error` at the start, so that error must be put back **last**.
    await load();
    busy = false;
    if (res.error) error = res.error;
  }

  async function runAction(a: SectionAction, source = section?.source) {
    busy = true;
    error = "";
    // **The source must be given by the caller** (the default is "the column at this moment").
    //
    // This used to say `route.section`, and in home mode it is **empty** - so clicking
    // `auto fallback` sent `{"source": "", "action": "fallback"}`, and the backend replied
    // "there is no action named fallback". The button could be drawn (it reads the landing section), yet the address was computed
    // elsewhere, and only **clicking it** shows the mismatch (measured and hit on 2026-09-30, the user's own words:
    // "the fallback button still cannot be turned off").
    const res = await runSectionAction(source ?? "", a.id);
    busy = false;
    if (res.error) {
      error = res.error;
      return;
    }
    // Drop the draft first: the action changes the disk, and after the re-read the baselines of the drafts in hand are all stale.
    drafts = {};
    await load();
    // If the action says what it produced (res.focus), switch there - the next step after "new" is always to fill it in,
    // and making the user find the just-created one among a pile of cards hides the answer to "what is it called"
    // one more time.
    //
    // The UI **does not guess** that id: if not found, stay put (that side has just re-read, and the new card is in the nav).
    const target = res.focus ? concepts.find((c) => c.id === res.focus) : undefined;
    if (target) {
      route = { home: false, section: target.source, card: target.id, split: route.split };
      writeHash(route);
    }
  }

  /** Pick "keep mine" in a conflict: replay once with the baseline of the disk copy. */
  async function keepMine(cf: Conflict) {
    const c = concepts.find((x) => x.id === cf.concept);
    if (!c) return;
    const res = await apply(cf.concept, cf.current, drafts[cf.concept]);
    if (res.error || res.conflict) {
      error = res.error ?? "conflict again — someone is writing this file right now";
      return;
    }
    delete drafts[cf.concept];
    drafts = { ...drafts };
    conflicts = conflicts.filter((x) => x !== cf);
    // Once written, re-read everything (same as saveAll: local BFF, no performance cost, no chaining).
    void load();
  }

  /** Pick "use the disk copy" in a conflict: drop my draft and re-read once. */
  function takeTheirs(cf: Conflict) {
    delete drafts[cf.concept];
    drafts = { ...drafts };
    conflicts = conflicts.filter((x) => x !== cf);
    void load();
  }

  // Navigation: all three entry points (sidebar, tab, keyboard) only change route, and writeHash lands it in the address bar.
  // **Change state in only one place**, and the URL cannot disagree with the screen.
  function pickSection(source: string) {
    // Clicking **the first-screen column** = back to home mode (the directory folds up and only the card is left).
    //
    // It pairs with the "settings" button in the top bar: that one goes from home to the full UI, this one goes from the full UI back home.
    // Without this return path, "home" in the directory would be no different from any other section - while it is a different
    // mode, not another section.
    if (source === homeSection?.source) {
      toHome();
      return;
    }
    route = { home: false, section: source, card: "", split: true };
    writeHash(route);
  }

  /** Back to home mode (not even `#/s/...` is left in the address, see routeTo in nav.ts). */
  function toHome() {
    route = { ...homeRoute };
    writeHash(route);
  }

  /**
   * Enter the **full UI** from home mode, landing on **the config file page**.
   *
   * The user's own words were "clicking settings while on home should jump to the config file page".
   *
   * The criterion is **the first section that has cards and is not the first-screen section**, with no module names hard-coded - this layer knows
   * no modules (same as the fileOf rule). Landing on "the first section" is wrong: that section is home itself.
   * If this distribution has only the first-screen section (a skeleton config), fall back to it.
   */
  function toSettings() {
    // The criterion is **the purpose the column declares itself** (`Section.fields === "settings"`, see the core
    // view.SectionInfo.Fields), and it is the first one with cards.
    //
    // Two paths were tried and both failed: "the first section with cards" lands on the breaker (it sorts earlier by source),
    // and "the first section with editable files" lands on Claude Code (it also has mappings-style cards).
    // What the user wants is **the config file page** - and "this column is settings" is a machine marker the backend already declares,
    // so it can be used to decide without introducing any module name.
    const target =
      sections.find(
        (s) =>
          s.fields === "settings" &&
          s.source !== homeSection?.source &&
          concepts.some((c) => c.source === s.source),
      ) ?? homeSection;
    if (!target) return;
    route = { home: false, section: target.source, card: "", split: true };
    writeHash(route);
  }
  function pickCard(id: string) {
    route = { ...route, card: id };
    writeHash(route);
  }

  function toggleSplit() {
    route = { ...route, split: !route.split };
    writeHash(route);
  }

  /**
   * `[` / `]`: switch cards within this section. At the end **wrap around** (rather than stopping in place): a key with no feedback
   * makes people think the shortcut did not work, and then try it a second time.
   */
  function stepCard(delta: 1 | -1) {
    if (sectionCards.length < 2 || !active) return;
    const i = sectionCards.findIndex((c) => c.id === active.id);
    const next = sectionCards[(i + delta + sectionCards.length) % sectionCards.length];
    if (next) pickCard(next.id);
  }

  function run(a: Action) {
    switch (a.kind) {
      case "save":
        // Accept it when there is no draft too (clear the previous error), but do not send a request.
        if (dirty.length && !busy) void saveAll();
        break;
      case "focus-filter":
        filterBox?.focus();
        filterBox?.select();
        break;
      case "card":
        stepCard(a.delta);
        break;
      case "section": {
        const s = sections[a.index];
        if (s) pickSection(s.source);
        break;
      }
      case "blur":
        break;
    }
  }

  /** Back/forward (and a manual hash change): this is the **only** place that reads back from the URL. */
  function onHashChange() {
    route = parseHash(location.hash);
  }

  // While filtering, if the current section has no hits, jump to **the first hit** (the last step of the cross-section
  // search path). Once written into route, the next round of `inSection` holds, so it does not jump back and forth.
  $effect(() => {
    if (!filter || !concepts.length) return;
    if (concepts.some((c) => c.source === route.section && matches(c))) return;
    const first = concepts.find(matches);
    if (!first) return;
    route = { home: false, section: first.source, card: first.id, split: route.split };
    writeHash(route);
  });

  $effect(() => {
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  });

  /**
   * When there are unsaved changes, refreshing/closing the tab should ask first.
   *
   * Drafts live in page memory (deliberately: one edit accumulates into one CAS write), so F5 means throwing them away
   * - and "pressed refresh by mistake" and "just wanted to see the latest state" look exactly alike. This browser prompt
   * is the only place that can stop it (the UI itself cannot: the refresh is not initiated by our code).
   *
   * The wording is decided by the browser (modern browsers all show their own sentence), so here we only preventDefault.
   * The cost is that this listens **follows the drafts**: no listener when there is no draft, so even a normal refresh gets no dialog.
   */
  $effect(() => {
    if (!dirty.length) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  });

  /**
   * Read the theme table once.
   *
   * **Failure to read is not an error**: an old backend has no such endpoint (404), and a UI that cannot read themes
   * should still open - an empty table means "only the factory token set". Popping up red text for it would turn "this backend
   * is missing a decorative feature" into "this UI is broken".
   */
  async function loadThemes() {
    try {
      themeDoc = await themes();
    } catch {
      themeDoc = { active: "", themes: [] };
    }
  }

  /** Switch to another theme. Empty id = back to the factory token set (see applyTheme in theme.ts). */
  async function pickTheme(id: string) {
    try {
      await setTheme(id);
      // Re-read after switching rather than changing the local copy in place: **which one got saved** is known only to the backend
      // (it may reject, or another tab may have just changed it), and guessing locally would disagree with disk.
      await loadThemes();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  /**
   * Themes land on the DOM (see theme.ts). Put it in an effect rather than pickTheme: this way
   * **one place** - both the one read at first screen and the one re-read after switching go the same path.
   */
  $effect(() => {
    applyTheme(themeDoc);
  });

  route = parseHash(location.hash);
  load();
  void loadThemes();

  // Auto refresh uses **two cadences**.
  //
  //   - the parts that change by themselves (counters, logs) are asked every 3 seconds - they are cheap and change fast.
  //   - **the first-screen section is asked every 15 seconds**: its output must re-read and re-parse every profile (expensive),
  //     but probe results, and config changed on the command line, all land on it, and this screen is exactly what is meant
  //     to be left up and watched. A full re-read (refreshing the first screen every 3 seconds too) would turn "glance at the request count" into "every 3 seconds
  //     parse all config", and the config files do not change by themselves - that cost buys no new information.
  //
  // Each computes its own source, and where they overlap (a section is both Live and the first screen) only the fast one runs:
  // asking for the same data twice, the extra time is of no use at all.
  // **The sources are read inside these two callbacks, not on the effect** - this is not a style question, it is the dividing line between
  // this working and not working: an effect tracks every piece of state read during its synchronous run, and both of those sources are
  // **new arrays** derived from `sections` / `concepts`. So every refresh (including those these two timers
  // start themselves) makes the effect re-run and clears and rebuilds both timers - and the 15-second one never
  // reaches second 15, it is reset every 3 seconds.
  //
  // Measured: before the fix, the first-screen section was refreshed **0** times in 17 seconds, while the 3-second one looked fine
  // (it is shorter than the reset interval, so it occasionally completes) - both were wrong, only the symptom differed.
  $effect(() => {
    if (!auto) return;
    const fast = setInterval(() => {
      const src = liveSources;
      if (src.length) void load(src, true);
    }, 3000);
    const slow = setInterval(() => {
      const live = liveSources;
      const home = homeSources.filter((s) => !live.includes(s));
      if (home.length) void load(home, true);
    }, 15000);
    return () => {
      clearInterval(fast);
      clearInterval(slow);
    };
  });
</script>

<!-- Switching language must **re-render the whole tree**.
     The language t() looks up lives in a module-level variable in i18n.ts, not in Svelte reactive state, so
     strings with no reactive dependency (buttons, placeholders) render only in the language of **the moment of first render**
     once, and never update afterwards; while those that depend on state (concept counts, timestamps) re-render and get the new language.
     The result is two languages on one screen (measured: with the backend on zh-Hans, "44 concepts" was in Chinese while
     "save" was in English).

     {#key} rebuilds the subtree when lang changes, and every t() is evaluated again. It only happens when the language **really changed**
     happens - normally it is right when the first snapshot arrives after startup, when the page has no state worth
     keeping yet (no drafts, no open editor, and route lives on App itself). -->
{#key lang}
<div class="shell" class:home={route.home}>
  <header class="top">
    <!-- The settings button is at the **far left**, left of the search box, and appears only in home mode (the user's own words:
         "put the settings button on the left of the page, left of the search bar. Only show it on home", and "do not use text
         for settings and home, use icons"). Why it exists in home mode and not in the full UI: the full UI's directory
         has a "home" entry to click back to, while home mode has the directory folded up - the two buttons each fill the
         other's missing path, with no redundant pair. -->
    {#if route.home}
      <button class="icon" title={t("settings")} aria-label={t("settings")} onclick={toSettings}>
        <!-- Gear. **The previous version drew it wrong**: a circle plus eight short rays reads as a sun (the user's own words:
             "why is your settings icon a sun, shouldn't it be a gear"). The teeth must be **blocks growing out of the
             wheel body**, not lines floating outside - so here we use a single outline path (Feather's, MIT),
             not a circle + stroke. -->
        <svg class="gear" viewBox="0 0 24 24" aria-hidden="true">
          <circle cx="12" cy="12" r="3.1" />
          <path
            d="M19.5 14.6a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 1 1-2.9 2.9l-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.5v.2a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.9.3l-.1.1a2 2 0 1 1-2.9-2.9l.1-.1a1.7 1.7 0 0 0 .3-1.9 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.9l-.1-.1a2 2 0 1 1 2.9-2.9l.1.1a1.7 1.7 0 0 0 1.9.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.9-.3l.1-.1a2 2 0 1 1 2.9 2.9l-.1.1a1.7 1.7 0 0 0-.3 1.9V9a1.7 1.7 0 0 0 1.5 1h.2a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"
          />
        </svg>
      </button>
    {/if}

    <strong>newgate</strong>
    {#if !route.home}
      <!-- The filter box is only in the full UI: home mode's screen has **all** the cards (it is the see-it-all-on-one-screen
           screen), and putting a filter on a grid of a dozen cards is one more rarely used control. -->
      <input
        class="filter"
        bind:this={filterBox}
        placeholder={t("filter — id, title, kind")}
        bind:value={filter}
      />
    {/if}
    <span class="spacer"></span>

    {#if route.home}
      <!-- Home mode has just these few: the `auto fallback` status button, themes, save (on demand).
           No reload (auto refresh is always running), no filter, no auto-refresh checkbox, no "probe
           everything" - the user's own words were "keep rarely used control buttons to a minimum" and "that probe-everything
           button on home is dumb too, just remove it". -->
      {#each homeSection?.actions ?? [] as a (a.id)}
        {#if a.id === "fallback"}
          <button
            class="tiny ghost"
            class:toned={!!a.tone}
            data-tone={a.tone}
            disabled={busy}
            onclick={() => runAction(a)}
          >
            {a.label}
          </button>
        {/if}
      {/each}
    {:else}
      {#if note}<span class="dim mono">{t("as of {time}", { time: note })}</span>{/if}
      <button onclick={reloadAll} disabled={busy}>{t("reload")}</button>
    {/if}

    <!-- The mount point for module-provided frontends (see PluginSlot.svelte): this knows no plugin. -->
    <PluginSlot at="topbar" {lang} />
    {#if themeDoc.themes.length}
      <ThemePicker doc={themeDoc} onPick={pickTheme} />
    {/if}
    <!-- Save **appears on demand** (drawn only when something is unsaved). The user's own words were "only save is dynamic
         -appearing" - a save button permanently pinned there is just a gray block taking up the top bar when there are no changes. -->
    {#if dirty.length}
      <button class="primary" onclick={saveAll} disabled={busy}>
        {t("save")} ({dirty.length})
      </button>
    {/if}
    {#if !route.home}
      <!-- Back to home mode. Also an **icon** (symmetric with the settings button). -->
      <button class="icon" title={t("home mode")} aria-label={t("home mode")} onclick={toHome}>
        <svg viewBox="0 0 16 16" aria-hidden="true">
          <path d="M2.6 7.4 8 3l5.4 4.4V13a.6.6 0 0 1-.6.6h-3v-3.4h-3.6v3.4h-3A.6.6 0 0 1 2.6 13z" />
        </svg>
      </button>
    {/if}
  </header>

  {#if route.home}
    <!-- Home mode: **the directory is not drawn**. This screen is the "use it most of the time" screen, and the directory is
         a permanently present nav column - to see anything else, the button in the top right goes to the full UI. -->
  {:else}
    <Sidebar {sections} active={route.section} {counts} onPick={pickSection} />
  {/if}

  <section class="content" class:home={route.home} class:subcol={!route.home && verticalTabs}>
    <div class="errs">
      {#if error}
        <div class="banner">{error}</div>
      {/if}
      {#if dropped}
        <div class="notice">{dropped}</div>
      {/if}
      {#each conflicts as cf (cf.concept + cf.current)}
        <!-- It is itself a .banner.conflict block (no wrapper: two layers of border look like two things). -->
        <ConflictDialog
          conflict={cf}
          onKeepMine={() => keepMine(cf)}
          onTakeTheirs={() => takeTheirs(cf)}
        />
      {/each}
    </div>

    <!-- This section's toolbar: the actions it injects itself (like "create another tier file").
         Actions live on the **section** rather than on a card - a newly created one has no concept yet, and no
         card can host this button; on the section it is always reachable, no matter which card you are viewing.
         With no actions the whole bar is not drawn (an empty bar only pushes content down). -->
    {#if !route.home && sectionActions.length}
      <div class="secbar">
        <span class="name">{section?.title}</span>
        <span class="spacer"></span>
        {#each sectionActions as a (a.id)}
          <button
          class="tiny ghost"
          class:toned={!!a.tone}
          data-tone={a.tone}
          disabled={busy}
          onclick={() => runAction(a)}
        >
          {a.label}
        </button>
        {/each}
      </div>
    {/if}

    <!-- Wrap in a .nav-slot: TabStrip is a component, and App's scoped styles cannot give its root element a grid
         position; marking it on the wrapper is clearer (see .content.subcol in app.css). -->
    {#if !route.home}
    <div class="nav-slot">
      <TabStrip
        cards={sectionCards}
        active={active?.id ?? ""}
        {drafts}
        vertical={verticalTabs}
        onPick={pickCard}
      />
    </div>
    {/if}

    <div class="pane">
      {#if active}
        <SplitView
          left={leftCard}
          right={rightCard}
          {drafts}
          {previews}
          split={route.split}
          onEdit={edit}
          onRevert={revert}
          onDeleteFile={deleteActive}
          onAction={runCardAction}
          onRowAction={runRow}
          onOpenFile={openFile}
          bare={route.home}
          onToggleSplit={toggleSplit}
        />
      {:else if concepts.length}
        <p class="dim">{t("nothing in this section matches.")}</p>
      {:else}
        <p class="dim">{t("no concepts — nothing installed in this process contributes a view.")}</p>
      {/if}
    </div>
  </section>
</div>
{/key}

<!-- The keyboard listener goes **outside** `{#key}`: there is no reason to remove and reinstall the listener when the language changes. -->
<Shortcuts onAction={run} />

<style>
  .top {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 9px 16px;
    background: var(--panel);
    border-bottom: 1px solid var(--line);
    /* No longer sticky: the whole page does not scroll (see .shell in app.css), nothing needs it to stick. */
    flex-wrap: wrap;
  }
  /* Only the filter box gets a fixed width. It used to be `.top input`, so the **auto-refresh checkbox** was stretched to
     220px too, pushing its label hundreds of pixels away - two things that should sit together looked unrelated. */
  .top .filter { width: 220px; }
</style>
