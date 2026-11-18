<script lang="ts">
  // Counters (read-only). The groups and "what does this number mean" for each row are **given by the contributor**
  // (on the gateway side it is metrics.Group / metrics.Hint, the same as `newgate metrics`),
  // so what is seen on the two UIs does not disagree.
  //
  // Bar length is normalized to the max within the group: comparing absolute numbers across groups is meaningless (different groups differ by orders of magnitude),
  // and seeing at a glance who is growing within a group is what matters.
  type Entry = { name: string; value: number; hint?: string };
  type Group = { id: string; label: string; counters: Entry[] };
  type Data = { groups: Group[]; total: number };

  import { t } from "../i18n";

  let { data }: { data: Data } = $props();

  function max(group: Group): number {
    return Math.max(1, ...(group.counters ?? []).map((c) => c.value));
  }
</script>

<div class="row">
  <span class="dim">{t("total {n}", { n: data.total ?? 0 })}</span>
  <span class="spacer"></span>
  <span class="dim">{t("counters are process-local — they reset when the daemon restarts")}</span>
</div>

{#each data.groups ?? [] as g (g.id)}
  <div class="group">
    <div class="row"><b>{g.label}</b><span class="dim mono">{g.id}</span></div>
    {#each g.counters ?? [] as c (c.name)}
      <div class="row counter" title={c.hint ?? ""}>
        <span class="mono name">{c.name}</span>
        <span class="bar"><i style="width:{(c.value / max(g)) * 100}%"></i></span>
        <span class="mono val">{c.value}</span>
      </div>
      {#if c.hint}<div class="dim hint">{c.hint}</div>{/if}
    {/each}
  </div>
{/each}

{#if !(data.groups ?? []).length}
  <p class="dim">{t("no counters yet — nothing has gone through the gateway since it started.")}</p>
{/if}

<style>
  .group { border-top: 1px dashed var(--line); padding: 8px 0; }
  .group:first-of-type { border-top: 0; }
  .counter { gap: 10px; }
  .name { min-width: 230px; font-size: 12px; }
  .val { min-width: 56px; text-align: right; }
  .bar { flex: 1; height: 7px; background: var(--panel-2); border-radius: 99px; overflow: hidden; }
  .bar i { display: block; height: 100%; background: var(--accent); }
  .hint { font-size: 11px; margin-left: 240px; }
</style>
