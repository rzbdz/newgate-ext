<script lang="ts">
  // First screen: **which client uses which tier** (shape in the core lib/view's Overview).
  //
  // # Division of labor between this screen and the chains screen
  //
  // It sorts before Chains in the same section. That one is "read one by one" - the full chain of each tier, who got skipped and
  // why; this one is "use it to decide": a client tab at the top, one card per profile below,
  // and on the card are **the steps that would be taken right now** plus the health of each stop (latency, color, last probe conclusion).
  //
  // # Three conventions, same origin as the chains side
  //
  //  1. **Knows no modules**. `agents` is a machine marker + display name from the backend, `cards` is each
  //     profile's chain conclusion. This screen imports nothing from config/breaker.
  //  2. **Color is semantics, not a value**. The color of the latency cell is given by the backend (`latency_tone`) -
  //     how many milliseconds counts as slow is breaker's rule, implemented in one place. Hard-coding a threshold on this screen means on the day the threshold changes
  //     there are two places to change, and the one that is missed turns nothing red.
  //  3. **The `use:` prefix is a contract**. Card actions whose id starts with `use:` are "use this one",
  //     and the part after it is the agent's machine marker; the rest (today just probing) is drawn as a button. This concatenation is
  //     hard-coded by the backend (see view.OverviewCard.Actions), and both sides must build the same string.
  type Step = {
    provider: string;
    model: string;
    profile?: string;
    latency_ms?: number;
    latency_tone?: string;
    probe?: string;
    note?: string;
  };
  type Act = { id: string; label: string };
  type Row = {
    id?: string;
    tier: string;
    label?: string;
    head?: string;
    steps?: Step[];
    note?: string;
    tone?: string;
  };
  type Card = {
    profile: string;
    /**
     * Whether this is **the "default" card** (see the core view.OverviewCard.Auto).
     *
     * It looks almost the same as a profile card, but says two different things: clicking "use for claude" on this one
     * makes claude **follow the global default**; clicking the same sentence on the ds card pins claude **to
     * ds**. The two resolve identically right now, and after the global is changed one follows while the other does not - so they must
     * be two cards (in Clash a group can both pick a node directly and reference another group).
     */
    auto?: boolean;
    file?: string;
    default?: boolean;
    roles: Row[];
    actions?: Act[];
  };
  type Agent = {
    id: string;
    name: string;
    /** The client's own icon (the `d` of an SVG path, 24 viewport; empty = not given, see the backend overview.go). */
    icon?: string;
    profile?: string;
    own?: boolean;
    ready?: boolean;
  };
  type Data = { agents: Agent[]; auto?: Card; cards: Card[] };

  import { t } from "../i18n";
  import type { ConceptAction } from "../api";

  let {
    data,
    onAction,
    onOpenFile,
  }: {
    data: Data;
    /**
     * Buttons on the card header ("use this one" / "probe the whole card"), see ConceptAction in api.ts.
     *
     * **No `onRowAction`**: rows on this screen have no actions. Probing a single binding lives in the health table
     * section (`breaker.health`, one button per row), while here it is per card and per screen - see
     * probeSet in modules/home/overview.go.
     */
    onAction?: (a: ConceptAction) => void;
    /**
     * Jump to the card that **edits this file** (the profile's card in the config section).
     *
     * A navigation trip, not an action: it changes nothing on disk, so it should not go through `onAction` (that path clears
     * drafts and re-reads everything when done - all side effects for "I just wanted to look at that file").
     */
    onOpenFile?: (file: string) => void;
  } = $props();

  const agents = $derived(data?.agents ?? []);
  const cards = $derived(data?.cards ?? []);
  /** The "default" card (see Card.auto). The backend does not report it when there is no selectable profile. */
  const auto = $derived<Card | undefined>(data?.auto);

  /**
   * Which tab is being viewed now (the agent's machine marker; empty string = the "all clients" tier).
   *
   * The default lands on **the first installed** client: when none is installed it lands on that empty tier (then it is the only
   * way to swap the chain, see useActions in overview.go).
   */
  let tab = $state<string | undefined>(undefined);
  const current = $derived<Agent | undefined>(
    agents.find((a) => a.id === tab) ?? agents.find((a) => a.ready) ?? agents[0],
  );

  /**
   * Whether this tab **follows the default** right now.
   *
   * Two different things land here: `own` false = this one has never set its own, and follows the global; while the "all clients"
   * tier **is itself the global default**, so it follows too (it has no "parent level" to follow - that default is
   * itself).
   */
  const follows = $derived(!current?.id || !current?.own);

  /**
   * Whether this card is **the one this tab is using right now**.
   *
   * Which card gets **the highlight** is exactly where "following" and "pinned" differ: when following the default, the highlight is on the
   * "default" card; when pinned, the highlight is on the pinned one. The two cards' content may be identical (the default happens to point at
   * it), and that is exactly why this rule is needed - the color says **relation**, not content.
   */
  function isActive(c: Card): boolean {
    // The "auto" tier: what is selected is **a profile card** (it defines who auto points at).
    if (!current?.id) return !c.auto && c.profile === current?.profile;
    // The client tier: when following auto it selects the "auto" card, and when pinned it selects the pinned one.
    return follows ? !!c.auto : !c.auto && c.profile === current?.profile;
  }

  /**
   * Which buttons to draw on this card.
   *
   * The `use:` family is "point this profile at some client", and only **one** should be drawn at a time: the one for the current
   * column. The one pointing it at itself is not reported by the backend at all (see useActions: it does not
   * appear for a client already using it), so "this column already uses it" is said by the statement below.
   *
   * The ID is `use:<agent>:<profile>` (see the core view.OverviewCard.Actions): **the profile
   * segment is required** - the same action for the same agent exists on a dozen cards, and the ledger looks people up by ID
   * and takes the first match. So the UI builds the one unique
   * from "the current column's agent + this card's profile" (the same rule as building row IDs on the chains card).
   *
   * The empty-id tier (no client installed, or the user clicked "all clients") draws the global one.
   */
  function actions(c: Card): Act[] {
    // Of this row of buttons, **keep only those that are not "selecting"** (today just probing).
    //
    // The "use it" family (`use:` / `auto:`) is no longer drawn as buttons: **clicking the card itself is that action**
    // (see pick). Placing a button with the same name on the card says the same thing twice - and the user's
    // own words were "that 'use this for xx' is redundant, clicking the card switches it directly".
    return (c.actions ?? []).filter((a) => !isPick(a.id));
  }

  /** This family of actions is "select it" - they are triggered by **clicking the card**, not drawn as buttons. */
  function isPick(id: string): boolean {
    return id.startsWith("use:") || id.startsWith("auto:");
  }

  /**
   * Which action should run when this card is clicked.
   *
   * None = **it is already selected** (the backend does not give the action "point this one at itself", see
   * useActions in overview.go), so nothing should happen - rather than having the UI make up a
   * result for it.
   *
   * The criterion for picking the ID is **word for word the same** as when it used to draw buttons: the meaning of that sentence on the same card is decided by the current
   * tier (`use:global:*` on the "auto" tier, `use:<agent>:*` on a client tier), so `who` is still used
   * to build it here.
   */
  function pickAction(c: Card): Act | undefined {
    const who = current?.id ? current.id : "global";
    const want = c.auto ? `auto:${who}` : `use:${who}:${c.profile}`;
    return (c.actions ?? []).find((a) => a.id === want);
  }

  /**
   * The cards currently being probed (the key is keyOf).
   *
   * Probing is an action that **takes a few seconds**, and what it returns is exactly the latency numbers on this screen - while waiting,
   * if the old numbers were still shown, the user would see "nothing happened when I clicked", and then those numbers would **suddenly** jump.
   * The user's own words: "when probing, remember to cover the current latency (show it as...). Refresh after new results".
   * So during probing those cells are covered with "...", and when the re-read comes back they naturally become the new values.
   */
  let probing = $state<Record<string, boolean>>({});

  /** Whether this card is being probed right now. */
  function isProbing(c: Card): boolean {
    return !!probing[keyOf(c)];
  }

  /**
   * Run an action; if it is probing, mark this card as "probing" for the duration.
   *
   * Do this only for the **probing** family: other actions (the switch-chain-head kind) produce no latency numbers, and covering them with
   * "..." would be saying something that does not exist. The criterion is the action id family (`probe` / `probe-auto`) -
   * the only branch that "re-reads the snapshot when done".
   */
  async function run(a: Act, c: Card) {
    if (!a.id.startsWith("probe")) {
      await onAction?.(a);
      return;
    }
    const k = keyOf(c);
    probing = { ...probing, [k]: true };
    try {
      await onAction?.(a);
    } finally {
      const next = { ...probing };
      delete next[k];
      probing = next;
    }
  }

  /**
   * The cards laid out in the grid.
   *
   * The client tier: "auto" comes first (its content = the one auto currently resolves to), followed by
   * every profile.
   *
   * **Do not lay it out under the "auto" tier**: in that tier **what is selected is a profile card itself** - clicking whichever one
   * points auto at it. Laying out another "auto" card would put two things saying the same thing on screen, and
   * that card's content is exactly a copy of "the currently selected one".
   */
  const shown = $derived<Card[]>(auto && current?.id ? [auto, ...cards] : cards);

  /**
   * A card's key (each's key, the expanded state is stored by it).
   *
   * The "default" one **has no profile name to use** - its profile is whatever it points at right now, and that changes;
   * using it as a key would rebuild the whole card as a different one each time the member changes (the expanded state is lost on the spot).
   * So it uses a fixed marker.
   */
  function keyOf(c: Card): string {
    return c.auto ? "\u0000auto" : c.profile;
  }

  /**
   * The stops to expand on the chain: **the first three after the chain head**, with the chain head taking a cell of its own.
   *
   * What the user wants is "who is the chain head, and who is queued behind it" - stops past the fourth mean nothing when reading this screen
   * (that is `newgate tier`'s and the Chains card's business), and three extra rows per card is dozens of rows.
   */
  const FALLBACKS = 3;
  function rest(r: Row): Step[] {
    return (r.steps ?? []).slice(1, 1 + FALLBACKS);
  }

  /** How many stops on the chain are not drawn (`+N` badge). */
  function hidden(r: Row): number {
    return Math.max(0, (r.steps?.length ?? 0) - 1 - FALLBACKS);
  }

  /**
   * Which cards are expanded. **All collapsed by default** (see the groups note below).
   *
   * Expanding is **per card**, not per tier: the question when expanding a card on this screen is always "how does this chain
   * actually go", and that chain's five tiers are read together (only by comparing do you see which tier drops first). Expanding per tier
   * lets you see only one tier at a time, and you would have to open five.
   */
  let open = $state<Record<string, boolean>>({});

  /** The system's "reduce motion" preference. It only affects **how it scrolls**, not whether it scrolls (see toggle). */
  function reduceMotion(): boolean {
    return window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
  }

  /**
   * Expand/collapse a card.
   *
   * # Scroll a bit after expanding
   *
   * The expanded card is **several times taller** (measured, all 5 tiers expanded is about 900px, collapsed about 200px), and it
   * spans the whole row - so its own row is pushed down, the cards above it move too, and the title of the one the user just
   * opened easily runs out of view. So after expanding, bring it into view.
   *
   * Why wait a frame: at that moment the DOM is still collapsed, and `scrollIntoView` measures the **old**
   * position (200px tall collapsed, 900px expanded, hundreds of pixels apart). Only the next frame measures right.
   *
   * Why **not scroll** when collapsing: collapsing is an "I am done looking" action, and moving the person away is exactly the opposite.
   */
  function toggle(profile: string, ev: MouseEvent) {
    const next = !open[profile];
    open = { ...open, [profile]: next };
    if (!next) return;
    const card = (ev.currentTarget as HTMLElement | null)?.closest(".pc");
    if (!card) return;
    requestAnimationFrame(() =>
      card.scrollIntoView({
        block: "start",
        behavior: reduceMotion() ? "auto" : "smooth",
      }),
    );
  }

  /**
   * How many stops still stand behind the chain head (that `+N` when collapsed).
   *
   * When collapsed it gives the **depth**, not who those stops are - who they belong to is only read when expanded (and is
   * `newgate tier`'s and the Chains screen's business). After expanding, `rest()` / `hidden()` take over.
   */
  function depth(r: Row): number {
    return Math.max(0, (r.steps?.length ?? 0) - 1);
  }

  function toneClass(tone: string | undefined): string {
    return tone === "ok" || tone === "warn" || tone === "bad" ? `t-${tone}` : "";
  }

  /**
   * The latency cell: number + color. When there is no sample, **draw nothing** (not 0ms) - "no sample"
   * and "so fast there is no latency" are two different statements, and this screen is exactly for comparing speed.
   *
   * The last probe conclusion (fluent / laggy / ...) goes into title: it is a machine marker and the criterion behind this cell,
   * and hovering should show it, but it should not take layout space (that column is for comparing numbers vertically).
   */
  function msTitle(s: Step): string {
    const key = `${s.provider}/${s.model}`;
    return s.probe ? `${key} · ${s.probe}` : key;
  }
</script>

<!-- Client tabs. When there are no client modules, agents is **one empty tier** (added by the backend), and it is drawn as usual
     as one tab - it is not decoration, it decides which client the "use it" on the cards says. -->
{#if agents.length}
  <div class="agents" role="tablist">
    {#each agents as a (a.id)}
      <button
        class="agent"
        class:on={current?.id === a.id}
        class:off={a.ready === false}
        role="tab"
        aria-selected={current?.id === a.id}
        onclick={() => (tab = a.id)}
      >
        {#if a.icon}
          <!-- The **client's own** icon (see confighook.Agent.Icon): this cell does not know who it is,
               it just puts that path string into a 24-viewport box. Color goes through currentColor,
               so it is right under all four themes; the "not installed" tier is dimmed by .agent.off's opacity. -->
          <svg class="ico brand" viewBox="0 0 24 24" aria-hidden="true">
            <path d={a.icon} />
          </svg>
        {:else}
          <!-- No icon given (or this tier is not a client at all, like "auto"): draw a generic
               terminal-shaped mark. **Drawing nothing** is not an option - one missing icon in a row of tabs looks like a loading
               failure. -->
          <svg class="ico" viewBox="0 0 16 16" aria-hidden="true">
            <rect x="1.5" y="2.5" width="13" height="11" rx="2.5" />
            <path d="M4.6 6.2 6.6 8l-2 1.8M8.4 10.2h3" />
          </svg>
        {/if}
        <span class="nm">{a.name}</span>
        {#if a.profile}
          <!-- The tab only shows the one it is actually going through right now. **Do not say "auto/pinned" here**: that is
               the "which one is selected" question, and it is answered by **which card the highlight falls on** in the grid below
               (see isActive) - saying it in two places will sooner or later get one of them wrong. -->
          <span class="cur mono">{a.profile}</span>
        {/if}
        {#if a.ready === false}
          <span class="dim off">{t("not installed")}</span>
        {/if}
      </button>
    {/each}
  </div>
{/if}

<div class="grid">
  {#each shown as c (keyOf(c))}
    <section class="pc" class:on={isActive(c)} class:folded-open={!!open[keyOf(c)]}>
      <!-- A **real button** covering the whole card: clicking anywhere on the card = selecting it (see pickAction).
           Why this rather than putting onclick on <section>: that way this action cannot be done from the keyboard at all
           (screen readers cannot read "this is clickable" either), and giving it a role would clash with "the card already
           has other buttons" (buttons cannot nest buttons). An underlay button solves both: it is a real
           control, Tab reaches it and Enter clicks it, while the few buttons in the card sit on top of it and handle their own.
           When there is no action to do (it is already selected) **do not lay it** - a control that does nothing when clicked is worse
           than no control; it would make the user think the UI is frozen. -->
      {#if pickAction(c)}
        {@const pa = pickAction(c)!}
        <button
          class="surface"
          data-nopick
          aria-label={pa.label}
          title={pa.label}
          onclick={() => onAction?.(pa)}
        ></button>
      {/if}
      <header class="phead">
        <!-- The chain name (= profile name) is the largest line of text on this card: the only decision the user makes on this screen
             is "which one to use", and the name is that decision's object.
             It is also the **expand/collapse** switch (mimicking Clash's group header): the whole line is clickable, which is much easier to hit than a 12px
             triangle in the corner, and the most frequent actions on this screen are "scan through" and
             "expand to look at this chain". -->
        <button
          class="tog"
          data-nopick
          aria-expanded={!!open[keyOf(c)]}
          onclick={(ev) => toggle(keyOf(c), ev)}
        >
          <svg class="chev" viewBox="0 0 16 16" aria-hidden="true">
            <path d="M6 4l4 4-4 4" />
          </svg>
          {#if c.auto}
            <!-- The "default" one: the name is **default**, followed in parentheses by which one it points at right now.
                 That value in parentheses follows the global default - and that is exactly how it differs from the ds card:
                 both show the same chain right now, but this one says "everyone follows it", and that one says
                 "this one itself". -->
            <span class="pname">{t("auto")}</span>
            {#if c.profile}
              <span class="cur">{t("(currently {profile})", { profile: c.profile })}</span>
            {/if}
          {:else}
            <span class="pname">{c.profile}</span>
          {/if}
        </button>
        <span class="spacer"></span>
        {#if isActive(c)}
          <!-- "This column is using it" is a **statement**, not a button: pointing it at itself is meaningless
               (so the backend does not report that action at all). Same origin as the Concept.Note rule - "not drawing
               that button" and "saying nothing" are two different things. -->
          <!-- "In use" is **the same sentence**, written the same on both cards - who is selected is not decided by the words
               on this paper, but by **which card the highlight falls on** (see isActive). This way the reader sees
               "which one this column selected", rather than two sentences that must be compared to tell apart. -->
          <span class="pill ok-pill" data-using="1">
            {current?.id ? t("in use by {client}", { client: current.name }) : t("in use")}
          </span>
        {/if}
        {#each actions(c) as a (a.id)}
          <!-- Icon only: this button exists on **every card** (a dozen cards is a dozen of them),
               and with text it would stand out more than the card name, while it does an incidental thing. The text moves into
               title/aria-label - keep the mark where it is visible, keep the words where they can speak. -->
          <button
            class="tiny ghost iconbtn"
            data-nopick
            data-action={a.id}
            title={a.label}
            aria-label={a.label}
            onclick={() => run(a, c)}
          >
            <svg class="net" viewBox="0 0 16 16" aria-hidden="true">
              <circle cx="3.1" cy="8" r="1.4" />
              <path d="M6.9 5.7a3.4 3.4 0 0 1 0 4.6" />
              <path d="M9.7 3.5a6.6 6.6 0 0 1 0 9" />
              <path d="M12.5 1.3a9.8 9.8 0 0 1 0 13.4" />
            </svg>
          </button>
        {/each}
      </header>

      {#if c.file}
        <!-- The file path is also **the entry to edit it**: this screen says "which chain it goes through right now", and changing the chain means
             going to that file's card in the config section. The path itself is the most natural landing point - looking at it, the user thinks
             "what I want to change is this file".
             When it cannot jump (no such callback, or that file has no editable card right now), draw it as a line of text as before,
             not as a link that does nothing when clicked. -->
        {#if onOpenFile}
          <button
            class="meta mono link"
            data-nopick
            onclick={() => onOpenFile(c.file!)}
            title={t("edit this file")}
          >
            {c.file}
          </button>
        {:else}
          <div class="meta mono">{c.file}</div>
        {/if}
      {/if}


      <ul class="roles" class:flat={!open[keyOf(c)]}>
        {#if !open[keyOf(c)]}
          <!-- The collapsed look: **one row per tier**, saying only who this tier starts from, how many stops are queued behind it, and how fast.
               Who those stops are is only read when expanded (and is `newgate tier`'s and the Chains screen's
               business); drawing it in the default view makes one card thirty rows, and two cards do not fit on one screen.

               Here tiers sharing a chain head are **not merged into one row**: merging is shorter, but how many rows each group takes varies,
               so card heights vary and rows vary - and this screen is for **comparing vertically** (which tier is fast within a card,
               who is faster between two cards). Equal-height, equal-width rows are worth more than a few fewer rows. -->
          {#each c.roles as r (r.id ?? r.tier)}
            <li class="grp">
              <span class="tname" title={r.tier}>{r.label ?? r.tier}</span>
              <span class="headline">
                {#if r.head}
                  <!-- The chain head gets truncated in a narrow card (`minimax/MiniMax-M2.7-hig...`), and it is the most
                       important value in this row - the full name goes into title, visible on hover. -->
                  <span class="head mono {toneClass(r.tone)}" title={r.head}>{r.head}</span>
                  {#if depth(r)}
                    <span class="depth mono" title={t("+{n} more on the chain", { n: depth(r) })}>
                      +{depth(r)}
                    </span>
                  {/if}
                {:else}
                  <!-- A tier with no candidates cannot be lifted: **why** it cannot be lifted is what this row is about
                       (an empty tier must not just show "empty" in the UI, that looks like a loading failure). -->
                  <span class="dim">{t("no steps")}</span>
                  {#if r.note}<span class="dim note" title={r.note}>{r.note}</span>{/if}
                {/if}
              </span>
              {#if isProbing(c)}
                <!-- Probing: cover the old number (see isProbing). Keeping the old value would make the user click and then
                     see "nothing happened", with the number suddenly jumping a few seconds later. -->
                <span class="ms mono dim">…</span>
              {:else if r.steps?.[0]?.latency_ms}
                <span
                  class="ms mono {toneClass(r.steps[0].latency_tone)}"
                  title={msTitle(r.steps[0])}
                >
                  {r.steps[0].latency_ms}{t("ms")}
                </span>
              {/if}
            </li>
          {/each}
        {:else}
          {#each c.roles as r (r.id ?? r.tier)}
            <li>
              <div class="tier">
                <span class="tierline">
                  <span class="tname" title={r.tier}>{r.label ?? r.tier}</span>
                  {#if r.head}
                    <span class="head mono {toneClass(r.tone)}">{r.head}</span>
                  {:else}
                    <span class="dim">{t("no steps")}</span>
                  {/if}
                </span>
                <!-- Latency gets its own column, right-aligned: this column is **for comparing vertically** (which tier is fast within a card,
                     who is faster between two cards), so it must align like the number column in a table. Following the chain head
                     would make it jump left and right with name lengths, and comparison would be impossible. -->
                {#if isProbing(c)}
                  <span class="ms mono dim">…</span>
                {:else if r.steps?.[0]?.latency_ms}
                  <span class="ms mono {toneClass(r.steps[0].latency_tone)}" title={msTitle(r.steps[0])}>
                    {r.steps[0].latency_ms}{t("ms")}
                  </span>
                {/if}
              </div>

              <!-- The first three stops after the chain head: one size smaller, indented, aligned with the chain head. The chain head is already on the row above,
                   so this starts at the second stop (not saying the chain head again). -->
              {#if rest(r).length}
                <ol class="steps">
                  {#each rest(r) as s, i (i)}
                    <li>
                      <span class="idx mono">{i + 2}</span>
                      <span class="mono binding">
                        <span class="prov">{s.provider}</span><span class="slash">/</span
                        ><span class="model">{s.model}</span>
                      </span>
                      {#if s.profile && s.profile !== c.profile}
                        <span class="pill from">{s.profile}</span>
                      {/if}
                      {#if s.latency_ms}
                        <span class="ms mono {toneClass(s.latency_tone)}" title={msTitle(s)}>
                          {s.latency_ms}{t("ms")}
                        </span>
                      {:else if s.probe}
                        <!-- No latency sample but there is a probe conclusion (for example just probed and the sample has not landed).
                             Draw the conclusion rather than leaving it blank: both say "how is this stop right now". -->
                        <span class="dim grade mono" title={msTitle(s)}>{s.probe}</span>
                      {/if}
                    </li>
                  {/each}
                  {#if hidden(r)}
                    <li class="more dim">
                      {t("+{n} more on the chain", { n: hidden(r) })}
                    </li>
                  {/if}
                </ol>
              {/if}

              {#if r.note}
                <p class="dim skip">{r.note}</p>
              {/if}
            </li>
          {/each}
        {/if}
      </ul>
    </section>
  {/each}
</div>

{#if !cards.length}
  <p class="dim">{t("no profiles")}</p>
{/if}

<style>
  /* ---- client tabs ---- */
  .agents {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-wrap: wrap;
    margin-bottom: 12px;
  }
  .agent {
    display: flex;
    align-items: center;
    gap: 7px;
    background: transparent;
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    padding: 5px 10px;
    color: var(--dim);
  }
  .agent:hover { color: var(--ink); border-color: var(--line); }
  /* The selected tab: background tint + accent border, the same tone as `.on` in the sidebar/TabStrip. */
  .agent.on {
    color: var(--ink);
    background: var(--panel-2);
    border-color: var(--accent);
  }
  /* Not-installed clients **are still drawn**, just one step dimmer: hiding them would make the user think they are unsupported, when the truth
     is "it is there, this machine just does not have it" (same origin as the Concept.Locked rule). */
  .agent.off { opacity: 0.55; }
  .ico {
    width: 14px;
    height: 14px;
    flex: none;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.4;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  /* The probe icon button: a "signal spreading" mark in a square cell (network semantics, pointing at no
     brand). Thinner lines, rounder endpoints - it appears once on each of a dozen cards, and if heavy the whole screen is noisy. */
  .iconbtn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 20px;
    padding: 0;
  }
  .net {
    width: 13px;
    height: 13px;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.3;
    stroke-linecap: round;
  }
  .net circle { fill: currentColor; stroke: none; }

  /* Brand icons and the generic mark above are **not drawn the same way**: the lobehub set is filled
     (`fill="currentColor" fill-rule="evenodd"`), and the generic mark is stroked. Two separate
     classes rather than overriding each other inside one `.ico` - their fill/stroke values are mutually exclusive, and
     mixing them always leaves one side needing `none` to suppress the other, and that gets missed. */
  .ico.brand {
    width: 15px;
    height: 15px;
    fill: currentColor;
    fill-rule: evenodd;
    stroke: none;
  }
  .nm { font-size: 12px; font-weight: 600; }
  .cur { font-size: 11px; color: var(--dim); }
  .off { font-size: 10px; }

  /* ---- cards ---- */
  /* Responsive grid: one card per profile, one column on narrow screens, two on wide. 360px is the width where "tier name + chain head
     + latency + one button" fits, without a row getting so long it cannot be read. */
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
    gap: 12px;
    align-items: start;
  }
  .pc {
    background: var(--panel);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: 8px 10px 9px;
    /* Scroll to it when expanded (see toggle): leave a little space, do not let the card edge touch the top of the scroll area - touching
       looks like it got cut off. */
    scroll-margin-top: 8px;
  }
  /* The expanded one **spans the whole row**.
     Why: it is a member of the grid, and when expanded it is several times taller than its neighbors - left in its own column,
     the other two cards in the same row would leave a large gap below (measured about 1400px wide and several hundred pixels tall),
     and the names of those stops would be squeezed into 360px and truncated. Spanning the row removes the hole, and the chain reads well.
     The cost is that all cards below move down - but that is what "I want to read this one" should look like. */
  .pc.folded-open { grid-column: 1 / -1; }
  /* The one this column is using: an accent line on the left + a brighter border. **One clue**
     (color) is not enough - there is also a statement `in use by ...` below (see .ok-pill). */
  .pc.on {
    border-color: color-mix(in srgb, var(--accent) 55%, var(--line));
    box-shadow: inset 3px 0 0 var(--accent);
  }
  .phead { display: flex; align-items: baseline; gap: 7px; flex-wrap: wrap; }
  /* The card-name cell is also the expand/collapse switch. As a button it must **strip all the browser's
     default button styling**, otherwise it becomes a small box, and this line of text is the largest line on this card. */
  .tog {
    display: flex;
    align-items: center;
    gap: 2px;
    margin-left: -4px;
    padding: 1px 4px 1px 2px;
    background: none;
    border: 0;
    border-radius: var(--radius-sm);
    color: inherit;
    cursor: pointer;
  }
  .tog:hover { background: var(--panel-2); }
  .chev {
    width: 12px;
    height: 12px;
    flex: none;
    fill: none;
    stroke: var(--dim);
    stroke-width: 1.8;
    stroke-linecap: round;
    stroke-linejoin: round;
    transition: transform 120ms ease;
  }
  /* Which way the caret points = which way clicking goes: right when collapsed (expand), down when expanded. */
  .tog[aria-expanded="true"] .chev { transform: rotate(90deg); }
  /* Motion is only used to say "this click landed", so both states respect the system's reduce-motion preference. */
  @media (prefers-reduced-motion: reduce) {
    .chev { transition: none; }
  }
  .pname { font-size: 13.5px; font-weight: 700; }
  .meta { font-size: 10.5px; color: var(--dim); margin: 0 0 5px; }
  /* The path line becomes a button, but must **look like the line of text it was**: it is the least conspicuous thing on this card,
     and now it is an entry point - making it a conspicuous button would add to every card something that competes for attention
     more than "use for whom". Only underline on hover. */
  .meta.link {
    display: block;
    padding: 0;
    background: none;
    border: 0;
    border-radius: 3px;
    text-align: left;
    cursor: pointer;
  }
  .meta.link:hover { color: var(--ink); text-decoration: underline; }

  /* The underlay "select it" button: covers the whole card and sits under the content (see that part of the template).
     The content layer sets pointer-events: none, so clicks **pass through** to it; the real buttons inside
     are enabled separately - this way clicking any blank part of the card selects it, and clicking the small triangle/probe/path each does its own thing. */
  .pc { position: relative; }
  .surface {
    position: absolute;
    inset: 0;
    z-index: 0;
    padding: 0;
    background: transparent;
    border: 0;
    border-radius: inherit;
    cursor: pointer;
  }
  /* Hover only **lifts it a little**: one step brighter border is enough; recoloring the whole block would make a screen of a dozen cards all flash. */
  .pc:has(.surface:hover) { border-color: color-mix(in srgb, var(--accent) 55%, var(--line)); }
  .pc:has(.surface:focus-visible) { outline: 2px solid var(--accent); outline-offset: 1px; }
  .pc > :not(.surface) { position: relative; z-index: 1; pointer-events: none; }
  .pc > :not(.surface) button,
  /* Those that are themselves buttons and hang directly on the card (the file path cell) **must also be enabled separately**:
     the rule above, `:not(.surface) button`, is about "a button **inside** some non-button child element",
     while `.meta.link` is itself that direct child - without this extra rule it would be judged
     `pointer-events: none` and not clickable (measured: hovering over it did nothing, looking like
     that line was dead to begin with). */
  .pc > button:not(.surface) { pointer-events: auto; }

  .roles { list-style: none; margin: 0; padding: 0; }
  .roles > li + li { margin-top: 8px; }
  /* A collapsed row: [tier] [chain head +N] [latency], three columns.
     The tier column has a **fixed width**: it is the baseline this screen aligns vertically against, and following name lengths would break it. */
  .grp {
    display: grid;
    grid-template-columns: 52px minmax(0, 1fr) auto;
    align-items: center;
    gap: 7px;
    /* Fixed row height, not padding: the same tier of two cards must land on the same horizontal line, and the card height must be computable
       (header + 19px x number of tiers). */
    height: 19px;
  }
  /* **Every card is the same height when collapsed**: the standard tiers are five (heavy/normal/mid/light/vision),
     and a profile that writes fewer tiers (only normal+light, say) would naturally be shorter, and several cards side by side would look ragged
     - and this screen is exactly for comparing horizontally. Reserve room for five; the missing ones are left blank at the bottom.
     Worth more than "each one snugly fits its content": ragged heights make the eye find the row again every time. */
  .roles.flat { min-height: 95px; }
  .headline { display: flex; align-items: baseline; gap: 6px; min-width: 0; }
  .headline .head {
    font-size: 11.5px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  /* How many stops remain after the chain head. It is **depth**, not content - you only know who they are when expanded, so here
     only a count is given (the same sentence as the `+N more on the chain` when expanded). */
  .depth { font-size: 10px; color: var(--dim); opacity: 0.7; flex: none; }
  .note { font-size: 10px; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  /* A row = [tier + chain head] [latency], two columns.
     - The first two **must be wrapped in one cell** (.tierline): laid out flat into two columns, the third (latency)
       gets pushed to the next row, and that row looks like "this tier has two chain heads".
     - Latency gets its own column, **right-aligned**: this column is for comparing vertically (this tier is faster than that one), and following
       the chain head would make it jump left and right with name lengths, making comparison impossible. min-width makes this column line up across cards,
       and tabular-nums makes the numbers themselves align.
     - The chain head must not be split into two pieces (`nowrap`): split, that row becomes two rows, with uneven heights.
       The slash between provider/model is a separator; when reading it is **two** names that can be looked up separately. */
  .tier {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: baseline;
    gap: 8px;
  }
  .tierline { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
  /* The tier name takes a fixed width: unfixed, each row's chain head would be ragged, same as above. */
  .tname { color: var(--dim); font-size: 11.5px; min-width: 52px; white-space: nowrap; }
  .head { font-size: 11.5px; white-space: nowrap; }
  /* The latency cell: a **measurement**, so give it a little background to separate it from the text, but use only the most minimal
     means (no border, nothing beyond the corner radius). Numbers use monospace + tabular figures, so a few rows compare vertically
     with their digits aligned. */
  .ms {
    font-size: 10.5px;
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
    text-align: right;
    min-width: 50px;
    padding: 0 5px;
    border-radius: 5px;
    background: color-mix(in srgb, currentColor 12%, transparent);
  }
  .grade { font-size: 10px; white-space: nowrap; }

  .steps {
    list-style: none;
    margin: 3px 0 0;
    padding: 0 0 0 59px; /* align with the chain head above: the indent = the width of tname + that gap */
  }
  .steps > li {
    display: flex;
    align-items: baseline;
    gap: 6px;
    font-size: 11px;
    color: var(--dim);
    padding: 1px 0;
    flex-wrap: wrap;
  }
  .idx { min-width: 10px; opacity: 0.5; font-size: 10px; }
  .binding { color: var(--ink); opacity: 0.72; white-space: nowrap; }
  .slash { opacity: 0.45; }
  .from { font-size: 10px; padding: 0 5px; }
  .more { font-size: 10px; padding-left: 16px; }
  .skip { font-size: 11px; margin: 3px 0 0 59px; }

  /* The tone of the status note (same set as ConceptCard's note): **change only the color, not the shape**. */
  .ok-pill { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 45%, transparent); }
  .t-ok { color: var(--ok); }
  .t-warn { color: var(--warn); }
  .t-bad { color: var(--danger); }
</style>
