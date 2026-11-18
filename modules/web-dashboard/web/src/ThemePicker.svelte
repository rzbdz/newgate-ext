<script lang="ts">
  // Theme switching: **one icon button**; the menu appears only when clicked.
  //
  // # Why not a dropdown
  //
  // It used to be a `<select>`. A dropdown is a **permanently present** control: one column wide, a box, an arrow,
  // taking up the top bar at every moment - and it is clicked a few times a year at most (the theme is something you pick once after install).
  // The user's own words were "the theme should just be a button, the menu pops up only when clicked, do not make a menu by default",
  // and "keep rarely used control buttons to a minimum".
  //
  // # Why this component has two callers
  //
  // The product UI (App.svelte) and the demo page (demo/DemoShell.svelte) each have a shell, and before this they each
  // wrote their own dropdown - and the two implementations had already drifted once elsewhere (the sidebar ordering rules). For something like a menu,
  // "what it looks like" is pure appearance and the two must be word for word identical, so it is extracted into one copy.
  import { t } from "./i18n";
  import type { Themes } from "./api";

  let {
    doc,
    onPick,
  }: {
    doc: Themes;
    onPick: (id: string) => void;
  } = $props();

  let open = $state(false);
  let box = $state<HTMLElement | null>(null);

  /**
   * Clicking elsewhere folds it away.
   *
   * Only listen while expanded (leave no handler behind that runs on every click when collapsed). `pointerdown`
   * rather than `click`: it judges on **press**, so "hold, drag outside the menu, release" does not close the menu
   * and immediately have it reopened by the button's click.
   */
  $effect(() => {
    if (!open) return;
    const away = (e: PointerEvent) => {
      if (!box?.contains(e.target as Node)) open = false;
    };
    const esc = (e: KeyboardEvent) => {
      if (e.key === "Escape") open = false;
    };
    document.addEventListener("pointerdown", away);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("pointerdown", away);
      document.removeEventListener("keydown", esc);
    };
  });

  /** The name of the current theme (empty id = the factory one, which is not in doc.themes). */
  const current = $derived(
    doc.themes.find((x) => x.id === doc.active)?.name ?? t("follow the system"),
  );
</script>

<div class="wrap" bind:this={box}>
  <!-- The mark is the **contrast** one (a circle, right half solid): it is the common way to draw "dark/light",
      points at no brand, and makes sense for all four options. -->
  <button
    class="icon"
    aria-haspopup="listbox"
    aria-expanded={open}
    aria-label={t("theme")}
    title={`${t("theme")} · ${current}`}
    onclick={() => (open = !open)}
  >
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="8" cy="8" r="5.9" />
      <path d="M8 2.1a5.9 5.9 0 0 1 0 11.8z" />
    </svg>
  </button>

  {#if open}
    <div class="menu" role="listbox" aria-label={t("theme")}>
      {#each [{ id: "", name: t("follow the system") }, ...doc.themes] as o (o.id)}
        <button
          class="opt"
          role="option"
          aria-selected={doc.active === o.id}
          onclick={() => {
            onPick(o.id);
            open = false;
          }}
        >
          <span class="tick" class:on={doc.active === o.id}>✓</span>
          <span class="nm">{o.name}</span>
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .wrap { position: relative; }

  /* Icon button: same height as the other small buttons in the top bar, but only one cell wide. */
  .icon {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 26px;
    padding: 0;
    background: transparent;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    color: var(--dim);
    cursor: pointer;
  }
  .icon:hover { color: var(--ink); border-color: color-mix(in srgb, var(--ink) 35%, var(--line)); }
  .icon svg {
    width: 15px;
    height: 15px;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.3;
  }
  .icon svg path { fill: currentColor; stroke: none; }

  /* Menu: **right-aligned**, hanging below the button (things on the right of the top bar expand leftwards so they do not run off screen). */
  .menu {
    position: absolute;
    top: calc(100% + 5px);
    right: 0;
    z-index: 40;
    min-width: 150px;
    padding: 4px;
    background: var(--panel);
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    box-shadow: 0 8px 22px rgb(0 0 0 / 26%);
  }
  .opt {
    display: flex;
    align-items: center;
    gap: 7px;
    width: 100%;
    padding: 5px 8px;
    background: transparent;
    border: 0;
    border-radius: 5px;
    color: var(--ink);
    font-size: 12px;
    text-align: left;
    cursor: pointer;
  }
  .opt:hover { background: var(--panel-2); }
  /* Use a **check mark** for the selected one rather than inverting the whole row: an inverted row looks like "the mouse is hovering here" in dark mode,
     while the check mark says "this is the current one" - the two things would hold at the same time on this cell. */
  .tick {
    width: 10px;
    color: var(--accent);
    opacity: 0;
    font-size: 11px;
  }
  .tick.on { opacity: 1; }
  .nm { flex: 1; }
</style>
