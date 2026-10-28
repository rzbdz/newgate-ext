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
  import { onMount, untrack } from "svelte";
  import { t } from "./i18n";
  import {
    modelList,
    fingerprintCache,
    fingerprintRun,
    type FingerprintEntry,
    type FingerprintMatch,
    type ModelInfo,
    type ModelList,
  } from "./api";
  import { fuzzyFilter } from "./fuzzy";

  let {
    value = "",
    provider = "",
    declared = [],
    readonly = false,
    /**
     * 要不要在格子旁边显示上一次验到的指纹、并给一颗「验」按钮。
     *
     * 默认开；自由验证那一页把它关掉——那里自己有一套 API/挑战数选择与一颗
     * 「获取指纹」，同一个格子里再放一颗同名按钮会让「这一次按的到底是哪一个」
     * 变得没有答案。
     */
    verify = true,
    onchange,
  }: {
    value?: string;
    provider?: string;
    /** providers.json 里声明的模型名：问不到上游时就用这一份。 */
    declared?: string[];
    readonly?: boolean;
    verify?: boolean;
    onchange: (model: string) => void;
  } = $props();

  let open = $state(false);
  /** 输入框里正在打的那一串。**打开时清空**，菜单才会列出全部模型。 */
  let query = $state(untrack(() => value));
  let active = $state(0);
  let loading = $state(false);
  let loadError = $state("");
  let catalogs = $state<Record<string, ModelList>>({});

  /**
   * 按模型名缓存的指纹结论（来自 `/api/ext/fingerprint` 的 cache 动作）。
   *
   * 它只在该下拉被打开/换 provider 时读一次（缓存文件很小），**不自动去验**：
   * verify 会花 token，必须是人点出来的。这里展示的永远是「上一次验到的那份」。
   */
  let fingerprints = $state<Record<string, FingerprintEntry>>({});
  let fpError = $state("");
  let fpBusy = $state(false);

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
  // 切回来还在）。指纹也一起换：它是按「provider/model」存的，留着上一家的
  // 只会让新格子显示一个不属于它的结论。
  let seenProvider = untrack(() => provider);
  $effect(() => {
    if (seenProvider === provider) return;
    seenProvider = provider;
    open = false;
    loadError = "";
    query = value;
    fingerprints = {};
    fpError = "";
    if (verify) void loadFingerprints();
  });

  onMount(() => {
    if (verify) void loadFingerprints();
  });

  /** 选中那个模型上一次的结论（没有就是 undefined）。 */
  const selectedFP = $derived(provider && value ? fingerprints[value] : undefined);

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

  async function loadFingerprints() {
    if (__NEWGATE_DEMO__ || !provider || !verify || readonly) return;
    try {
      const list = await fingerprintCache(provider);
      const next: Record<string, FingerprintEntry> = {};
      for (const e of list) next[e.model] = e;
      fingerprints = next;
      fpError = "";
    } catch (e) {
      // 端点是可选的：没装那个模块时这里是 404。**不是故障**——格子照常用，
      // 只是没有指纹可展示（与 modelList 的 404 同一条）。
      fpError = e instanceof Error ? e.message : String(e);
    }
  }

  /**
   * 现场验一次选中的模型。**会花 token**：按钮的 title 已说明，不弹确认框——
   * 这个按钮点一下就是「我要验」，再问一次只会多一步。
   */
  async function verifySelected() {
    const model = value.trim();
    if (!verify || !provider || !model || fpBusy) return;
    fpBusy = true;
    fpError = "";
    try {
      const e = await fingerprintRun(provider, model, "auto", 3);
      fingerprints = { ...fingerprints, [e.model]: e };
    } catch (e) {
      fpError = e instanceof Error ? e.message : String(e);
    } finally {
      fpBusy = false;
    }
  }

  function matchTone(m: FingerprintMatch): string {
    switch (m) {
      case "match":
      case "dated":
        return "ok";
      case "family":
      case "mismatch":
        return "warn";
      default:
        return "bad";
    }
  }

  function matchText(m: FingerprintMatch): string {
    switch (m) {
      case "match":
        return t("match");
      case "dated":
        return t("dated snapshot");
      case "family":
        return t("same family");
      case "mismatch":
        return t("different model");
      case "unknown":
        return t("no ranking");
      case "error":
        return t("error");
      default:
        return m;
    }
  }

  function fpLabel(e: FingerprintEntry): string {
    const name = e.predicted_name || e.predicted;
    return name || t("no ranking");
  }

  function fpPct(e: FingerprintEntry): string {
    return e.probability == null ? "" : ` ${Math.round(e.probability * 100)}%`;
  }

  function fpMark(m: FingerprintMatch): string {
    switch (m) {
      case "match":
      case "dated":
        return "✓";
      case "family":
      case "mismatch":
        return "⚠";
      default:
        return "✕";
    }
  }

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
      // 指纹是本地缓存（几十 KB），每次打开都重读一遍：终端里刚跑过 verify
      // 的话，下拉里那一格应该立刻是新结论，而不是页面加载时的那一份。
      if (verify) void loadFingerprints();
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

  {#if verify && provider && !readonly && value.trim()}
    <!-- 上一次验到的指纹就在格子旁边：它是**陈述**（缓存），不是「这一格的值」，
         所以不参与编辑、也不进草稿。没有它就是「还没验过」，不是故障。 -->
    <span
      class="fp {selectedFP ? matchTone(selectedFP.match) : 'none'}"
      title={selectedFP
        ? `${fpLabel(selectedFP)}${fpPct(selectedFP)} · ${selectedFP.checked_at}`
        : t("no fingerprint yet — verify to ask the upstream")}
    >
      {#if selectedFP}{fpMark(selectedFP.match)} {fpLabel(selectedFP)}{fpPct(selectedFP)}{:else}{t("no fingerprint")}{/if}
    </span>
    <button
      type="button"
      class="tiny ghost"
      title={t("ask the upstream for a fresh fingerprint (costs tokens)")}
      onmousedown={(e) => e.preventDefault()}
      onclick={verifySelected}
      disabled={fpBusy}
    >{fpBusy ? t("verifying…") : t("verify")}</button>
  {/if}
  {#if fpError}
    <span class="fp bad" title={fpError}>!</span>
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
      <div class="status" class:bad={!!loadError || !!fpError}>
        {status}
        {#if fpError} · {t("fingerprint: {err}", { err: fpError })}{/if}
      </div>
      {#if grouped.length}
        {#each grouped as g (g.group)}
          {#if g.group}
            <div class="group"><span>{g.group}</span><span class="dim">{g.items.length}</span></div>
          {/if}
          {#each g.items as it (it.m.id)}
            {@const fpe = fingerprints[it.m.id]}
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
              <span class="oinfo">
                <span class="mid mono">{it.m.id}</span>
                {#if it.m.name && it.m.name !== it.m.id}
                  <span class="mname dim">{it.m.name}</span>
                {/if}
              </span>
              {#if fpe}
                <!-- 下拉里每一行都带**上一次**的指纹：选之前就能看见「这一行写的是
                     这个名字，但上次验出来是另一个」。这是这个下拉最该有的信息。 -->
                <span class="fpm {matchTone(fpe.match)}" title={matchText(fpe.match)}>
                  {matchText(fpe.match)}{#if fpe.probability != null} · {Math.round(fpe.probability * 100)}%{/if}
                </span>
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
  .picker { position: relative; display: inline-flex; align-items: center; flex-wrap: wrap; row-gap: 4px; min-width: 220px; }
  .picker .model { flex: 1; min-width: 160px; }
  .probe { padding: 0 4px; line-height: 1; }
  /* 指纹那一格：陈述，不是输入。颜色只表达「名字对上没有」，不改变形状。 */
  .fp {
    font-size: 11px;
    font-family: var(--mono);
    padding: 1px 6px;
    border-radius: 99px;
    border: 1px solid var(--line);
    color: var(--dim);
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .fp.ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 50%, var(--line)); }
  .fp.warn { color: var(--warn); border-color: color-mix(in srgb, var(--warn) 50%, var(--line)); }
  .fp.bad { color: var(--danger, #c0392b); border-color: var(--danger, #c0392b); }
  .fp.none { color: var(--dim); border-style: dashed; }
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
    flex-direction: row;
    align-items: baseline;
    gap: 8px;
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
  .oinfo { display: flex; flex-direction: column; gap: 1px; flex: 1; min-width: 0; }
  .opt:hover, .opt.active { background: color-mix(in srgb, var(--accent) 16%, transparent); }
  .opt.selected .mid { font-weight: 600; }
  .opt .mid { font-size: 12px; overflow-wrap: anywhere; }
  .opt .mname { font-size: 11px; }
  /* 每行右侧的上一次指纹：小、等宽，不抢模型名的位置。 */
  .fpm { font-size: 10px; font-family: var(--mono); white-space: nowrap; color: var(--dim); }
  .fpm.ok { color: var(--ok); }
  .fpm.warn { color: var(--warn); }
  .fpm.bad { color: var(--danger, #c0392b); }
</style>
