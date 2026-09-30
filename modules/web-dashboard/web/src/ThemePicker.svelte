<script lang="ts">
  // 皮肤切换：**一个图标按钮**，点了才出选单。
  //
  // # 为什么不是一个下拉框
  //
  // 之前它是 `<select>`。下拉框是一条**常驻的**控件：一栏宽度、一个方框、一个箭头，
  // 每时每刻都占着顶栏——而它一年也点不了几次（皮肤是装完挑一次就完了的东西）。
  // 用户的原话是「主题应该做一个按钮就行了，点击了才 popup 选单，默认不要做选单」，
  // 以及「尽量减少不常用的控件按钮」。
  //
  // # 为什么这个组件有两处调用者
  //
  // 产品界面（App.svelte）与演示页（demo/DemoShell.svelte）各有一个壳，之前那里
  // 各写一遍下拉——而两份实现已经在别处漂过一次（侧栏的排列规则）。选单这种东西
  // 「长什么样」是纯粹的观感，两处必须逐字一样，所以抽成一份。
  import { t } from "./i18n";
  import type { Themes } from "./api";

  let {
    doc,
    onPick,
  }: {
    doc: Themes;
    onPick: (id: string) => void;
  } = $props();

  let open = $state(false);
  let box = $state<HTMLElement | null>(null);

  /**
   * 点别处就收起来。
   *
   * 只在展开时挂监听（收起时不留一个每点一下都要跑一遍的处理器）。`pointerdown`
   * 而不是 `click`：它在**按下**时就判，于是「按住拖到选单外面再松开」不会把选单
   * 关掉又立刻被按钮那一下重新打开。
   */
  $effect(() => {
    if (!open) return;
    const away = (e: PointerEvent) => {
      if (!box?.contains(e.target as Node)) open = false;
    };
    const esc = (e: KeyboardEvent) => {
      if (e.key === "Escape") open = false;
    };
    document.addEventListener("pointerdown", away);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("pointerdown", away);
      document.removeEventListener("keydown", esc);
    };
  });

  /** 现在这一套的名字（空 id = 出厂那套，它不在 doc.themes 里）。 */
  const current = $derived(
    doc.themes.find((x) => x.id === doc.active)?.name ?? t("follow the system"),
  );
</script>

<div class="wrap" bind:this={box}>
  <!-- 记号是**对比度**那个（一个圆、右半边实心）：它是「深/浅」这件事的通用画法，
      不指任何一家品牌，四个选项下都说得通。 -->
  <button
    class="icon"
    aria-haspopup="listbox"
    aria-expanded={open}
    aria-label={t("theme")}
    title={`${t("theme")} · ${current}`}
    onclick={() => (open = !open)}
  >
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="8" cy="8" r="5.9" />
      <path d="M8 2.1a5.9 5.9 0 0 1 0 11.8z" />
    </svg>
  </button>

  {#if open}
    <div class="menu" role="listbox" aria-label={t("theme")}>
      {#each [{ id: "", name: t("follow the system") }, ...doc.themes] as o (o.id)}
        <button
          class="opt"
          role="option"
          aria-selected={doc.active === o.id}
          onclick={() => {
            onPick(o.id);
            open = false;
          }}
        >
          <span class="tick" class:on={doc.active === o.id}>✓</span>
          <span class="nm">{o.name}</span>
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .wrap { position: relative; }

  /* 图标按钮：与顶栏别的小按钮同高，但只有一格宽。 */
  .icon {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 26px;
    padding: 0;
    background: transparent;
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    color: var(--dim);
    cursor: pointer;
  }
  .icon:hover { color: var(--ink); border-color: color-mix(in srgb, var(--ink) 35%, var(--line)); }
  .icon svg {
    width: 15px;
    height: 15px;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.3;
  }
  .icon svg path { fill: currentColor; stroke: none; }

  /* 选单：**右对齐**挂在按钮下面（顶栏右边的东西，往左展开才不会顶出屏幕）。 */
  .menu {
    position: absolute;
    top: calc(100% + 5px);
    right: 0;
    z-index: 40;
    min-width: 150px;
    padding: 4px;
    background: var(--panel);
    border: 1px solid var(--line);
    border-radius: var(--radius-sm);
    box-shadow: 0 8px 22px rgb(0 0 0 / 26%);
  }
  .opt {
    display: flex;
    align-items: center;
    gap: 7px;
    width: 100%;
    padding: 5px 8px;
    background: transparent;
    border: 0;
    border-radius: 5px;
    color: var(--ink);
    font-size: 12px;
    text-align: left;
    cursor: pointer;
  }
  .opt:hover { background: var(--panel-2); }
  /* 选中那个用 **对勾** 而不是把整行反白：反白在深色下像「鼠标正停在这」，
     而对勾说的是「当前是它」——两件事在这一格上会同时成立。 */
  .tick {
    width: 10px;
    color: var(--accent);
    opacity: 0;
    font-size: 11px;
  }
  .tick.on { opacity: 1; }
  .nm { flex: 1; }
</style>
