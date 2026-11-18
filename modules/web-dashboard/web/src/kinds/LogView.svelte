<script lang="ts">
  // The tail of the proxy log (read-only).
  //
  // Following relies on **polling**, not SSE: this version's channel is "ask for a snapshot" (see api.ts), and reading the log
  // tail is cheap (there is a byte upper bound, see tailLines on the gateway side). When the day comes that polling is not enough,
  // then go streaming - going streaming first welds a protocol onto unverified usage.
  //
  // Auto-scroll to the bottom: someone looking at logs wants the last line. Do not interrupt them when they scroll up (see onscroll).
  type Data = { path: string; lines: string[]; truncated?: boolean; missing?: boolean };

  import { t } from "../i18n";

  let { data }: { data: Data } = $props();

  let follow = $state(true);
  // $state: it is read by the $effect below, and bind:this is only assigned after mounting - if it were not reactive,
  // the scroll-to-bottom step would always see undefined (stopping at the top on first load).
  let box = $state<HTMLDivElement | undefined>(undefined);

  $effect(() => {
    // Depend on data.lines so this runs once per refresh (Svelte's $effect tracks the reactive
    // values it reads).
    void data.lines;
    if (follow && box) box.scrollTop = box.scrollHeight;
  });

  function onscroll() {
    if (!box) return;
    // Only keep following when "stuck to the bottom": scrolling up means the user is reading history, and dragging them back to the bottom is the most annoying kind of
    // interaction (the line they were looking at runs away).
    follow = box.scrollHeight - box.scrollTop - box.clientHeight < 24;
  }
</script>

<div class="row head">
  <span class="mono dim">{data.path}</span>
  {#if data.truncated}
    <span class="pill" title={t("only the tail is shown")}>{t("tail")}</span>
  {/if}
  <span class="spacer"></span>
  <label class="dim tiny"><input type="checkbox" bind:checked={follow} /> {t("follow")}</label>
</div>

{#if data.missing}
  <p class="dim">{t("no log file yet — this daemon has not written anything.")}</p>
{:else}
  <div class="log" bind:this={box} {onscroll}>
    {#each data.lines ?? [] as line, i (i)}
      <div class="line" class:err={/\b(4\d\d|5\d\d)\b/.test(line)}>{line}</div>
    {/each}
  </div>
{/if}

<style>
  .head { margin-bottom: 6px; }
  .log {
    font-family: var(--mono);
    font-size: 11.5px;
    line-height: 1.45;
    /* Take everything left in the card (the card fills the window itself, see .card.fills in app.css): one more line of log
       seen is one more line. min-height: 0 is what makes it **scroll itself** instead of pushing the card taller. */
    flex: 1;
    min-height: 0;
    overflow: auto;
    background: var(--bg);
    border: 1px solid var(--line);
    border-radius: 8px;
    padding: 6px 8px;
  }
  .line { white-space: pre-wrap; word-break: break-all; }
  .line.err { color: var(--warn); }
  label.tiny { display: flex; align-items: center; gap: 4px; font-size: 11px; }
</style>
