<script lang="ts">
  // 模型选择器：一个**可输入的下拉**，数据来自 provider 自己（而不是 providers.json）。
  //
  // # 为什么不是一个 datalist
  //
  // 改造前这一格是 `<input list=…>`：datalist 只给「声明的模型」，而那份清单要
  // 用户手写；上游新出一个模型，用户得先去 provider 卡片里补一行，再回来选。而且
  // datalist 没有分组、没有模糊搜索、也不说「上游到底回没回话」。
  //
  // 这里在**打开的那一刻**去问一次（见 api.ts 的 modelList → BFF 的
  // /api/ext/modellist → 发行版的 modules/modellist）：
  //   - 上游支持列表 → 直接列出它此刻真正提供的模型（带分组），支持模糊搜索；
  //   - 上游不支持 / key 不允许 / 网络不通 → **回落到 providers.json 里声明的
  //     清单**，并把原因写在菜单顶上。这不是故障：多数上游没有这个接口。
  //
  // # 为什么是 fixed 定位
  //
  // 卡片 `.card` 有 `overflow: hidden`（见 app.css）。绝对定位的菜单会被卡片裁掉，
  // 于是在最需要它的地方（一屏十几张档位卡）只剩一条缝。fixed 到视口、按输入框
  // 的位置算坐标，再把宽度夹进视口——滚动/缩放时直接关掉菜单（见 closeOnViewport）。
  import { untrack } from "svelte";
  import { t } from "./i18n";
  import { modelList, type ModelInfo, type ModelList } from "./api";
  import { fuzzyFilter } from "./fuzzy";

  let {
    value = "",
    provider = "",
    declared = [],
    readonly = false,
    onchange,
  }: {
    value?: string;
    provider?: string;
    /** providers.json 里声明的模型名：问不到上游时就用这一份。 */
    declared?: string[];
    readonly?: boolean;
    onchange: (model: string) => void;
  } = $props();

  let open = $state(false);
  /** 输入框里正在打的那一串。**打开时清空**，菜单才会列出全部模型。 */
  let query = $state(untrack(() => value));
  let active = $state(0);
  let loading = $state(false);
  let loadError = $state("");
  let catalogs = $state<Record<string, ModelList>>({});

  let input: HTMLInputElement | undefined = $state();
  let menuEl: HTMLDivElement | undefined = $state();
  let pos = $state({ left: 0, top: 0, width: 260, maxHeight: 320 });
  // 菜单的 id 要唯一：一屏里同时有好几个候选行，每个都有自己的选择器。
  const menuID = $props.id();

  // 外部改了值（换 provider、撤销草稿、载入另一份 profile）时同步输入框——
  // 但**打开的时候不抢**：那正是用户在打字的时候。
  let seenValue = untrack(() => value);
  $effect(() => {
    if (seenValue === value) return;
    seenValue = value;
    if (!open) query = value;
  });

  // 换 provider = 换了一份清单：菜单与本地查询都重来（缓存按 provider 存着，
  // 切回来还在）。
  let seenProvider = untrack(() => provider);
  $effect(() => {
    if (seenProvider === provider) return;
    seenProvider = provider;
    open = false;
    loadError = "";
    query = value;
  });

  const catalog = $derived(provider ? catalogs[provider] : undefined);

  /** 菜单里要列的东西：上游说得出就用上游的，否则用声明的那份。 */
  const models = $derived<ModelInfo[]>(
    catalog?.supported && (catalog.models?.length ?? 0) > 0
      ? catalog.models
      : declared.map((id) => ({ id })),
  );

  const filtered = $derived(fuzzyFilter(query, models, (m) => [m.id, m.name, m.group]));

  const status = $derived.by(() => {
    if (loadError) return loadError;
    if (loading) return t("asking the provider for its models…");
    if (!catalog) {
      return provider
        ? t("declared in providers.json — open the list to ask the provider")
        : t("pick a provider first");
    }
    if (catalog.supported && (catalog.models?.length ?? 0) > 0) {
      let s = t("{n} models from the provider", { n: catalog.models.length });
      if (catalog.cached) s += " · " + t("cached");
      if (catalog.adapter) s += " · " + catalog.adapter;
      return s;
    }
    return catalog.note || t("the provider did not list its models — showing the declared list");
  });

  /**
   * 分组：同一个组的模型连在一起，组的先后 = 它最好的那个命中。
   *
   * 没有组的时候就是一个空组（菜单不画标题）——「上游没给分组」与「所有模型都在
   * 同一组」在界面上本来就该长得一样。
   */
  const grouped = $derived.by(() => {
    const map = new Map<string, { m: ModelInfo; i: number }[]>();
    filtered.forEach((m, i) => {
      const g = m.group ?? "";
      const hit = map.get(g);
      if (hit) hit.push({ m, i });
      else map.set(g, [{ m, i }]);
    });
    let out = [...map.entries()].map(([group, items]) => ({ group, items }));
    if (query.trim()) {
      // fuzzyFilter 已按分数排好；组按各自第一个命中的名次排，组内保持分数序。
      out = out.sort((a, b) => a.items[0].i - b.items[0].i);
    }
    return out;
  });

  async function load(refresh: boolean) {
    // 演示页没有后端（见 src/env.d.ts）：照常列声明清单，不去问一个不存在的端点。
    if (__NEWGATE_DEMO__) return;
    if (!provider || readonly) return;
    const p = provider;
    loading = true;
    loadError = "";
    try {
      const res = await modelList(p, refresh);
      catalogs = { ...catalogs, [p]: res };
    } catch (e) {
      loadError = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  function place() {
    if (!input) return;
    const r = input.getBoundingClientRect();
    const gap = 4;
    const maxW = Math.max(220, Math.min(560, window.innerWidth - 16));
    const width = Math.min(Math.max(r.width, 260), maxW);
    let left = Math.min(r.left, window.innerWidth - width - 8);
    left = Math.max(8, left);
    let top = r.bottom + gap;
    let maxHeight = Math.min(320, window.innerHeight - top - 8);
    if (maxHeight < 140) {
      // 下面放不下：翻到上面去。
      maxHeight = Math.min(320, r.top - gap - 8);
      top = Math.max(8, r.top - gap - maxHeight);
    }
    pos = { left, top, width, maxHeight };
  }

  function openMenu() {
    if (readonly) return;
    place();
    if (!open) {
      open = true;
      active = 0;
      // 打开时清空查询：用户想看的是**全部**，而不是「只匹配当前这个值」。
      // 当前值仍然在 placeholder 里（见模板），不会看起来像丢了。
      // 已经打开时不清——那会把用户正打着的那串字擦掉。
      query = "";
      if (provider && !catalogs[provider] && !loading) void load(false);
    }
  }

  function typed(e: Event) {
    if (!open) {
      place();
      open = true;
    }
    query = (e.currentTarget as HTMLInputElement).value;
    onchange(query);
    active = 0;
  }

  function pick(m: ModelInfo) {
    onchange(m.id);
    query = m.id;
    open = false;
  }

  function close() {
    open = false;
    query = value;
  }

  function refresh() {
    if (loading) return;
    place();
    open = true;
    void load(true);
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      const wasOpen = open;
      if (!wasOpen) openMenu();
      if (filtered.length) active = wasOpen ? Math.min(active + 1, filtered.length - 1) : 0;
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      if (filtered.length) active = Math.max(active - 1, 0);
    } else if (e.key === "Enter") {
      if (open && filtered[active]) {
        e.preventDefault();
        pick(filtered[active]);
      } else {
        close();
      }
    } else if (e.key === "Escape") {
      e.preventDefault();
      close();
      input?.blur();
    } else if (e.key === "Tab") {
      close();
    }
  }

  // 菜单是 fixed 的：视口一动，它就会悬在一个不再对应输入框的位置上。
  // 直接关掉比追着重新定位简单，也比「看起来还开着、点下去选错行」安全。
  //
  // 用**捕获**监听：卡片住在一个 `overflow: auto` 的窗格里，那一层滚动不会
  // 冒泡到 window。菜单自己的滚动要放过——那是用户在翻列表。
  $effect(() => {
    if (!open) return;
    const onScroll = (e: Event) => {
      if (menuEl && e.target instanceof Node && menuEl.contains(e.target)) return;
      close();
    };
    const onResize = () => close();
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("resize", onResize);
    return () => {
      window.removeEventListener("scroll", onScroll, true);
      window.removeEventListener("resize", onResize);
    };
  });
</script>

<div class="picker">
  <input
    bind:this={input}
    class="mono model"
    role="combobox"
    aria-autocomplete="list"
    aria-expanded={open}
    aria-controls={menuID}
    autocomplete="off"
    spellcheck="false"
    disabled={readonly}
    value={query}
    placeholder={value || t("model")}
    onfocus={openMenu}
    onclick={openMenu}
    oninput={typed}
    onkeydown={keydown}
    onblur={close}
  />
  {#if provider && !readonly}
    <button
      type="button"
      class="tiny ghost probe"
      title={t("ask the provider again (ignore the cached answer)")}
      onmousedown={(e) => e.preventDefault()}
      onclick={refresh}
      disabled={loading}
    >↻</button>
  {/if}

  {#if open}
    <div
      bind:this={menuEl}
      id={menuID}
      class="menu"
      role="listbox"
      style:left="{pos.left}px"
      style:top="{pos.top}px"
      style:width="{pos.width}px"
      style:max-height="{pos.maxHeight}px"
    >
      <div class="status" class:bad={!!loadError}>{status}</div>
      {#if grouped.length}
        {#each grouped as g (g.group)}
          {#if g.group}
            <div class="group"><span>{g.group}</span><span class="dim">{g.items.length}</span></div>
          {/if}
          {#each g.items as it (it.m.id)}
            <button
              type="button"
              class="opt"
              class:active={it.i === active}
              class:selected={it.m.id === value}
              role="option"
              aria-selected={it.m.id === value}
              onmousedown={(e) => e.preventDefault()}
              onclick={() => pick(it.m)}
            >
              <span class="mid mono">{it.m.id}</span>
              {#if it.m.name && it.m.name !== it.m.id}
                <span class="mname dim">{it.m.name}</span>
              {/if}
            </button>
          {/each}
        {/each}
      {:else}
        <div class="status dim">{t("no model matches “{q}”", { q: query })}</div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .picker { position: relative; display: inline-flex; align-items: center; min-width: 220px; }
  .picker .model { flex: 1; min-width: 160px; }
  .probe { padding: 0 4px; line-height: 1; }
  .menu {
    position: fixed;
    z-index: 50;
    overflow-y: auto;
    background: var(--panel);
    border: 1px solid var(--line);
    border-radius: var(--radius);
    box-shadow: 0 10px 28px rgba(0, 0, 0, 0.28);
    padding: 3px;
  }
  .status {
    font-size: 11px;
    color: var(--dim);
    padding: 4px 7px 5px;
    border-bottom: 1px dashed var(--line);
    margin-bottom: 2px;
    overflow-wrap: anywhere;
  }
  .status.bad { color: var(--danger, #c0392b); }
  .group {
    display: flex;
    justify-content: space-between;
    gap: 8px;
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--dim);
    padding: 7px 7px 3px;
    border-top: 1px dashed var(--line);
    margin-top: 2px;
  }
  .group:first-of-type { border-top: 0; margin-top: 0; }
  .opt {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 1px;
    width: 100%;
    text-align: left;
    background: transparent;
    border: 0;
    border-radius: calc(var(--radius) - 2px);
    padding: 5px 7px;
    cursor: pointer;
    color: var(--fg);
    font-size: 12px;
  }
  .opt:hover, .opt.active { background: color-mix(in srgb, var(--accent) 16%, transparent); }
  .opt.selected .mid { font-weight: 600; }
  .opt .mid { font-size: 12px; overflow-wrap: anywhere; }
  .opt .mname { font-size: 11px; }
</style>
