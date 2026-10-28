<script lang="ts">
  // 指纹那一屏（形状见内核 lib/view 的 KindFingerprints）。
  //
  // 它回答的问题只有一句：**这条 binding 写的模型，上游此刻真的在用它吗？**
  //
  // 两种用法：
  //   - 「按 profile」：把配置里的每一条 binding 摊开，一行一个上次验到的结论，
  //     每一行都能现场再验一次；
  //   - 「挑一个模型」：不经过任何 profile，直接选一家上游、一个模型，拿一份指纹。
  //     排查「这个新模型是不是冒充的」时，配置里还没有它。
  //
  // # 它不认识任何模块
  //
  // 素材来自概念的数据（后端 verify 的 State）；「验一次」走 BFF 的扩展端点
  // `/api/ext/fingerprint`——端点名不是模块名，前端要知道的只是「有一个指纹端点」。
  // 没有这个端点时，所有按钮会报 404，而这一屏照常打开（这是「没装那个模块」的
  // 正常形态，不是白屏）。
  import { untrack } from "svelte";
  import { t } from "../i18n";
  import ModelPicker from "../ModelPicker.svelte";
  import {
    fingerprintRun,
    type FingerprintEntry,
    type FingerprintMatch,
    type FingerprintProfile,
    type FingerprintState,
  } from "../api";

  let { data }: { data: FingerprintState } = $props();

  const empty: FingerprintState = { profiles: [], providers: [], entries: [], loaded: false };
  /** 后端每次快照都会给一个新对象；这里只在没有覆盖时用它。 */
  const initial: FingerprintState = untrack(() => data ?? empty);
  let snap = $state<FingerprintState>(initial);
  let seen: FingerprintState = initial;
  $effect(() => {
    if (seen === data) return;
    seen = data;
    // 新快照可能已经包含终端里刚验出来的那一条；不整个替换，交给下面的合并
    // （按 checked_at 取新的），所以这里只更新配置视图那一半。
    snap = data ?? empty;
  });

  /**
   * 这个页面自己验出来的结论。与快照里的那份**按 checked_at 合并**：
   *
   *   - 网页上刚点完按钮 → 这里的更新；
   *   - 终端里刚跑过 `newgate verify` → 下一次快照里的更新。
   *
   * 两边都可能在对方之后发生。按时间取新的，才能既不被 3 秒一次的快照盖掉，
   * 也不把终端里更新的一条挡在外面。
   */
  let fresh = $state<FingerprintEntry[]>([]);

  const view = $derived(snap);
  const profiles = $derived<FingerprintProfile[]>(view.profiles ?? []);
  const providers = $derived(view.providers ?? []);

  const entries = $derived.by(() => {
    const m = new Map<string, FingerprintEntry>();
    for (const e of [...(view.entries ?? []), ...fresh]) {
      const k = keyOf(e.provider, e.model);
      const old = m.get(k);
      if (!old || Date.parse(e.checked_at) >= Date.parse(old.checked_at)) m.set(k, e);
    }
    return m;
  });

  /** 「按 profile」的筛选：空 = 全部。 */
  let filter = $state("");
  const shown = $derived(filter ? profiles.filter((p) => p.name === filter) : profiles);

  type Target = { provider: string; model: string };
  const targets = $derived.by<Target[]>(() => {
    const seen = new Set<string>();
    const out: Target[] = [];
    for (const p of shown) {
      for (const r of p.roles) {
        if (!r.provider || !r.model) continue;
        const k = keyOf(r.provider, r.model);
        if (seen.has(k)) continue;
        seen.add(k);
        out.push({ provider: r.provider, model: r.model });
      }
    }
    return out;
  });

  let busy = $state<Record<string, boolean>>({});
  let errors = $state<Record<string, string>>({});
  const anyBusy = $derived(Object.values(busy).some(Boolean));

  function setBusy(k: string, v: boolean) {
    busy = { ...busy, [k]: v };
  }
  function setError(k: string, msg: string) {
    errors = { ...errors, [k]: msg };
  }

  function keyOf(provider: string, model: string): string {
    return provider + "/" + model;
  }

  function mergeEntries(list: FingerprintEntry[], add: FingerprintEntry[]): FingerprintEntry[] {
    const m = new Map<string, FingerprintEntry>();
    for (const e of [...list, ...add]) {
      const k = keyOf(e.provider, e.model);
      const old = m.get(k);
      if (!old || Date.parse(e.checked_at) >= Date.parse(old.checked_at)) m.set(k, e);
    }
    return [...m.values()];
  }

  async function runOne(provider: string, model: string, api = "auto", count = 3): Promise<FingerprintEntry | null> {
    const k = keyOf(provider, model);
    if (busy[k]) return null;
    setBusy(k, true);
    setError(k, "");
    try {
      const e = await fingerprintRun(provider, model, api, count);
      fresh = mergeEntries(fresh, [e]);
      return e;
    } catch (err) {
      setError(k, errText(err));
      return null;
    } finally {
      setBusy(k, false);
    }
  }

  async function verifyAll() {
    const list = targets;
    let next = 0;
    const worker = async () => {
      while (next < list.length) {
        const tg = list[next++];
        await runOne(tg.provider, tg.model);
      }
    };
    // 两条并发：每条本身已经在并发打 3 个挑战。再多会把上游的限流与超时
    // 混进结论里，而这一屏要的是「模型是谁」，不是「谁先回」。
    await Promise.all([worker(), worker()]);
  }

  // ---- 自由验证（子页） ----
  let tab = $state<"profiles" | "pick">("profiles");
  let pickProvider = $state("");
  let pickModel = $state("");
  let pickAPI = $state("auto");
  let pickCount = $state(3);
  let pickEntry = $state<FingerprintEntry | null>(null);
  let pickBusy = $state(false);
  let pickError = $state("");

  let seeded = false;
  $effect(() => {
    if (!seeded && providers.length > 0) {
      seeded = true;
      if (!pickProvider) pickProvider = providers[0].name;
    }
  });

  let seenPick = untrack(() => pickProvider);
  $effect(() => {
    if (seenPick === pickProvider) return;
    seenPick = pickProvider;
    pickModel = "";
    pickEntry = null;
    pickError = "";
  });

  const pickProviderInfo = $derived(providers.find((p) => p.name === pickProvider));

  async function runPick() {
    const model = pickModel.trim();
    if (!pickProvider || !model || pickBusy) return;
    pickBusy = true;
    pickError = "";
    try {
      // 自由验证走自己的请求，不经过 runOne：那一份的 busy 表是按行共享的，
      // 同一个 binding 正好也在 profile 行上验着的时候，这里不该被它挡掉。
      const e = await fingerprintRun(pickProvider, model, pickAPI, pickCount);
      pickEntry = e;
      fresh = mergeEntries(fresh, [e]);
    } catch (err) {
      pickEntry = null;
      pickError = errText(err);
    } finally {
      pickBusy = false;
    }
  }

  // ---- 展示 ----
  function errText(err: unknown): string {
    return err instanceof Error ? err.message : String(err);
  }

  function matchTone(m: FingerprintMatch): "ok" | "warn" | "bad" {
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

  function prediction(e: FingerprintEntry): string {
    if (e.predicted && e.predicted_name && e.predicted_name !== e.predicted) {
      return `${e.predicted_name} (${e.predicted})`;
    }
    if (e.predicted) return e.predicted;
    return t("no ranking");
  }

  function pct(p?: number | null): string {
    return p == null ? "—" : `${Math.round(p * 100)}%`;
  }

  function ago(iso: string): string {
    const sec = Math.max(0, (Date.now() - Date.parse(iso)) / 1000);
    if (!Number.isFinite(sec)) return iso;
    if (sec < 60) return t("just now");
    if (sec < 3600) return t("{n} min ago", { n: Math.floor(sec / 60) });
    if (sec < 86400) return t("{n} h ago", { n: Math.floor(sec / 3600) });
    return t("{n} d ago", { n: Math.floor(sec / 86400) });
  }

  function decisionTone(d?: string): string {
    if (d === "scored") return "ok";
    if (d === "partial") return "warn";
    return d ? "bad" : "";
  }
</script>

<div class="fp">
  {#if view.error}
    <div class="notice">{view.error}</div>
  {/if}

  <div class="tabs">
    <button class:on={tab === "profiles"} onclick={() => (tab = "profiles")}>{t("by profile")}</button>
    <button class:on={tab === "pick"} onclick={() => (tab = "pick")}>{t("pick a model")}</button>
    <span class="spacer"></span>
    <span class="dim tiny">
      {#if view.loaded}
        {t("reference data {build}", { build: view.build || "?" })}
      {:else if view.data_cached}
        {t("reference data is cached — the first verify in this process loads it")}
      {:else}
        {t("reference data not loaded yet — the first verify downloads about 3.6 MB")}
      {/if}
    </span>
  </div>

  {#if tab === "profiles"}
    <div class="bar">
      <label class="dim tiny">
        {t("profile")}
        <select bind:value={filter}>
          <option value="">{t("all profiles")}</option>
          {#each profiles as p (p.name)}
            <option value={p.name}>{p.name}</option>
          {/each}
        </select>
      </label>
      <span class="spacer"></span>
      <button class="tiny" disabled={anyBusy || targets.length === 0} onclick={verifyAll}>
        {anyBusy ? t("verifying…") : t("verify all ({n})", { n: targets.length })}
      </button>
    </div>

    {#if shown.length === 0}
      <p class="dim">{t("no profiles to verify — configure a profile first, or use “pick a model”")}</p>
    {:else}
      {#each shown as p (p.name)}
        <div class="profile">
          <div class="phead">
            <span class="mono name">{p.name}</span>
            <span class="spacer"></span>
            <span class="dim tiny">{t("{n} tiers", { n: p.roles.length })}</span>
          </div>
          <table>
            <thead>
              <tr>
                <th>{t("tier")}</th>
                <th>{t("upstream/model")}</th>
                <th>{t("fingerprint")}</th>
                <th>{t("name match")}</th>
                <th>{t("checked")}</th>
                <th class="acts"></th>
              </tr>
            </thead>
            <tbody>
              {#each p.roles as r (p.name + ":" + r.role)}
                {@const pv = r.provider ?? ""}
                {@const mv = r.model ?? ""}
                {@const k = pv && mv ? keyOf(pv, mv) : ""}
                {@const e = k ? entries.get(k) : undefined}
                <tr>
                  <td>{r.role}</td>
                  <td class="mono">
                    {#if pv && mv}{pv}/{mv}{:else}<span class="dim">{r.error || "—"}</span>{/if}
                  </td>
                  <td class="mono">
                    {#if e}{prediction(e)}{:else}<span class="dim">—</span>{/if}
                  </td>
                  <td>
                    {#if e}
                      <span class="badge {matchTone(e.match)}">{matchText(e.match)}</span>
                    {:else}
                      <span class="dim">—</span>
                    {/if}
                  </td>
                  <td class="dim tiny">{e ? ago(e.checked_at) : "—"}</td>
                  <td class="acts">
                    {#if k}
                      <button
                        class="tiny ghost"
                        disabled={busy[k]}
                        title={t("ask the upstream for a fresh fingerprint (costs tokens)")}
                        onclick={() => runOne(pv, mv)}
                      >
                        {busy[k] ? t("verifying…") : t("verify")}
                      </button>
                    {/if}
                  </td>
                </tr>
                {#if k && errors[k]}
                  <tr><td></td><td colspan="5" class="bad tiny">{errors[k]}</td></tr>
                {/if}
                {#if k && e?.error}
                  <tr><td></td><td colspan="5" class="bad tiny">{e.error}</td></tr>
                {/if}
                {#if k && e?.cache_error}
                  <tr><td></td><td colspan="5" class="warn tiny">{e.cache_error}</td></tr>
                {/if}
              {/each}
            </tbody>
          </table>
        </div>
      {/each}
    {/if}
  {:else}
    <div class="pick">
      <label class="dim tiny">
        {t("provider")}
        <select bind:value={pickProvider}>
          {#each providers as p (p.name)}
            <option value={p.name}>{p.name}{p.has_key ? "" : t(" (no key)")}</option>
          {/each}
        </select>
      </label>
      <label class="dim tiny grow">
        {t("model")}
        <ModelPicker
          provider={pickProvider}
          declared={pickProviderInfo?.models ?? []}
          value={pickModel}
          verify={false}
          onchange={(m) => {
            pickModel = m;
            pickEntry = null;
            pickError = "";
          }}
        />
      </label>
      <label class="dim tiny">
        {t("api")}
        <select bind:value={pickAPI}>
          <option value="auto">{t("auto (from the provider protocol)")}</option>
          <option value="chat_completions">chat_completions</option>
          <option value="responses">responses</option>
          <option value="messages">messages</option>
        </select>
      </label>
      <label class="dim tiny">
        {t("challenges")}
        <select value={pickCount} onchange={(e) => (pickCount = Number(e.currentTarget.value))}>
          <option value={1}>1</option>
          <option value={2}>2</option>
          <option value={3}>3</option>
        </select>
      </label>
      <button class="tiny" disabled={pickBusy || !pickProvider || !pickModel.trim()} onclick={runPick}>
        {pickBusy ? t("verifying…") : t("get fingerprint")}
      </button>
    </div>

    {#if providers.length === 0}
      <p class="dim">{t("no providers declared — add one in providers.json first")}</p>
    {/if}
    {#if pickError}
      <div class="notice bad">{pickError}</div>
    {/if}

    {#if pickEntry}
      {@const e = pickEntry}
      <div class="result">
        <div class="head">
          <span class="mono">{e.provider}/{e.model}</span>
          <span class="badge {matchTone(e.match)}">{matchText(e.match)}</span>
          <span class="spacer"></span>
          <span class="dim tiny">{ago(e.checked_at)}</span>
        </div>
        <div class="grid">
          <div class="cell"><span class="dim tiny">{t("prediction")}</span><span class="mono">{prediction(e)}</span></div>
          <div class="cell"><span class="dim tiny">{t("probability")}</span><span class="mono">{pct(e.probability)}</span></div>
          <div class="cell"><span class="dim tiny">{t("decision")}</span><span class="badge {decisionTone(e.decision)}">{e.decision || "—"}</span></div>
          <div class="cell"><span class="dim tiny">{t("used outputs")}</span><span class="mono">{e.used_outputs ?? 0}</span></div>
          <div class="cell"><span class="dim tiny">{t("family")}</span><span class="mono">{e.family_name || e.family || "—"}</span></div>
          <div class="cell"><span class="dim tiny">{t("api")}</span><span class="mono">{e.api || "—"}</span></div>
          <div class="cell"><span class="dim tiny">{t("reference data")}</span><span class="mono">{e.build || "—"}</span></div>
        </div>
        {#if e.error}<p class="bad">{e.error}</p>{/if}
        {#if e.cache_error}<p class="warn">{e.cache_error}</p>{/if}
        {#if e.results?.length}
          <div class="cands">
            <div class="dim tiny">{t("top candidates")}</div>
            <table>
              <thead><tr><th>{t("model")}</th><th>{t("family")}</th><th class="right">{t("score")}</th><th class="right">{t("probability")}</th></tr></thead>
              <tbody>
                {#each e.results as c (c.model)}
                  <tr>
                    <td class="mono">{c.display_name || c.model}<span class="dim tiny"> {c.model}</span></td>
                    <td class="dim">{c.family_name || c.family || "—"}</td>
                    <td class="right mono">{c.score.toFixed(2)}</td>
                    <td class="right mono">{pct(c.probability)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </div>
    {/if}
  {/if}
</div>

<style>
  .fp { display: flex; flex-direction: column; gap: 12px; }
  .tabs, .bar, .pick, .head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .tabs button.on { background: var(--panel-2); border-color: var(--accent); }
  .bar, .pick { padding: 8px 10px; border: 1px solid var(--line); border-radius: var(--radius-sm); }
  .pick .grow { flex: 1; min-width: 220px; }
  .spacer { flex: 1; }
  .profile { border: 1px solid var(--line); border-radius: var(--radius-sm); overflow: hidden; }
  .phead { display: flex; align-items: center; gap: 8px; padding: 7px 10px; background: var(--panel-2); }
  .name { font-weight: 600; }
  table { border-collapse: collapse; width: 100%; font-size: 12px; }
  th, td { text-align: left; padding: 5px 10px; border-top: 1px solid var(--line); vertical-align: top; }
  thead th { border-top: 0; color: var(--dim); font-weight: 500; font-size: 11px; }
  .acts { width: 1%; white-space: nowrap; }
  .right { text-align: right; }
  .badge { display: inline-block; padding: 0 7px; border-radius: 99px; border: 1px solid var(--line); font-size: 11px; }
  .badge.ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 50%, var(--line)); }
  .badge.warn { color: var(--warn); border-color: color-mix(in srgb, var(--warn) 50%, var(--line)); }
  .badge.bad { color: var(--danger); border-color: color-mix(in srgb, var(--danger) 50%, var(--line)); }
  .bad { color: var(--danger); }
  .warn { color: var(--warn); }
  .result { border: 1px solid var(--line); border-radius: var(--radius-sm); padding: 10px; display: flex; flex-direction: column; gap: 10px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 8px 16px; }
  .cell { display: flex; flex-direction: column; gap: 2px; }
  .cands table { margin-top: 4px; }
  .notice { padding: 8px 11px; border-radius: var(--radius-sm); border: 1px solid var(--warn); background: color-mix(in srgb, var(--warn) 10%, transparent); }
  /* notice 也可能是请求失败（bad）：同一条横幅，语气跟着原因走。 */
  .notice.bad { border-color: var(--danger); background: color-mix(in srgb, var(--danger) 10%, transparent); }
</style>
