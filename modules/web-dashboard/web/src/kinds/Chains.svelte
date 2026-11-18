<script lang="ts">
  // A screen of laid-out candidate chains (shape in the core lib/view's Chains).
  //
  // The division of labor with Table is written there: a table is "one fact per row", and readers only look; here each row **carries a destination**
  // ("this tier goes through this chain now"), so it has two things a table does not - the chain head takes a cell of its own (the user's
  // only question is "why is it not the one I thought", and the eye lands there), and buttons on rows.
  //
  // Three conventions, all enforced only by the frontend:
  //
  //  1. **The chain head reads Head, not Steps[0]**. If the frontend picked the first one itself for an empty chain, "what to do with an empty
  //     chain" would be copied into the frontend, while it is a conclusion the backend already computed (see ChainRow.Head).
  //  2. **One row = one tier, ordered as the contributor gives it**. Nothing is sorted or grouped here, and no tier name is recognized -
  //     `heavy`/`normal` are just strings, and like module names they are "content the backend already translated".
  //  3. **tone is semantics, not color** (same as Table). Any unrecognized word is treated as not given.
  type Step = { provider: string; model: string; profile?: string; note?: string; tone?: string };
  /** A button on a row. label is the human sentence the contributor wrote (already translated by the backend); the UI does not translate it. */
  type Act = { id: string; label: string };
  type Role = {
    tier: string;
    label?: string;
    head?: string;
    steps?: Step[];
    note?: string;
    tone?: string;
    actions?: Act[];
  };
  type Card = { profile: string; file?: string; default?: boolean; roles: Role[] };
  type Data = { cards: Card[] };

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
     * **Deliberately goes through `onRowAction`, not `Concept.Actions`**: after an action runs, the UI
     * does only one thing - re-read the snapshot. Card actions (`RunConceptAction`) are for things like "make this one the
     * default", which **need a different card after they finish** (the profile card disappears itself); while this row changes
     * **a row inside the same card**, and the card is still there. Both paths lose the act of "running it" itself, and only the object
     * they look back at differs - which is exactly where they should be kept apart.
     *
     * The row ID is given by rowID below (`profile + "/" + tier`, the same construction as the core ChainRow.ID
     * - that side builds it the same way, and what `RunRowAction` gets is this string).
     */
    onAction?: (row: string, a: RowAction) => void;
  } = $props();

  const cards = $derived(data?.cards ?? []);

  /**
   * A row's identity: **which profile's which tier this row is**.
   *
   * Both are needed, because "switch heavy to zhipu/glm-4" is two different things in `demo.kv` and `alt.kv`
   * - with only the tier name, the backend cannot know which file to touch (it could only guess
   * "the one currently in effect", while this screen clearly lists every one).
   *
   * The construction is **exactly the same as the backend's** (`profile + "/" + tier`, see the core ChainRow.ID part:
   * a row ID is a machine marker, and both sides must build the same string). Do not establish a different separator here
   * - the backend uses this string to match in `RunRowAction`, and a mismatch shows as "clicking the button does nothing".
   */
  function rowID(card: Card, role: Role): string {
    return `${card.profile}/${role.tier}`;
  }

  /** Which profile this stop comes from - **not repeated when it is the same as the card name** (which is the vast majority). */
  function foreign(card: Card, s: Step): boolean {
    return !!s.profile && s.profile !== card.profile;
  }

  function toneClass(tone: string | undefined): string {
    return tone === "ok" || tone === "warn" || tone === "bad" ? `t-${tone}` : "";
  }
</script>

{#each cards as card (card.profile)}
  <div class="chain">
    <!-- Card header: this profile's name + "is it the one in effect right now".
         The latter means the same thing and uses the same tone (green) as Concept.Note's "current config",
         because the first thing the user looks for on this screen is "who am I actually going through right now". -->
    <div class="row head">
      <span class="profile">{card.profile}</span>
      {#if card.default}
        <span class="pill note-ok" data-default="1">{t("default")}</span>
      {/if}
      {#if card.file}
        <span class="meta">{card.file}</span>
      {/if}
    </div>

    <ul class="roles">
      {#each card.roles as role (role.tier)}
        <li>
          <div class="row tier">
            <span class="tier-name" title={role.tier}>{role.label ?? role.tier}</span>
            <!-- The chain head gets its own cell (see point 1 above). With no candidates at all it is a statement,
                 not an empty cell - empty would make the reader think the chain failed to load. -->
            {#if role.head}
              <span class="head-name {toneClass(role.tone)}">{role.head}</span>
            {:else}
              <span class="dim">{t("no steps")}</span>
            {/if}
            <span class="spacer"></span>
            {#each role.actions ?? [] as a (a.id)}
              <!-- The text is given by the contributor (translated by the backend), so no t() here. -->
              <button
                class="tiny ghost"
                data-action={a.id}
                title={a.label}
                onclick={() => onAction?.(rowID(card, role), a)}
              >
                {a.label}
              </button>
            {/each}
          </div>

          <!-- Chain expansion: who else is within the chain head. **Not drawn when there is only one stop** - that row would be
               identical to the cell above, and one more level of indentation just says the same name twice. -->
          {#if (role.steps?.length ?? 0) > 1}
            <ol class="steps">
              {#each role.steps ?? [] as s, i (i)}
                <li class={toneClass(s.tone)}>
                  <span class="idx">{i + 1}</span>
                  <!-- provider and model are laid out separately (the slash between them is a separator, not part of
                       the data): one line `ark/deepseek-v3` reads like one name, while they are
                       two things that can be looked up separately. -->
                  <span class="binding">
                    <span class="prov">{s.provider}</span><span class="slash">/</span
                    ><span class="model">{s.model}</span>
                  </span>
                  {#if foreign(card, s)}
                    <!-- The stops from another profile **must state their origin**: otherwise the user looks for a candidate that does not exist
                         in the file they are viewing. -->
                    <span class="pill from">{s.profile}</span>
                  {/if}
                  {#if s.note}
                    <span class="dim why">{s.note}</span>
                  {/if}
                </li>
              {/each}
            </ol>
          {/if}

          <!-- The summary sentence at the chain tail (today it is "N candidates skipped"). It goes **below the row** rather than
               inline, because it is about the candidates that were skipped - those are not on the chain at all, and inline has no
               place for them. -->
          {#if role.note}
            <p class="dim skip">{role.note}</p>
          {/if}
        </li>
      {/each}
    </ul>
  </div>
{/each}

{#if !cards.length}
  <!-- Empty and "broken" must look different: here it says "there is not one profile on this machine",
       while an unreadable one (a corrupt file and the like) is said by the card's own error branch. -->
  <p class="dim">{t("no profiles")}</p>
{/if}

<style>
  /* Separate a stack of cards with a line rather than a border: this whole block is **the content of one card**
     (the outer .card already has a border), and drawing another frame inside is a frame within a frame. */
  .chain + .chain {
    margin-top: 14px;
    padding-top: 12px;
    border-top: 1px solid var(--line);
  }
  .head { align-items: baseline; gap: 8px; margin-bottom: 6px; }
  .profile { font-weight: 600; font-size: 13px; }
  .meta { color: var(--dim); font-family: var(--mono); font-size: 11px; }

  .roles { list-style: none; margin: 0; padding: 0; }
  .roles > li + li { margin-top: 7px; }
  .tier { align-items: baseline; gap: 8px; }
  /* The tier name takes a fixed width: unfixed, each row's arrow would be ragged, and the value of these rows is precisely
     **vertical comparison** (who heavy goes through, who normal goes through). */
  .tier-name { color: var(--dim); font-size: 12px; min-width: 62px; }
  .head-name { font-family: var(--mono); font-size: 12px; }

  .steps {
    list-style: none;
    margin: 4px 0 0;
    padding: 0 0 0 62px; /* align with the chain head above: the indent = the width of tier-name */
  }
  .steps > li {
    display: flex;
    align-items: baseline;
    gap: 6px;
    font-size: 11px;
    color: var(--dim);
    padding: 1px 0;
  }
  .idx {
    font-family: var(--mono);
    min-width: 12px;
    opacity: 0.5;
  }
  .binding { font-family: var(--mono); color: var(--ink); opacity: 0.75; }
  .slash { opacity: 0.45; }
  .from { font-size: 10px; padding: 0 5px; }
  .why { font-size: 11px; }
  .skip { font-size: 11px; margin: 3px 0 0 62px; }

  /* The tone of the status note (same set as ConceptCard's note): **change only the color, not the shape**. */
  .note-ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 45%, transparent); }
  .t-ok { color: var(--ok); }
  .t-warn { color: var(--warn); }
  .t-bad { color: var(--danger); }
</style>
