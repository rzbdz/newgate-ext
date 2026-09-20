<script lang="ts">
  // 冲突：你手里那份基线已经不是磁盘上那份了（CLI 在你看页面的时候改过同一个
  // 文件）。**两边原文都摆出来**，因为只有当事人能判断该怎么办：
  //
  //   - 你改的是 A 段、他改的是 B 段 → 大部分情况下「保留我的」是对的；
  //   - 你们碰的是同一段 → 得看一眼才知道谁的更新。
  //
  // 「保存失败，请重试」把这件事交给用户去猜，而猜错的代价是丢数据。
  import type { Conflict } from "./api";

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
    <b>{conflict.path} changed on disk</b>
    <span class="pill mono">{conflict.base.slice(0, 14)} → {conflict.current.slice(0, 14)}</span>
    <span class="spacer"></span>
    <button onclick={onTakeTheirs}>use theirs (drop my edit)</button>
    <button class="primary" onclick={onKeepMine}>keep mine</button>
  </div>
  <p class="dim">
    Something else wrote this file after this page loaded it (a CLI command, another
    browser tab, or a program of yours). Nothing has been written — pick one.
  </p>
  <div class="side">
    <div>
      <h4>yours</h4>
      <pre>{conflict.yours}</pre>
    </div>
    <div>
      <h4>on disk now</h4>
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
