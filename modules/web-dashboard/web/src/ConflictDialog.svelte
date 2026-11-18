<script lang="ts">
  // Conflict: the baseline you hold is no longer the one on disk (the CLI changed the same
  // file while you were looking at the page). **Both sides' original text are laid out**, because only the person involved can judge what to do:
  //
  //   - you changed section A, they changed section B -> "keep mine" is right in most cases;
  //   - you both touched the same section -> you have to look before knowing whose is newer.
  //
  // "Save failed, please retry" leaves this to the user to guess, and guessing wrong costs data.
  import type { Conflict } from "./api";
  import { t } from "./i18n";

  let {
    conflict,
    onKeepMine,
    onTakeTheirs,
  }: {
    conflict: Conflict;
    onKeepMine: () => void;
    onTakeTheirs: () => void;
  } = $props();
</script>

<div class="banner conflict">
  <div class="row">
    <b>{t("{path} changed on disk", { path: conflict.path })}</b>
    <span class="pill mono">{conflict.base.slice(0, 14)} → {conflict.current.slice(0, 14)}</span>
    <span class="spacer"></span>
    <button onclick={onTakeTheirs}>{t("use theirs (drop my edit)")}</button>
    <button class="primary" onclick={onKeepMine}>{t("keep mine")}</button>
  </div>
  <p class="dim">
    {t(
      "Something else wrote this file after this page loaded it (a CLI command, another browser tab, or a program of yours). Nothing has been written — pick one.",
    )}
  </p>
  <div class="side">
    <div>
      <h4>{t("yours")}</h4>
      <pre>{conflict.yours}</pre>
    </div>
    <div>
      <h4>{t("on disk now")}</h4>
      <pre>{conflict.theirs}</pre>
    </div>
  </div>
</div>

<style>
  .conflict { border-color: var(--warn); background: color-mix(in srgb, var(--warn) 10%, transparent); }
  .side { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; margin-top: 8px; }
  .side h4 { margin: 0 0 4px; font-size: 11px; text-transform: uppercase; color: var(--dim); }
  .side pre {
    font-family: var(--mono);
    font-size: 11px;
    max-height: 260px;
    overflow: auto;
    background: var(--bg);
    border: 1px solid var(--line);
    border-radius: 8px;
    padding: 8px;
    margin: 0;
  }
</style>
