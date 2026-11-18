<script lang="ts">
  // A **slot** in the layout: plugins that declare in the manifest that they fill it each get an empty container.
  //
  // This is the UI's **framework interface** to modules (the same category as Kind and theme tokens): the UI decides "there is
  // a cell on the right of the top bar", and the module decides what goes in that cell. This knows no plugin - adding or removing one,
  // this file needs no change.
  //
  // A broken plugin (import fails, mount throws) only loses its own cell, and says something in the console:
  // the UI must not go blank because one module's frontend is broken (same reasoning as the card's Broken case).
  import { onMount } from "svelte";
  import { pluginManifest, pluginHost } from "./api";

  let { at, lang }: { at: string; lang: string } = $props();
  let box: HTMLDivElement;

  onMount(() => {
    const unmounts: Array<() => void> = [];
    let gone = false;
    void (async () => {
      for (const p of await pluginManifest()) {
        if (gone || !p.slots.includes(at)) continue;
        const el = document.createElement("div");
        el.className = "plugin";
        el.dataset.plugin = p.name;
        box.append(el);
        try {
          const mod = await import(/* @vite-ignore */ `${import.meta.env.BASE_URL}${p.entry}`);
          if (gone) return;
          const off = await mod.mount(el, pluginHost(p.name, lang));
          if (typeof off === "function") unmounts.push(off);
        } catch (err) {
          console.warn(`plugin ${p.name} failed to mount in ${at}:`, err);
          el.remove();
        }
      }
    })();
    return () => {
      gone = true;
      for (const off of unmounts) {
        try {
          off();
        } catch {
          /* A failed unmount should not block page switching */
        }
      }
    };
  });
</script>

<div class="plugin-slot" data-slot={at} bind:this={box}></div>

<style>
  .plugin-slot {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .plugin-slot:empty {
    display: none;
  }
</style>
