<script lang="ts">
  // 代理日志的尾巴（只读）。
  //
  // 跟随靠**轮询**，不是 SSE：这一版的通道是「问一次快照」（见 api.ts），而日志
  // 的尾巴读起来是廉价的（有字节上界，见网关那边的 tailLines）。真到了轮询不够
  // 用的那天再上流——先上流是把一条协议焊在没验证过的用法上。
  //
  // 自动滚到底：看日志的人要的是最后一行。往上翻的时候不打断他（见 onscroll）。
  type Data = { path: string; lines: string[]; truncated?: boolean; missing?: boolean };

  import { t } from "../i18n";

  let { data }: { data: Data } = $props();

  let follow = $state(true);
  // $state：它被下面的 $effect 读，而 bind:this 是在挂载之后才赋值的——不是响应式
  // 的话，滚到底那一步永远看到的是 undefined（第一次加载就停在顶部）。
  let box = $state<HTMLDivElement | undefined>(undefined);

  $effect(() => {
    // 依赖 data.lines 让这段在每次刷新后跑一次（Svelte 的 $effect 会追踪它读到的
    // 响应式值）。
    void data.lines;
    if (follow && box) box.scrollTop = box.scrollHeight;
  });

  function onscroll() {
    if (!box) return;
    // 「贴底」才继续跟：用户往上翻就是在读历史，这时把他拽回底部是最讨厌的一种
    // 交互（他正在看的那一行会跑掉）。
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
    max-height: 340px;
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
