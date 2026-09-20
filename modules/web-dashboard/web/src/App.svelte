<script lang="ts">
  // 界面壳：拉快照、按 Kind 渲染卡片、把改动**攒起来**、点保存才落盘。
  //
  // 为什么是「攒起来 + 一次保存」而不是「改一个字段发一次」：每个概念背后是一份
  // 文件，而写文件是一次 CAS（比对基线 → 原子写）。逐字段写会把一次编辑拆成
  // 几次互相看不见的提交，中间任何一次撞上别人的改动，文件就停在半路。攒起来
  // 一次写，前端手里的基线与文件之间只有一次比对。
  import { apply, snapshot, type Concept, type Conflict } from "./api";
  import ConceptCard from "./ConceptCard.svelte";
  import ConflictDialog from "./ConflictDialog.svelte";

  let concepts = $state<Concept[]>([]);
  /** 每个概念**攒着**的改动。空 = 没有未保存的东西。 */
  let drafts = $state<Record<string, unknown>>({});
  let conflicts = $state<Conflict[]>([]);
  let error = $state("");
  let busy = $state(false);
  let filter = $state("");
  let auto = $state(false);
  let note = $state("");

  const dirty = $derived(Object.keys(drafts));
  const sources = $derived([...new Set(concepts.map((c) => c.source))].sort());
  const shown = $derived(
    concepts.filter((c) => {
      if (!filter) return true;
      const hay = (c.id + " " + c.title + " " + c.source + " " + c.kind).toLowerCase();
      return hay.includes(filter.toLowerCase());
    }),
  );

  /** 概念的基线（内容哈希）。概念的数据是它自己定义形状的，基线住在里面。 */
  function baseOf(c: Concept): string {
    const b = (c.data as { base?: unknown } | null)?.base;
    return typeof b === "string" ? b : "";
  }

  async function load(keepDrafts = false) {
    busy = true;
    error = "";
    try {
      const doc = await snapshot();
      if (!keepDrafts) drafts = {};
      // 刷新**不覆盖**有草稿的卡片：那是用户正在改的东西，被后台刷新抹掉是最
      // 不可原谅的一种丢失（他连自己丢在哪都不知道）。
      const merged = doc.concepts.map((c) =>
        drafts[c.id] && keepDrafts ? { ...c } : c,
      );
      const byId = new Map(concepts.map((c) => [c.id, c]));
      concepts = merged.map((c) => (drafts[c.id] && keepDrafts ? byId.get(c.id) ?? c : c));
      note = doc.generated_at;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  function edit(id: string, value: unknown) {
    drafts[id] = value;
    drafts = { ...drafts };
  }

  function revert(id: string) {
    delete drafts[id];
    drafts = { ...drafts };
  }

  async function saveAll() {
    busy = true;
    error = "";
    const stillConflicting: Conflict[] = [];
    for (const id of dirty) {
      const c = concepts.find((x) => x.id === id);
      if (!c) continue;
      const res = await apply(id, baseOf(c), drafts[id]);
      if (res.conflict) {
        stillConflicting.push(res.conflict);
        continue;
      }
      if (res.error) {
        // 贡献者的报错原样显示并点名是哪张卡片：把几个概念的错误混成一句
        // 「保存失败」，用户不知道该去看哪一张。
        error = `${c.title}: ${res.error}`;
        continue;
      }
      if (res.base !== undefined && c.data && typeof c.data === "object") {
        // 续着改不用刷新页面：新基线直接写回卡片里那份数据。
        (c.data as { base?: string }).base = res.base;
      }
      delete drafts[id];
    }
    drafts = { ...drafts };
    conflicts = stillConflicting;
    busy = false;
    if (!stillConflicting.length && !error) note = new Date().toLocaleTimeString();
  }

  /** 冲突里选「保留我的」：拿磁盘上那份的基线重放一次。 */
  async function keepMine(cf: Conflict) {
    const c = concepts.find((x) => x.id === cf.concept);
    if (!c) return;
    const res = await apply(cf.concept, cf.current, drafts[cf.concept]);
    if (res.error || res.conflict) {
      error = res.error ?? "conflict again — someone is writing this file right now";
      return;
    }
    if (res.base !== undefined && c.data && typeof c.data === "object") {
      (c.data as { base?: string }).base = res.base;
    }
    delete drafts[cf.concept];
    drafts = { ...drafts };
    conflicts = conflicts.filter((x) => x !== cf);
  }

  /** 冲突里选「用磁盘上那份」：丢掉我的草稿，重新读一次。 */
  function takeTheirs(cf: Conflict) {
    delete drafts[cf.concept];
    drafts = { ...drafts };
    conflicts = conflicts.filter((x) => x !== cf);
    void load(true);
  }

  load();

  // 自动刷新只对**指标**有意义（配置是静态的，而计数器一直在动）。有草稿时
  // 一律不刷：看的时候数字在跳可以接受，正在改的输入框被抹掉不行。
  $effect(() => {
    if (!auto) return;
    const t = setInterval(() => {
      if (!dirty.length) void load(true);
    }, 5000);
    return () => clearInterval(t);
  });
</script>

<header class="top">
  <strong>newgate</strong>
  <span class="dim mono">{concepts.length} concepts</span>
  <input placeholder="filter — id, title, kind" bind:value={filter} />
  <span class="spacer"></span>
  {#if note}<span class="dim mono">as of {note}</span>{/if}
  <label class="dim row"><input type="checkbox" bind:checked={auto} /> auto-refresh</label>
  <button onclick={() => load(false)} disabled={busy}>reload</button>
  <button class="primary" onclick={saveAll} disabled={busy || !dirty.length}>
    save{dirty.length ? ` (${dirty.length})` : ""}
  </button>
</header>

<main>
  {#if error}
    <div class="banner">{error}</div>
  {/if}
  {#each conflicts as cf (cf.concept + cf.current)}
    <ConflictDialog
      conflict={cf}
      onKeepMine={() => keepMine(cf)}
      onTakeTheirs={() => takeTheirs(cf)}
    />
  {/each}

  {#each sources as src (src)}
    {@const cards = shown.filter((c) => c.source === src)}
    {#if cards.length}
      <h2 class="srch">{src}<span class="dim"> — {cards.length}</span></h2>
      {#each cards as c (c.id)}
        <ConceptCard concept={c} draft={drafts[c.id]} onEdit={(v) => edit(c.id, v)} onRevert={() => revert(c.id)} />
      {/each}
    {/if}
  {/each}

  {#if !concepts.length && !error}
    <p class="dim">no concepts — nothing installed in this process contributes a view.</p>
  {/if}
</main>

<style>
  .top {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 9px 16px;
    background: var(--panel);
    border-bottom: 1px solid var(--line);
    position: sticky;
    top: 0;
    z-index: 5;
    flex-wrap: wrap;
  }
  .top input { width: 220px; }
  main { padding: 16px; max-width: 1100px; margin: 0 auto; }
  .srch {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.7px;
    color: var(--dim);
    margin: 18px 0 8px;
  }
</style>
