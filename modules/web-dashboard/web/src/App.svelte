<script lang="ts">
  // 界面壳：拉快照、按 Kind 渲染卡片、把改动**攒起来**、点保存才落盘。
  //
  // 为什么是「攒起来 + 一次保存」而不是「改一个字段发一次」：每个概念背后是一份
  // 文件，而写文件是一次 CAS（比对基线 → 原子写）。逐字段写会把一次编辑拆成
  // 几次互相看不见的提交，中间任何一次撞上别人的改动，文件就停在半路。攒起来
  // 一次写，前端手里的基线与文件之间只有一次比对。
  import { apply, snapshot, type Concept, type Conflict } from "./api";
  import { setLang, t } from "./i18n";
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
  /**
   * lang 是**后端解析出来的**语言（快照带来的）。它在这里单独存一份的原因见
   * 模板外面那个 `{#key}`：`t()` 读的语言住在 i18n.ts 的模块级变量里，而那
   * 不是 Svelte 的响应式状态——所以换语言这件事必须由**重新渲染整棵树**来落地。
   */
  let lang = $state("");

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

  /** 只有这几位值得每几秒刷一次（计数器、日志）。配置那一位要重读并重新解析
      每一份 profile 与每一个源文件——让「刷一下计数器」顺带付那笔账，是把钱花在
      没人看的地方（见 api.ts 的 snapshot）。 */
  const liveSources = $derived([
    ...new Set(
      concepts.filter((c) => c.kind === "series" || c.kind === "log").map((c) => c.source),
    ),
  ]);

  function bySourceId(list: Concept[]): Concept[] {
    return [...list].sort((a, b) => (a.source + "/" + a.id).localeCompare(b.source + "/" + b.id));
  }

  /**
   * sources 给出时是一次**增量**刷新：只问这几位，只替换这几位。
   * 不给 = 整份重读（首次加载、或者用户点了 reload）。
   */
  /**
   * localTime 把后端给的时间戳按**看页面那个人的时区**显示。
   *
   * 后端给的是带 `Z` 的 RFC3339（UTC）。原样摆出来的话，本机 +08:00 的人会读到
   * 「10:34」而墙上钟是 18:34——一个会让人怀疑数据是不是旧了的显示，而这条
   * 存在的全部意义正是回答「这份快照有多新」。时区换算属于渲染层：后端只知道
   * 自己的时区，而看页面的人可能在别处。
   *
   * 不是今天的话把日期也带上：页面可以开着不动（自动刷新关掉时），只显示一个
   * 钟点会读成「刚刚」。
   *
   * 解析不了就原样返回——显示一个看不懂的字符串，也比显示 `Invalid Date` 强。
   *
   * **格式跟随界面语言，不跟随浏览器**：tag 传的是后端解析出来的那门语言（与旁边
   * 那些字同一门）。两者的差别是看得见的——浏览器 en-US 而界面中文时，不传 tag
   * 会渲染成 `6:35:10 PM`，夹在「数据时间」后面很突兀。
   */
  function localTime(iso: string, tag?: string): string {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return d.toDateString() === new Date().toDateString()
      ? d.toLocaleTimeString(tag || undefined)
      : d.toLocaleString(tag || undefined);
  }

  async function load(sources?: string[], quiet = false) {
    if (!quiet) busy = true;
    error = "";
    try {
      const doc = await snapshot(sources);
      // 语言跟着后端走（每次快照都设一次：用户在命令行 `newgate lang zh-Hans`
      // 之后刷新页面就该变）。概念标题是后端翻译的，这一步只管界面骨架。
      setLang(doc.lang);
      // 赋值给 $state 才会让下面那个 `{#key}` 换掉整棵树（值相同时不换）。
      lang = doc.lang;
      // <html lang> 也要跟着走：它不参与渲染，但屏幕阅读器靠它选发音、浏览器靠它
      // 选断行与拼写检查——写成 en 而界面是中文，等于对辅助技术说错了话。
      document.documentElement.lang = doc.lang;
      if (sources) {
        const byId = new Map(concepts.map((c) => [c.id, c]));
        // 有草稿的卡片不换：那可能是只读概念之外的意外（读数与写数撞在同一张
        // 卡上），而用户正在改的东西被后台刷新顶掉是最不可原谅的一种丢失。
        for (const c of doc.concepts) if (drafts[c.id] === undefined) byId.set(c.id, c);
        concepts = bySourceId([...byId.values()]);
      } else {
        concepts = doc.concepts;
        // 整份重读之后，磁盘上已经不存在的概念（模块被关掉）没有地方可去了，
        // 它的草稿也该跟着走——留着它只会让「保存」按一个已经不存在的 id 发。
        const alive = new Set(doc.concepts.map((c) => c.id));
        for (const id of Object.keys(drafts)) if (!alive.has(id)) delete drafts[id];
        drafts = { ...drafts };
        conflicts = [];
      }
      note = localTime(doc.generated_at, doc.lang);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      if (!quiet) busy = false;
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
    if (!stillConflicting.length && !error) note = localTime(new Date().toISOString(), lang);
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
    void load();
  }

  load();

  // 自动刷新只问**活着的那几位**（计数器、日志）。整份重读会把配置目录每三秒
  // 重读一遍，而那个成本换不到任何新信息——配置文件不会自己变。
  $effect(() => {
    if (!auto) return;
    const src = liveSources;
    if (!src.length) return;
    const t = setInterval(() => void load(src, true), 3000);
    return () => clearInterval(t);
  });
</script>

<!-- 换语言要**重新渲染整棵树**。
     t() 查的那门语言住在 i18n.ts 的模块级变量里，不是 Svelte 的响应式状态，所以
     没有响应式依赖的字符串（按钮、placeholder）只按**首次渲染那一刻**的语言渲染
     一次，之后再不更新；而依赖了状态的（概念数、时间戳）会重渲染、拿到新语言。
     结果是同一屏上两种语言（实测：后端给 zh-Hans 时「44 个概念」是中文而
     "save" 是英文）。

     {#key} 在 lang 变化时重建子树，所有 t() 重新求值。它只在语言**真的变了**的
     时候发生——正常情况是启动后第一次拿到快照那一下，那时页面还没有任何值得
     保留的状态（草稿、打开的编辑器都还没建）。 -->
{#key lang}
<header class="top">
  <strong>newgate</strong>
  <span class="dim mono">{t("{n} concepts", { n: concepts.length })}</span>
  <input class="filter" placeholder={t("filter — id, title, kind")} bind:value={filter} />
  <span class="spacer"></span>
  {#if note}<span class="dim mono">{t("as of {time}", { time: note })}</span>{/if}
  <label class="dim row"><input type="checkbox" bind:checked={auto} /> {t("auto-refresh")}</label>
  <button onclick={() => load()} disabled={busy}>{t("reload")}</button>
  <button class="primary" onclick={saveAll} disabled={busy || !dirty.length}>
    {t("save")}{dirty.length ? ` (${dirty.length})` : ""}
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
    <p class="dim">{t("no concepts — nothing installed in this process contributes a view.")}</p>
  {/if}
</main>
{/key}

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
  /* 只给过滤框定宽。原来是 `.top input`，于是「自动刷新」那个**复选框**也被拉成
     220px，把它的标签顶到几百像素之外——两个本该挨着的东西看起来毫不相干。 */
  .top .filter { width: 220px; }
  main { padding: 16px; max-width: 1100px; margin: 0 auto; }
  .srch {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.7px;
    color: var(--dim);
    margin: 18px 0 8px;
  }
</style>
