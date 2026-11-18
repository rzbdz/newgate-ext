<script lang="ts">
  // Model picker: an **input-capable dropdown**, with data from the provider itself (not from providers.json).
  //
  // # Why not a datalist
  //
  // Before the change this cell was `<input list=...>`: a datalist only gives "the declared models", and that list had to be
  // hand-written by the user; when the upstream ships a new model, the user has to go add a line in the provider card, then come back and pick. And
  // a datalist has no grouping, no fuzzy search, and does not say "did the upstream answer at all".
  //
  // Here it asks once **at the moment of opening** (see modelList in api.ts -> the BFF's
  // /api/ext/modellist -> the distribution's modules/modellist):
  //   - the upstream supports listing -> list what it really offers right now (with groups), with fuzzy search;
  //   - the upstream does not support it / the key is not allowed / the network is down -> **fall back to the list declared in providers.json**,
  //     and write the reason at the top of the menu. This is not a failure: most upstreams do not have this endpoint.
  //
  // # Why fixed positioning
  //
  // The card `.card` has `overflow: hidden` (see app.css). An absolutely positioned menu gets clipped by the card,
  // so in the place that needs it most (a screen with a dozen tier cards) only a slit is left. Fixed to the viewport, with coordinates computed from the input box,
  // and the width clamped into the viewport - close the menu directly on scroll/zoom (see closeOnViewport).
  import { onMount, untrack } from "svelte";
  import { t } from "./i18n";
  import {
    modelList,
    fingerprintCache,
    fingerprintRun,
    type FingerprintEntry,
    type FingerprintMatch,
    type ModelInfo,
    type ModelList,
  } from "./api";
  import { fuzzyFilter } from "./fuzzy";

  let {
    value = "",
    provider = "",
    declared = [],
    readonly = false,
    /**
     * Whether to show the last verified fingerprint next to the cell, and offer a "verify" button.
     *
     * On by default; the free-verification page turns it off - that page has its own API/challenge-number selector and a
     * "get fingerprint" button, and putting another button of the same name in the same cell would leave "which one was pressed this time"
     * with no answer.
     */
    verify = true,
    onchange,
  }: {
    value?: string;
    provider?: string;
    /** The model names declared in providers.json: use this set when the upstream cannot be asked. */
    declared?: string[];
    readonly?: boolean;
    verify?: boolean;
    onchange: (model: string) => void;
  } = $props();

  let open = $state(false);
  /** The string being typed into the input box. **Cleared when opened**, so the menu lists all models. */
  let query = $state(untrack(() => value));
  let active = $state(0);
  let loading = $state(false);
  let loadError = $state("");
  let catalogs = $state<Record<string, ModelList>>({});

  /**
   * Fingerprint conclusions cached by model name (from the cache action of `/api/ext/fingerprint`).
   *
   * It is read once only when the dropdown is opened / the provider is switched (the cache file is small), and **does not verify automatically**:
   * verify spends tokens, and it must be triggered by a person. What is shown here is always "the one verified last time".
   */
  let fingerprints = $state<Record<string, FingerprintEntry>>({});
  let fpError = $state("");
  let fpBusy = $state(false);

  let input: HTMLInputElement | undefined = $state();
  let menuEl: HTMLDivElement | undefined = $state();
  let pos = $state({ left: 0, top: 0, width: 260, maxHeight: 320 });
  // The menu id must be unique: several candidate rows are on one screen at once, each with its own picker.
  const menuID = $props.id();

  // When the value is changed externally (switch provider, revert draft, load another profile), sync the input box -
  // but **do not grab it while open**: that is exactly when the user is typing.
  let seenValue = untrack(() => value);
  $effect(() => {
    if (seenValue === value) return;
    seenValue = value;
    if (!open) query = value;
  });

  // Switching provider = switching to a different list: the menu and the local query both start over (the cache is stored per provider,
  // so switching back still has it). The fingerprint switches too: it is stored by "provider/model", and keeping the previous vendor's
  // would only make the new cell show a conclusion that does not belong to it.
  let seenProvider = untrack(() => provider);
  $effect(() => {
    if (seenProvider === provider) return;
    seenProvider = provider;
    open = false;
    loadError = "";
    query = value;
    fingerprints = {};
    fpError = "";
    if (verify) void loadFingerprints();
  });

  onMount(() => {
    if (verify) void loadFingerprints();
  });

  /** The last conclusion for the selected model (undefined if none). */
  const selectedFP = $derived(provider && value ? fingerprints[value] : undefined);

  const catalog = $derived(provider ? catalogs[provider] : undefined);

  /** What to list in the menu: use the upstream's if it can say, otherwise the declared one. */
  const models = $derived<ModelInfo[]>(
    catalog?.supported && (catalog.models?.length ?? 0) > 0
      ? catalog.models
      : declared.map((id) => ({ id })),
  );

  const filtered = $derived(fuzzyFilter(query, models, (m) => [m.id, m.name, m.group]));

  const status = $derived.by(() => {
    if (loadError) return loadError;
    if (loading) return t("asking the provider for its models…");
    if (!catalog) {
      return provider
        ? t("declared in providers.json — open the list to ask the provider")
        : t("pick a provider first");
    }
    if (catalog.supported && (catalog.models?.length ?? 0) > 0) {
      let s = t("{n} models from the provider", { n: catalog.models.length });
      if (catalog.cached) s += " · " + t("cached");
      if (catalog.adapter) s += " · " + catalog.adapter;
      return s;
    }
    return catalog.note || t("the provider did not list its models — showing the declared list");
  });

  /**
   * Groups: models in the same group are listed together, and a group's order = the rank of its best hit.
   *
   * When there are no groups it is one empty group (the menu draws no heading) - "the upstream gave no groups" and "all models are in
   * the same group" should look the same in the UI anyway.
   */
  const grouped = $derived.by(() => {
    const map = new Map<string, { m: ModelInfo; i: number }[]>();
    filtered.forEach((m, i) => {
      const g = m.group ?? "";
      const hit = map.get(g);
      if (hit) hit.push({ m, i });
      else map.set(g, [{ m, i }]);
    });
    let out = [...map.entries()].map(([group, items]) => ({ group, items }));
    if (query.trim()) {
      // fuzzyFilter is already sorted by score; groups are ordered by the rank of their first hit, and within a group the score order is kept.
      out = out.sort((a, b) => a.items[0].i - b.items[0].i);
    }
    return out;
  });

  async function loadFingerprints() {
    if (__NEWGATE_DEMO__ || !provider || !verify || readonly) return;
    try {
      const list = await fingerprintCache(provider);
      const next: Record<string, FingerprintEntry> = {};
      for (const e of list) next[e.model] = e;
      fingerprints = next;
      fpError = "";
    } catch (e) {
      // The endpoint is optional: it is 404 when that module is not installed. **Not a failure** - the cell works as usual,
      // there is just no fingerprint to show (same as the 404 from modelList).
      fpError = e instanceof Error ? e.message : String(e);
    }
  }

  /**
   * Verify the selected model on the spot. **Spends tokens**: the button's title says so, no confirmation dialog -
   * pressing this button already means "I want to verify", and asking again only adds a step.
   */
  async function verifySelected() {
    const model = value.trim();
    if (!verify || !provider || !model || fpBusy) return;
    fpBusy = true;
    fpError = "";
    try {
      const e = await fingerprintRun(provider, model, "auto", 3);
      fingerprints = { ...fingerprints, [e.model]: e };
    } catch (e) {
      fpError = e instanceof Error ? e.message : String(e);
    } finally {
      fpBusy = false;
    }
  }

  function matchTone(m: FingerprintMatch): string {
    switch (m) {
      case "match":
      case "dated":
        return "ok";
      case "family":
      case "mismatch":
        return "warn";
      default:
        return "bad";
    }
  }

  function matchText(m: FingerprintMatch): string {
    switch (m) {
      case "match":
        return t("match");
      case "dated":
        return t("dated snapshot");
      case "family":
        return t("same family");
      case "mismatch":
        return t("different model");
      case "unknown":
        return t("no ranking");
      case "error":
        return t("error");
      default:
        return m;
    }
  }

  function fpLabel(e: FingerprintEntry): string {
    const name = e.predicted_name || e.predicted;
    return name || t("no ranking");
  }

  function fpPct(e: FingerprintEntry): string {
    return e.probability == null ? "" : ` ${Math.round(e.probability * 100)}%`;
  }

  function fpMark(m: FingerprintMatch): string {
    switch (m) {
      case "match":
      case "dated":
        return "✓";
      case "family":
      case "mismatch":
        return "⚠";
      default:
        return "✕";
    }
  }

  async function load(refresh: boolean) {
    // The demo page has no backend (see src/env.d.ts): list the declared list as usual, and do not ask an endpoint that does not exist.
    if (__NEWGATE_DEMO__) return;
    if (!provider || readonly) return;
    const p = provider;
    loading = true;
    loadError = "";
    try {
      const res = await modelList(p, refresh);
      catalogs = { ...catalogs, [p]: res };
    } catch (e) {
      loadError = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  function place() {
    if (!input) return;
    const r = input.getBoundingClientRect();
    const gap = 4;
    const maxW = Math.max(220, Math.min(560, window.innerWidth - 16));
    const width = Math.min(Math.max(r.width, 260), maxW);
    let left = Math.min(r.left, window.innerWidth - width - 8);
    left = Math.max(8, left);
    let top = r.bottom + gap;
    let maxHeight = Math.min(320, window.innerHeight - top - 8);
    if (maxHeight < 140) {
      // It does not fit below: flip above.
      maxHeight = Math.min(320, r.top - gap - 8);
      top = Math.max(8, r.top - gap - maxHeight);
    }
    pos = { left, top, width, maxHeight };
  }

  function openMenu() {
    if (readonly) return;
    place();
    if (!open) {
      open = true;
      active = 0;
      // Clear the query when opened: what the user wants to see is **everything**, not "only what matches the current value".
      // The current value is still in the placeholder (see the template), so it does not look lost.
      // Do not clear when already open - that would erase the string the user is typing.
      query = "";
      if (provider && !catalogs[provider] && !loading) void load(false);
      // The fingerprint is a local cache (tens of KB), re-read every time it is opened: if verify was just run in the terminal,
      // that cell in the dropdown should be the new conclusion right away, not the one from page load.
      if (verify) void loadFingerprints();
    }
  }

  function typed(e: Event) {
    if (!open) {
      place();
      open = true;
    }
    query = (e.currentTarget as HTMLInputElement).value;
    onchange(query);
    active = 0;
  }

  function pick(m: ModelInfo) {
    onchange(m.id);
    query = m.id;
    open = false;
  }

  function close() {
    open = false;
    query = value;
  }

  function refresh() {
    if (loading) return;
    place();
    open = true;
    void load(true);
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      const wasOpen = open;
      if (!wasOpen) openMenu();
      if (filtered.length) active = wasOpen ? Math.min(active + 1, filtered.length - 1) : 0;
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      if (filtered.length) active = Math.max(active - 1, 0);
    } else if (e.key === "Enter") {
      if (open && filtered[active]) {
        e.preventDefault();
        pick(filtered[active]);
      } else {
        close();
      }
    } else if (e.key === "Escape") {
      e.preventDefault();
      close();
      input?.blur();
    } else if (e.key === "Tab") {
      close();
    }
  }

  // The menu is fixed: once the viewport moves, it hangs at a position that no longer corresponds to the input box.
  // Closing directly is simpler than chasing it with repositioning, and safer than "looks still open, click and pick the wrong row".
  //
  // Use a **capture** listener: the card lives in an `overflow: auto` pane, and scrolling in that layer does not
  // bubble to window. The menu's own scrolling must pass through - that is the user paging through the list.
  $effect(() => {
    if (!open) return;
    const onScroll = (e: Event) => {
      if (menuEl && e.target instanceof Node && menuEl.contains(e.target)) return;
      close();
    };
    const onResize = () => close();
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("resize", onResize);
    return () => {
      window.removeEventListener("scroll", onScroll, true);
      window.removeEventListener("resize", onResize);
    };
  });
</script>

<div class="picker">
  <input
    bind:this={input}
    class="mono model"
    role="combobox"
    aria-autocomplete="list"
    aria-expanded={open}
    aria-controls={menuID}
    autocomplete="off"
    spellcheck="false"
    disabled={readonly}
    value={query}
    placeholder={value || t("model")}
    onfocus={openMenu}
    onclick={openMenu}
    oninput={typed}
    onkeydown={keydown}
    onblur={close}
  />
  {#if provider && !readonly}
    <button
      type="button"
      class="tiny ghost probe"
      title={t("ask the provider again (ignore the cached answer)")}
      onmousedown={(e) => e.preventDefault()}
      onclick={refresh}
      disabled={loading}
    >↻</button>
  {/if}

  {#if verify && provider && !readonly && value.trim()}
    <!-- The last verified fingerprint sits right next to the cell: it is a **statement** (cache), not "this cell's value",
         so it takes no part in editing and does not go into drafts. Its absence means "not verified yet", not a failure. -->
    <span
      class="fp {selectedFP ? matchTone(selectedFP.match) : 'none'}"
      title={selectedFP
        ? `${fpLabel(selectedFP)}${fpPct(selectedFP)} · ${selectedFP.checked_at}`
        : t("no fingerprint yet — verify to ask the upstream")}
    >
      {#if selectedFP}{fpMark(selectedFP.match)} {fpLabel(selectedFP)}{fpPct(selectedFP)}{:else}{t("no fingerprint")}{/if}
    </span>
    <button
      type="button"
      class="tiny ghost"
      title={t("ask the upstream for a fresh fingerprint (costs tokens)")}
      onmousedown={(e) => e.preventDefault()}
      onclick={verifySelected}
      disabled={fpBusy}
    >{fpBusy ? t("verifying…") : t("verify")}</button>
  {/if}
  {#if fpError}
    <span class="fp bad" title={fpError}>!</span>
  {/if}

  {#if open}
    <div
      bind:this={menuEl}
      id={menuID}
      class="menu"
      role="listbox"
      style:left="{pos.left}px"
      style:top="{pos.top}px"
      style:width="{pos.width}px"
      style:max-height="{pos.maxHeight}px"
    >
      <div class="status" class:bad={!!loadError || !!fpError}>
        {status}
        {#if fpError} · {t("fingerprint: {err}", { err: fpError })}{/if}
      </div>
      {#if grouped.length}
        {#each grouped as g (g.group)}
          {#if g.group}
            <div class="group"><span>{g.group}</span><span class="dim">{g.items.length}</span></div>
          {/if}
          {#each g.items as it (it.m.id)}
            {@const fpe = fingerprints[it.m.id]}
            <button
              type="button"
              class="opt"
              class:active={it.i === active}
              class:selected={it.m.id === value}
              role="option"
              aria-selected={it.m.id === value}
              onmousedown={(e) => e.preventDefault()}
              onclick={() => pick(it.m)}
            >
              <span class="oinfo">
                <span class="mid mono">{it.m.id}</span>
                {#if it.m.name && it.m.name !== it.m.id}
                  <span class="mname dim">{it.m.name}</span>
                {/if}
              </span>
              {#if fpe}
                <!-- Every row in the dropdown carries the **last** fingerprint: before picking you can already see "this row says
                     this name, but last time it verified as something else". That is the most valuable information this dropdown can have. -->
                <span class="fpm {matchTone(fpe.match)}" title={matchText(fpe.match)}>
                  {matchText(fpe.match)}{#if fpe.probability != null} · {Math.round(fpe.probability * 100)}%{/if}
                </span>
              {/if}
            </button>
          {/each}
        {/each}
      {:else}
        <div class="status dim">{t("no model matches “{q}”", { q: query })}</div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .picker { position: relative; display: inline-flex; align-items: center; flex-wrap: wrap; row-gap: 4px; min-width: 220px; }
  .picker .model { flex: 1; min-width: 160px; }
  .probe { padding: 0 4px; line-height: 1; }
  /* The fingerprint cell: a statement, not an input. Color only expresses "did the name match", and does not change the shape. */
  .fp {
    font-size: 11px;
    font-family: var(--mono);
    padding: 1px 6px;
    border-radius: 99px;
    border: 1px solid var(--line);
    color: var(--dim);
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .fp.ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 50%, var(--line)); }
  .fp.warn { color: var(--warn); border-color: color-mix(in srgb, var(--warn) 50%, var(--line)); }
  .fp.bad { color: var(--danger, #c0392b); border-color: var(--danger, #c0392b); }
  .fp.none { color: var(--dim); border-style: dashed; }
  .menu {
    position: fixed;
    z-index: 50;
    overflow-y: auto;
    background: var(--panel);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    box-shadow: 0 10px 28px rgba(0, 0, 0, 0.28);
    padding: 3px;
  }
  .status {
    font-size: 11px;
    color: var(--dim);
    padding: 4px 7px 5px;
    border-bottom: 1px dashed var(--line);
    margin-bottom: 2px;
    overflow-wrap: anywhere;
  }
  .status.bad { color: var(--danger, #c0392b); }
  .group {
    display: flex;
    justify-content: space-between;
    gap: 8px;
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--dim);
    padding: 7px 7px 3px;
    border-top: 1px dashed var(--line);
    margin-top: 2px;
  }
  .group:first-of-type { border-top: 0; margin-top: 0; }
  .opt {
    display: flex;
    flex-direction: row;
    align-items: baseline;
    gap: 8px;
    width: 100%;
    text-align: left;
    background: transparent;
    border: 0;
    border-radius: calc(var(--radius) - 2px);
    padding: 5px 7px;
    cursor: pointer;
    color: var(--fg);
    font-size: 12px;
  }
  .oinfo { display: flex; flex-direction: column; gap: 1px; flex: 1; min-width: 0; }
  .opt:hover, .opt.active { background: color-mix(in srgb, var(--accent) 16%, transparent); }
  .opt.selected .mid { font-weight: 600; }
  .opt .mid { font-size: 12px; overflow-wrap: anywhere; }
  .opt .mname { font-size: 11px; }
  /* The last fingerprint on the right of each row: small, monospace, not stealing the model name's place. */
  .fpm { font-size: 10px; font-family: var(--mono); white-space: nowrap; color: var(--dim); }
  .fpm.ok { color: var(--ok); }
  .fpm.warn { color: var(--warn); }
  .fpm.bad { color: var(--danger, #c0392b); }
</style>
