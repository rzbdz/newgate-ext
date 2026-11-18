<script lang="ts">
  // A group of switches / pickers. Every item carries a **why** (the contributor's `why`) - this is the place in this
  // UI that saves the most time: no need to dig through docs to know what a switch does.
  import { untrack } from "svelte";

  //
  // Changes are made in place (picking a dropdown is one edit), and land only on save. No modal, no collapsed-by-default:
  // the meaning of a popup is "interrupt", and there is no decision here that needs interrupting.
  type Item = {
    id: string;
    label: string;
    kind: "select" | "bool" | "text";
    value: string | boolean;
    options?: string[];
    /** Value -> the wording for people to read (see OptionLabels in lib/view). Missing ones show the value itself. */
    option_labels?: Record<string, string>;
    /** The hint in an empty cell ("empty = loopback only" and the like). The empty-value cell relies on it to say what empty means. */
    placeholder?: string;
    why?: string;
    /** The group (module name and the like). There can be several groups in one list - see the group subheadings below. */
    group?: string;
    /** Danger level (safe / quirk / footgun). It only affects display: footgun is put by the contributor into
        another read-only card, and does not appear here. */
    danger?: string;
  };
  type Data = { file: string; base: string; items: Item[] };

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

  // Same as MappingEditor: store "have I edited this", and if not, follow props - otherwise
  // after reload the UI still shows the previous value (see the comment there).
  let edited = $state<Record<string, unknown> | null>(null);
  const values = $derived(
    edited ?? (draft as Record<string, unknown>) ?? fromItems(data.items),
  );

  function fromItems(items: Item[]): Record<string, unknown> {
    const out: Record<string, unknown> = {};
    for (const it of items ?? []) out[it.id] = it.value;
    return out;
  }

  // Submit **all** items (not only the changed one): the contributor takes the fields it recognizes by id and ignores extra
  // keys, but a missing key is treated as never written - which would become "change one switch, another gets reset".
  function set(id: string, v: unknown) {
    edited = { ...values, [id]: v };
    onEdit({ ...edited });
  }

  // When the backend has re-read (data got a new object), clear the local cache and fall back to draft/props - otherwise this
  // old value would overwrite **the new value already landed elsewhere** on the next save (see the same comment in
  // Records.svelte: the consequence of not clearing there was deletes being submitted repeatedly).
  let seen = untrack(() => data);
  $effect(() => {
    if (seen === data) return;
    seen = data;
    edited = null;
  });
</script>

<div class="row"><span class="mono dim">{data.file}</span></div>

{#each data.items ?? [] as it, i (it.id)}
  {#if it.group && it.group !== (data.items[i - 1]?.group)}
    <div class="group">{it.group}</div>
  {/if}
  <div class="item">
    <div class="row">
      <b>{it.label}</b>
      {#if it.danger && it.danger !== "safe"}
        <span class="pill" title="what turning this off costs">{it.danger}</span>
      {/if}
      <span class="spacer"></span>
      {#if it.kind === "bool"}
        <!-- A switch is a switch: a box with a check reads like a "form item", while what is expressed here is
             "is this capability on right now" - a slidable track is understood at a glance, and you do not have to count
             the difference between checked and unchecked. The input is still a checkbox: screen readers and the keyboard (space)
             rely on it, and the styling only draws it as a switch. -->
        <label class="sw">
          <input
            type="checkbox"
            role="switch"
            checked={values[it.id] === true}
            disabled={readonly}
            onchange={(e) => set(it.id, e.currentTarget.checked)}
          />
          <span class="track"><span class="knob"></span></span>
        </label>
      {:else if it.kind === "select"}
        <select
          value={String(values[it.id] ?? "")}
          disabled={readonly}
          onchange={(e) => set(it.id, e.currentTarget.value)}
        >
          <!-- Empty value = semantics like "follow the default", and it needs a visible name: an empty <option>
               is an option with no text in the UI, and the user does not know what it is.
               The wording prefers what the contributor gives (`option_labels`): for some values only that one
               knows the meaning - the chain head's empty value is "follow the global default", and "who is the default" must be filled in by the backend
               (the two characters "-" cannot say this, nor distinguish it from "I want ds specifically"). -->
          {#each it.options ?? [] as o (o)}<option value={o}>{it.option_labels?.[o] ?? (o || "—")}</option>{/each}
          {#if values[it.id] !== undefined && !(it.options ?? []).includes(String(values[it.id]))}
            <option value={String(values[it.id])}>{String(values[it.id])} (not in the list)</option>
          {/if}
        </select>
      {:else}
        <input
          class="mono"
          value={String(values[it.id] ?? "")}
          placeholder={it.placeholder}
          disabled={readonly}
          oninput={(e) => set(it.id, e.currentTarget.value)}
        />
      {/if}
    </div>
    {#if it.why}<div class="dim why">{it.why}</div>{/if}
  </div>
{/each}

<style>
  .group {
    margin: 12px 0 4px;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.6px;
    color: var(--dim);
  }
  .group:first-child { margin-top: 4px; }
  .item { padding: 7px 0; border-top: 1px dashed var(--line); }
  .item:first-of-type { border-top: 0; }
  .why { font-size: 12px; margin-top: 2px; }
  /* Switch: the real input is hidden under the track (keyboard and screen readers work as usual), and what is visible is .track/.knob.
     Use :focus-visible to give keyboard users a focus ring - once the input is hidden, the system's default
     focus box disappears with it, and not restoring it means only the mouse can be used. */
  .sw { display: inline-flex; cursor: pointer; }
  .sw input {
    position: absolute;
    width: 1px;
    height: 1px;
    opacity: 0;
    margin: 0;
  }
  .track {
    width: 34px;
    height: 18px;
    border-radius: 9px;
    background: var(--line);
    display: inline-block;
    position: relative;
    transition: background 0.15s;
  }
  .knob {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 14px;
    height: 14px;
    border-radius: 50%;
    background: var(--ink);
    transition: transform 0.15s;
  }
  .sw input:checked + .track { background: var(--ok); }
  .sw input:checked + .track .knob { transform: translateX(16px); }
  .sw input:focus-visible + .track { outline: 2px solid var(--accent); outline-offset: 2px; }
  .sw input:disabled + .track { opacity: 0.5; cursor: not-allowed; }
</style>
