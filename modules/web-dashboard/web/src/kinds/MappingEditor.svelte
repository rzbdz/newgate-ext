<script lang="ts">
  // Tier <-> candidate binding editor. **Order is the fallback order**, so each row has up/down on the left
  // to move it - order is the only semantics here, and without a prominent place for it the user would think it is list decoration.
  //
  // The edit shape produced is defined by the contributor (applyProfileRoles in
  // core/modules/config/view.go): `{"roles": {"<tier>": [{"provider","model"} | {"ref"}]}}`.
  // One key point: **submit the whole roles at once**, not an increment - the backend is "replace the roles key".
  import { untrack } from "svelte";
  import { t } from "../i18n";
  import ModelPicker from "../ModelPicker.svelte";

  type Binding = { provider?: string; model?: string; ref?: string };
  type Role = { id: string; bindings: Binding[] };
  type Provider = {
    name: string;
    protocol?: string;
    base_url?: string;
    key_env?: string;
    has_key: boolean;
    models: string[];
  };
  type Data = {
    profile: string;
    file: string;
    base: string;
    description?: string;
    default: boolean;
    pinned?: boolean;
    excluded?: boolean;
    extends?: string;
    /** Optional parent tier (another tier file name; the first entry is an empty string = no inheritance). Given by the backend: the UI
        does not know "which tier files exist", it only knows what this cell's dropdown should list. */
    extends_options?: string[];
    roles: Role[];
    providers: Provider[];
  };
  type Roles = Record<string, Binding[]>;

    // What this card submits is **the whole file**: the tier table + name + inheritance. All three belong to one .kv, and split
  // into two cards they would "you say yours, I say mine" (before 2026-09-20 that is exactly how it was: another "tier file"
  // card handled names and inheritance, and the files it listed correspond one to one with the cards here - the user's own words were "that is redundant").
  type Payload = { roles: Roles; name: string; extends: string };

  let {
    data,
    draft,
    readonly,
    onEdit,
    onDeleteFile,
  }: {
    data: Data;
    draft: unknown;
    readonly: boolean;
    onEdit: (v: unknown) => void;
    // Delete **takes effect immediately** (no "change then click save"): it changes the file on disk itself, and making the user
    // click delete once and then save once is a useless ritual attached to an irreversible action.
    // It acts on **this card's own** file, so no argument is needed.
    //
    // "Create" is not here (see the comment in the template below): it belongs to **this section's toolbar**.
    onDeleteFile?: () => void;
  } = $props();

  // Local edit (null = not touched yet). **Why not "copy a snapshot on mount"**: the copy only takes
  // a value at the moment of mounting, and afterwards when props change (the user picked "use the disk copy" in a conflict, or
  // clicked reload) it is still the old one - so the UI shows a tier table that no longer exists on disk.
  //
  // So what is stored here is "have I edited this": if not, follow props; if so, local wins.
  let edited = $state<Payload | null>(null);
  const cur = $derived<Payload>(
    edited ??
      (draft as Payload | undefined) ?? {
        roles: toRoles(data.roles),
        name: data.profile,
        extends: data.extends ?? "",
      },
  );
  const roles = $derived(cur.roles);
  let newRole = $state("");

  // When the backend has re-read (data got a new object), clear the local cache and fall back to draft/props.
  //
  // Why it must be cleared: this cache takes priority over draft and props, and **after one successful save App re-reads
  // globally** - without clearing, the UI still shows the version from before the save, and the next save writes it back, overwriting
  // what just landed elsewhere (the same comment in Records.svelte records the symptom measured).
  // Clearing loses nothing: unsaved changes live in App's `drafts` (stored by concept id), and it falls back to them.
  let seen = untrack(() => data);
  $effect(() => {
    if (seen === data) return;
    seen = data;
    edited = null;
  });

  function toRoles(list: Role[]): Roles {
    const out: Roles = {};
    for (const r of list ?? []) out[r.id] = (r.bindings ?? []).map((b) => ({ ...b }));
    return out;
  }

  // Every change: copy one -> change the copy -> submit it. **Do not change cur in place**: that may be changing
  // the object in props (Svelte's reactivity cannot see other people's objects), and the UI would not move after the change, and the user would think
  // they missed the click.
  function push(mutate: (p: Payload) => void) {
    const next = JSON.parse(JSON.stringify(cur)) as Payload;
    mutate(next);
    edited = next;
    onEdit(JSON.parse(JSON.stringify(next)));
  }

  function models(provider: string): string[] {
    return data.providers.find((p) => p.name === provider)?.models ?? [];
  }

  // A new candidate's initial value takes **the first provider** (in the vast majority of cases that is the one the user wants), but
  // **leave it empty when there is no provider** - writing `provider: ""` gives a binding like `/model-name`,
  // which resolves to nothing while looking quite plausible in the file (2026-09-20 scene: a profile's
  // tier became `mid=/gemini-...`). The backend now rejects such bindings too; this is to keep it from happening.
  function addCandidate(id: string) {
    const first = data.providers[0];
    push((p) => {
      p.roles[id].push(
        first ? { provider: first.name, model: first.models?.[0] ?? "" } : {},
      );
    });
  }
  function removeCandidate(id: string, i: number) {
    push((p) => p.roles[id].splice(i, 1));
  }
  function move(id: string, i: number, by: number) {
    push((p) => {
      const list = p.roles[id];
      const j = i + by;
      if (j < 0 || j >= list.length) return;
      [list[i], list[j]] = [list[j], list[i]];
    });
  }
  function addRole() {
    const id = newRole.trim();
    if (!id) return;
    push((p) => {
      if (!p.roles[id]) p.roles[id] = [];
    });
    newRole = "";
  }
  function removeRole(id: string) {
    push((p) => {
      delete p.roles[id];
    });
  }
  // A reference and "provider+model" are mutually exclusive (see domain.Binding): a reference means "follow that tier",
  // and keeping provider at the same time would only give the resolution path two interpretations.
  function setRef(id: string, i: number, ref: string) {
    if (ref) push((p) => { p.roles[id][i] = { ref }; });
    else {
      const first = data.providers[0];
      push((p) => {
        p.roles[id][i] = first
          ? { provider: first.name, model: first.models?.[0] ?? "" }
          : {};
      });
    }
  }
</script>

<div class="head">
  {#if !readonly}
    <!-- Name and inheritance sit in the card header: they and the tier table below belong to **this one file**. After editing, click save
         and they land together (same as the tier table: one CAS writes one file). -->
    <label class="meta">
      {t("name")}
      <input
        class="mono name"
        value={cur.name}
        disabled={readonly}
        oninput={(e) => push((p) => { p.name = e.currentTarget.value; })}
      />
    </label>
    <label class="meta">
      {t("extends")}
      <select
        value={cur.extends}
        disabled={readonly}
        onchange={(e) => push((p) => { p.extends = e.currentTarget.value; })}
      >
        {#each data.extends_options ?? [""] as o (o)}<option value={o}>{o || "—"}</option>{/each}
      </select>
    </label>
  {:else}
    <span class="mono dim">{data.profile}</span>
    {#if data.extends}<span class="pill">{t("extends {name}", { name: data.extends })}</span>{/if}
  {/if}
  {#if data.default}<span class="pill">{t("default")}</span>{/if}
  {#if data.pinned}<span class="pill">{t("pinned")}</span>{/if}
  {#if data.excluded}<span class="pill">{t("excluded")}</span>{/if}
  <span class="spacer"></span>
  <span class="mono dim file">{data.file}</span>
  <!-- "Create another" **is not on this card**: a newly created one has no concept yet, and no card can
       host it. It hangs on **this section's toolbar** (see .secbar in App.svelte and
       Section.Actions in core/lib/view), which is always reachable. -->
  {#if !readonly && onDeleteFile}
    <button
      class="tiny ghost danger"
      onclick={() => {
        if (confirm(t("Delete profile “{name}” ({file})? This removes the file.", {
          name: cur.name, file: data.file,
        }))) onDeleteFile();
      }}
    >{t("delete this file")}</button>
  {/if}
</div>

{#each Object.keys(roles) as id (id)}
  <div class="role">
    <div class="row">
      <b class="mono">{id}</b>
      <!-- An empty tier means "not written": inherit from the parent / fall down the ladder (after the 2026-09-20 fix it is equivalent to not written).
           Do not shout "resolves to nothing" - that looks like a hard error, but in fact it just falls back to inheritance. -->
      {#if !roles[id].length}<span class="dim">{t("no candidates — inherits or falls back")}</span>{/if}
      <span class="spacer"></span>
      {#if !readonly}
        <button class="tiny" onclick={() => addCandidate(id)}>{t("+ candidate")}</button>
        <button
          class="tiny ghost"
          onclick={() => removeRole(id)}
          title={t("remove this tier from the file")}
        >{t("remove tier")}</button>
      {/if}
    </div>
    {#each roles[id] as b, i (i)}
      <div class="row cand">
        <span class="idx mono dim">{i + 1}</span>
        {#if b.ref !== undefined}
          <input
            class="mono"
            value={b.ref}
            placeholder={t("another tier id")}
            disabled={readonly}
            oninput={(e) => setRef(id, i, e.currentTarget.value)}
          />
        {:else}
          <select
            value={b.provider ?? ""}
            disabled={readonly}
            onchange={(e) => {
              const prov = e.currentTarget.value;
              push((p) => {
                p.roles[id][i].provider = prov;
                p.roles[id][i].model = models(prov)[0] ?? p.roles[id][i].model ?? "";
              });
            }}
          >
            <option value="">{t("— provider —")}</option>
            {#each data.providers as p (p.name)}
              <option value={p.name}>{p.name}{p.has_key ? "" : t(" (no key)")}</option>
            {/each}
            {#if b.provider && !data.providers.some((p) => p.name === b.provider)}
              <option value={b.provider}>{b.provider}{t(" (not declared)")}</option>
            {/if}
          </select>
          <ModelPicker
            provider={b.provider ?? ""}
            declared={models(b.provider ?? "")}
            value={b.model ?? ""}
            {readonly}
            onchange={(m) => {
              // The picker **also** handles "hand-typing a model the upstream did not list": what it returns is
              // the string in the input box. So there is only one write path here, with no "selected" versus "typed"
              // two semantics to distinguish.
              push((p) => { p.roles[id][i].model = m; });
            }}
          />
        {/if}
        <span class="spacer"></span>
        {#if !readonly}
          <label class="dim tiny">
            {t("ref")}
            <input
              type="checkbox"
              checked={b.ref !== undefined}
              onchange={(e) => setRef(id, i, e.currentTarget.checked ? id : "")}
            />
          </label>
          <button class="tiny ghost" onclick={() => move(id, i, -1)} disabled={i === 0}>↑</button>
          <button class="tiny ghost" onclick={() => move(id, i, 1)} disabled={i === roles[id].length - 1}>↓</button>
          <button class="tiny ghost" onclick={() => removeCandidate(id, i)}>×</button>
        {/if}
      </div>
    {/each}
  </div>
{/each}

{#if !readonly}
  <div class="row">
    <input
      placeholder={t("add a tier (e.g. vision)")}
      bind:value={newRole}
      onkeydown={(e) => e.key === "Enter" && addRole()}
    />
    <button class="tiny" onclick={addRole} disabled={!newRole.trim()}>{t("add tier")}</button>
  </div>
{/if}

<style>
  .head { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-bottom: 10px; }
  .role { border-top: 1px dashed var(--line); padding: 8px 0; }
  .role:first-of-type { border-top: 0; }
  .cand { padding: 2px 0 2px 6px; }
  .cand select { min-width: 150px; }
  .idx { width: 14px; }
  .danger { color: var(--danger, #c0392b); }
  label.tiny { display: flex; align-items: center; gap: 4px; font-size: 11px; }
  /* Name and inheritance come first in the card (they are this file's identity), and the file path retreats to the right end as a footnote. */
  .meta { display: flex; align-items: center; gap: 6px; font-size: 11px; color: var(--dim); }
  .meta .name { min-width: 130px; }
  .file { font-size: 11px; }
</style>
