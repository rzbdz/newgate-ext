<script lang="ts">
  // A group of records: each has a few fields, and the fields declare their own type (text / select / secret / lines).
  //
  // Division of labor with Toggles: that one is "a group of homogeneous switches", one value each; here each one is a **small
  // form**, and the count can grow or shrink. Both are needed, because stuffing the provider shape into toggles comes out
  // awkward on both sides.
  //
  // Changes are made in place and land only on save (consistent with the rest of this UI). What is submitted is **all records**:
  // whatever is not submitted was deleted - so deleting needs no second endpoint (see applyProviders in config).
  import { untrack } from "svelte";
  import { t } from "../i18n";

  type Kind = "text" | "select" | "secret" | "lines";
  type Field = {
    id: string;
    label: string;
    kind: Kind;
    value?: string;
    options?: string[];
    placeholder?: string;
    why?: string;
  };
  // The name is not Record: that is TypeScript's built-in generic, and a collision reports errors elsewhere,
  // making it look like a ghost when tracked down. base is the baseline of the file this record represents at load (only for writable record sets;
  // deleting it needs a CAS check, see view.Record.Base).
  type Rec = { id: string; label: string; fields: Field[]; removable?: boolean; base?: string };
  type Data = {
    file?: string;
    base?: string;
    items: Rec[];
    can_add?: boolean;
    add_label?: string;
    // The field shape when adding a record (given by the contributor, see view.Records.Blank).
    blank?: Field[];
  };

  let {
    data,
    draft,
    readonly,
    onEdit,
  }: {
    data: Data;
    draft: unknown;
    readonly: boolean;
    onEdit: (v: unknown) => void;
  } = $props();

  // key is **render identity**, id is **backend identity**. They are separate because "the same row" must be
  // stable over the whole lifecycle, while id is empty for a while (a newly added row is not saved yet, and the backend does not
  // know it yet). Using the array index as identity was this card's previous approach, and the cost was measured: after deleting a row
  // Svelte reuses DOM by key, while spec (taken from data.items[i]) and values (taken from items[i])
  // come from two different arrays, and once the lengths differ they misalign - the most glaring symptom is **the card title still says
  // the filename that was deleted, while the fields below belong to the next file**.
  type Item = { key: string; id: string; base?: string; values: globalThis.Record<string, string> };

  // Same as MappingEditor/Toggles: store "have I edited this", and if not, follow props -
  // otherwise after a background refresh the UI still shows an old value.
  //
  // The submitted shape is `{"items": [...], "removed": [...]}`, not a bare array:
  // this `edit` blob is **defined by the concept itself**, and it will eventually need to hold a second kind of action (like "this one
  // should be deleted"). `removed` explicitly reports the deleted id+base - not relying on "one fewer submitted is inferred as a delete",
  // because deleting that record may cost deleting a file (the profiles card), and an inference is too dangerous.
  // Known read-only record sets (providers) ignore removed and do not require base (apply finds by id).
  let edited = $state<Item[] | null>(null);
  let removedAccum = $state<{ id: string; base?: string }[]>([]);
  const items = $derived(
    edited ?? (draft as { items?: Item[] } | undefined)?.items ?? fromData(data),
  );
  /** On init, carry back the already-deleted ones from the backend snapshot too (taken by base), so they are not lost between refreshes. */
  const removed = $derived(
    removedAccum.length
      ? removedAccum
      : (draft as { removed?: { id: string; base?: string }[] } | undefined)?.removed ?? [],
  );

  /** Eye: turn a password-type input in one cell into plaintext. **Only for "what I just pasted"** - the value itself
      (the already existing one) is not on this side at all, so lighting it up reveals nothing. */
  let shown = $state<Record<string, boolean>>({});

  /** The render identity of a new row: a monotonic in-process counter is enough (it need not be stable across refreshes - a new row was
      never saved, and it should disappear after a refresh). */
  let seq = 0;

  function fromData(d: Data): Item[] {
    return (d.items ?? []).map((r) => ({
      key: r.id,
      id: r.id,
      base: r.base,
      values: Object.fromEntries((r.fields ?? []).map((f) => [f.id, f.value ?? ""])),
    }));
  }

  /**
   * A new row's field shape = **the shape of the existing first one** (every record in the same record set has the same fields).
   * Before this, a new row had no fields to draw: spec was taken from `data.items[i]`, and the new row's index was already past that
   * array - so "add" produced an empty card with only a title and no input box, which is as good as not being able to add.
   * No value is carried here (a new row starts empty), and Options are reused as is (for example a tier's "inherits from" dropdown
   * should list the other tiers).
   */
  function blankFields(): Field[] {
    // Shape takes **the one the contributor explicitly gives** first (`data.blank`). Why leave this opening:
    // the field shape lives on each record, so "what a new one looks like" could previously only be copied from an existing
    // one - and when there is **not a single one**, there is nothing to copy, and clicking "add" gives an empty card with not one input box,
    // and the user has no way to fill anything in. Install state is exactly this (a config that has not
    // configured anything yet), so it is not a corner case.
    const shape = data.blank?.length ? data.blank : data.items?.[0]?.fields;
    return (shape ?? []).map((f) => ({ ...f, value: "" }));
  }

  /**
   * When the backend has **re-read** (`data` got a new object), clear this local cache.
   *
   * The cost of not clearing was measured: delete a row -> save succeeds -> App re-reads globally -> `data` is new, but
   * `removedAccum` still holds that `(id, base)`. So **every next save** submits it
   * again, and the backend sees that file is no longer on disk -> StaleError "someone else changed it, reload and
   * retry". What the user sees is "I deleted extra, and it reports claude-cheap.kv" - an error unrelated to
   * what they just did.
   *
   * Clearing loses nothing: **unsaved changes live in App's `drafts`** (stored by concept id), and the
   * `items`/`removed` derivation chain falls back to it immediately, with no visible change on screen.
   */
  let seen = untrack(() => data);
  $effect(() => {
    if (seen === data) return;
    seen = data;
    edited = null;
    removedAccum = [];
    shown = {};
  });

  function specOf(it: Item): Rec {
    const found = data.items.find((r) => r.id === it.id);
    // A new row (empty id) is not known to the backend yet; fill a blank form using the first record's field shape.
    if (found) return found;
    return { id: "", label: "", fields: blankFields(), removable: false };
  }

  function push(next: Item[]) {
    edited = next;
    onEdit({ items: next, removed });
  }

  function setValue(key: string, field: string, v: string) {
    push(items.map((it) => (it.key === key ? { ...it, values: { ...it.values, [field]: v } } : it)));
  }

  function remove(key: string) {
    const it = items.find((x) => x.key === key);
    // Keep the (id, base) of this deleted row - the backend uses it for a CAS delete, so what someone else just changed on disk is not deleted.
    if (it?.id) removedAccum = [...removedAccum, { id: it.id, base: it.base }];
    push(items.filter((x) => x.key !== key));
  }

  function add() {
    // A new record has no id (the contributor recognizes "this is new" by that), and all fields are empty - the user fills a blank form from scratch.
    push([...items, { key: "new:" + ++seq, id: "", values: {} }]);
  }
</script>

{#if data.file}<div class="row"><span class="mono dim">{data.file}</span></div>{/if}

{#each items as it (it.key)}
  {@const spec = specOf(it)}
  <div class="rec">
    <div class="row head">
      <b>{spec.label || it.values.name || t("new record")}</b>
      <span class="spacer"></span>
      {#if !readonly && spec.removable}
        <button class="tiny ghost" onclick={() => remove(it.key)}>{t("remove")}</button>
      {/if}
    </div>
    {#each spec.fields as f (f.id)}
      <div class="field">
        <label for="f-{it.key}-{f.id}">{f.label}</label>
        {#if f.kind === "select"}
          <select
            id="f-{it.key}-{f.id}"
            value={it.values[f.id] ?? ""}
            disabled={readonly}
            onchange={(e) => setValue(it.key, f.id, e.currentTarget.value)}
          >
            {#each f.options ?? [] as o (o)}<option value={o}>{o || "—"}</option>{/each}
          </select>
        {:else if f.kind === "lines"}
          <textarea
            id="f-{it.key}-{f.id}"
            rows="3"
            placeholder={f.placeholder}
            value={it.values[f.id] ?? ""}
            disabled={readonly}
            oninput={(e) => setValue(it.key, f.id, e.currentTarget.value)}
          ></textarea>
        {:else if f.kind === "secret"}
          <!-- The value is not on this side: this cell is empty when it arrives, and typing is the change (see lib/view's FieldSecret).
               The eye only helps with "the string I just typed/pasted" - which is exactly the most common verification action. -->
          <div class="row">
            <input
              id="f-{it.key}-{f.id}"
              class="mono grow"
              type={shown[it.key + "#" + f.id] ? "text" : "password"}
              autocomplete="off"
              placeholder={f.placeholder}
              value={it.values[f.id] ?? ""}
              disabled={readonly}
              oninput={(e) => setValue(it.key, f.id, e.currentTarget.value)}
            />
            <button
              class="tiny ghost"
              title={shown[it.key + "#" + f.id] ? t("hide") : t("show")}
              onclick={() => (shown[it.key + "#" + f.id] = !shown[it.key + "#" + f.id])}
            >
              {shown[it.key + "#" + f.id] ? "🙈" : "👁"}
            </button>
          </div>
        {:else}
          <input
            id="f-{it.key}-{f.id}"
            class="mono"
            placeholder={f.placeholder}
            value={it.values[f.id] ?? ""}
            disabled={readonly}
            oninput={(e) => setValue(it.key, f.id, e.currentTarget.value)}
          />
        {/if}
        {#if f.why}<div class="dim why">{f.why}</div>{/if}
      </div>
    {/each}
  </div>
{/each}

{#if !items.length}
  <p class="dim">{t("nothing here yet")}</p>
{/if}

{#if !readonly && data.can_add}
  <button onclick={add}>{data.add_label || t("+ add")}</button>
{/if}

<style>
  .rec {
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: 10px 12px;
    margin-bottom: 10px;
  }
  .head { margin-bottom: 6px; }
  .field {
    display: grid;
    grid-template-columns: 160px minmax(0, 1fr);
    gap: 6px 10px;
    align-items: center;
    padding: 3px 0;
  }
  .field label { color: var(--dim); }
  .field input, .field select, .field textarea { width: 100%; }
  .field textarea { font-family: var(--mono); font-size: 12px; resize: vertical; }
  .why { grid-column: 2; font-size: 12px; }
  .grow { flex: 1; }
  @media (max-width: 700px) {
    .field { grid-template-columns: minmax(0, 1fr); }
    .why { grid-column: 1; }
  }
</style>
