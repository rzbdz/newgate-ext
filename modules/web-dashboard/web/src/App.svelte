<script lang="ts">
  // 界面壳：拉快照、按 Kind 渲染卡片、把改动**攒起来**、点保存才落盘。
  //
  // 为什么是「攒起来 + 一次保存」而不是「改一个字段发一次」：每个概念背后是一份
  // 文件，而写文件是一次 CAS（比对基线 → 原子写）。逐字段写会把一次编辑拆成
  // 几次互相看不见的提交，中间任何一次撞上别人的改动，文件就停在半路。攒起来
  // 一次写，前端手里的基线与文件之间只有一次比对。
  //
  // # 版面：左目录 / 右内容 / 中间分栏
  //
  // 第一版是把所有卡片按来源一条瀑布铺下来（实测 4303px ≈ 4.8 屏），于是「改一个
  // 开关」这件四步就能做完的事，第一步是滚三屏。现在：**左侧是节的目录**（点一下
  // 切一节）、**一节里多张卡走 tab**、**同一份文件的控件与原文并排**。验收线是
  // 「任何东西 4-5 次操作内到达」，操作数在下面每个动作旁边写着。
  import {
    apply,
    preview,
    runConceptAction,
    runRowAction,
    runSectionAction,
    snapshot,
    type Concept,
    type ConceptAction,
    type Conflict,
    type Section,
    type RowAction,
    type SectionAction,
  } from "./api";
  import { setLang, t } from "./i18n";
  import { emptyRoute, fileOf, parseHash, writeHash, type Action, type Route } from "./nav";
  import ConflictDialog from "./ConflictDialog.svelte";
  import Shortcuts from "./Shortcuts.svelte";
  import Sidebar from "./Sidebar.svelte";
  import SplitView from "./SplitView.svelte";
  import TabStrip from "./TabStrip.svelte";

  let concepts = $state<Concept[]>([]);
  let sections = $state<Section[]>([]);
  /** 每个概念**攒着**的改动。空 = 没有未保存的东西。 */
  let drafts = $state<Record<string, unknown>>({});
  let conflicts = $state<Conflict[]>([]);
  let error = $state("");
  let busy = $state(false);
  let filter = $state("");
  let auto = $state(false);
  let note = $state("");
  let filterBox = $state<HTMLInputElement | undefined>(undefined);
  /**
   * lang 是**后端解析出来的**语言（快照带来的）。它在这里单独存一份的原因见
   * 模板外面那个 `{#key}`：`t()` 读的语言住在 i18n.ts 的模块级变量里，而那
   * 不是 Svelte 的响应式状态——所以换语言这件事必须由**重新渲染整棵树**来落地。
   */
  let lang = $state("");

  /**
   * 现在在看哪一节、哪一张卡、并排还是折叠。
   *
   * 它住在 App 而不是某个子组件里，有一个很硬的理由：外面那个 `{#key lang}` 会在
   * 语言变化时**重建整棵子树**，子组件里存的东西全部会没。App 自己的状态活得下来
   * （`drafts` 就是靠这条活到今天的）。
   */
  let route = $state<Route>({ ...emptyRoute });

  const dirty = $derived(Object.keys(drafts));

  /** 过滤是**跨节**的：一处输入，处处生效（忘了某张卡在哪一节时，这是最快的一条路）。 */
  function matches(c: Concept): boolean {
    if (!filter) return true;
    const hay = (c.id + " " + c.title + " " + c.source + " " + c.kind).toLowerCase();
    return hay.includes(filter.toLowerCase());
  }

  /**
   * 导航单元 = 每份文件**一张**卡。同一份文件经常有两半：控件半（mapping-editor /
   * toggles / records）与原文半（code）——那是 SplitView 的左栏和右栏。把原文半
   * （`config.file.*`）也列成独立 tab，config 那节就会堆出一片 `config.file.*.json`
   * / `*.kv`，看着像同一个东西出现了两次，而且占了导航一大片——它不是一份应用户的
   * 配置，它是那份配置的**并排右栏**，只有当控件被打开、split 打开时才出现。
   *
   * 所以：这份文件有「非 code 的控制卡」时，只列控制卡，原文半隐藏（split 打开它自
   * 然出现）；这份文件只有一张卡（目录卡、table、log…）照列。不认识的模块照样成立
   * ——本条不 import 任何模块名，判据就是「同一相对路径 + kind 是不是 code」。
   */
  const navUnits = $derived.by(() => {
    const byFile = new Map<string, Concept[]>();
    for (const c of concepts) {
      const f = fileOf(c);
      const key = f ? c.source + "|" + f : null;
      if (key === null) continue;
      const a = byFile.get(key) ?? [];
      a.push(c);
      byFile.set(key, a);
    }
    const out: Concept[] = [];
    for (const c of concepts) {
      const f = fileOf(c);
      if (!f) {
        out.push(c);
        continue;
      }
      const group = byFile.get(c.source + "|" + f)!;
      const control = group.find((g) => g.kind !== "code");
      // 有控制卡时，原文只是它的右栏，不占导航位。
      if (control && c.kind === "code") continue;
      out.push(c);
    }
    return out;
  });

  /** 侧栏徽标：命中数 + 未保存数。过滤时显示的是命中数——不然搜到一个 3 张卡的
   *  节，徽标还写着 12，看着像搜索没生效。用 navUnits 数（原文半不单独算一张）。 */
  const counts = $derived.by(() => {
    const m = new Map<string, { total: number; dirty: number; locked: number }>();
    for (const s of sections) m.set(s.source, { total: 0, dirty: 0, locked: 0 });
    for (const c of navUnits) {
      const e = m.get(c.source);
      if (!e || !matches(c)) continue;
      e.total++;
      if (drafts[c.id] !== undefined) e.dirty++;
      if (c.locked) e.locked++;
    }
    return m;
  });

  /** 当前这一节的卡片（过滤之后）。顺序跟着后端来（(Source, ID) 排序）。 */
  const sectionCards = $derived(navUnits.filter((c) => c.source === route.section && matches(c)));

  /**
   * 一节的卡多到横向 tab 条滚不动时，改用**左侧第二个竖栏**（见 TabStrip）。
   *
   * 阈值按下限取：超过这条就该竖着列——config 有 38 张卡，横条要滚好几屏才能扫
   * 完，而竖着的一列是一眼的事。少于此的节（plugin-manager 两张、gateway 一张）
   * 横条更省行高。这名字是个玄学数字，所以写这句在这里解释；改它不需要其它地方动。
   */
  const TAB_OVERFLOW = 8;
  const verticalTabs = $derived(sectionCards.length > TAB_OVERFLOW);

  /** 当前这张卡。route.card 为空（或者落在一个已经不在的 id 上）时取第一张——
   *  「一节的第一张」是这一节的默认视图，键盘与 URL 都依赖它是确定的。 */
  const active = $derived(sectionCards.find((c) => c.id === route.card) ?? sectionCards[0]);

  /**
   * 与当前这张卡**说的是同一份文件**的另一半（见 nav.ts 的 fileOf）。
   *
   * 从哪一半进来看到的都一样：进来的若是原文（code），配给它的就是控件那一半，
   * 于是并排永远是「左控件、右原文」。配不上就单栏——没有错误、没有空栏。
   */
  const pair = $derived.by(() => {
    if (!active) return undefined;
    const f = fileOf(active);
    if (!f) return undefined;
    return concepts.find(
      (x) =>
        x.id !== active.id &&
        x.source === active.source &&
        fileOf(x) === f &&
        (active.kind === "code" ? x.kind !== "code" : x.kind === "code"),
    );
  });

  /** 当前这一节：它的名字与它注入的动作（见 api.ts 的 SectionAction）。 */
  const section = $derived(sections.find((s) => s.source === route.section));
  const sectionActions = $derived(section?.actions ?? []);

  /** 并排时哪一半在左：控件那一半。 */
  const leftCard = $derived(active?.kind === "code" && pair ? pair : active);
  const rightCard = $derived(active?.kind === "code" && pair ? active : pair);

  /** 概念的基线（内容哈希）。概念的数据是它自己定义形状的，基线住在里面。 */
  function baseOf(c: Concept): string {
    const b = (c.data as { base?: unknown } | null)?.base;
    return typeof b === "string" ? b : "";
  }

  /** 只有**自己会变**的那几位值得每几秒刷一次。谁算「会变」由贡献者声明
      （`Concept.Live`），界面不再按 Kind 猜：那个猜法把「读一次贵不贵」——
      只有贡献者知道的事——写成了渲染形状的附庸，而且没给别的 Kind 留口子
      （一张显示「还有多久自动关闭」的表，页面开着不动就永远停在那个数字上）。
      配置那一位不声明：它要重读并重新解析每一份 profile 与每一个源文件，
      让「刷一下计数器」顺带付那笔账，是把钱花在没人看的地方（见 api.ts 的 snapshot）。 */
  const liveSources = $derived([
    ...new Set(concepts.filter((c) => c.live).map((c) => c.source)),
  ]);

  /**
   * 把一列概念排成**后端那份顺序**：`(source, order, id)`。
   *
   * 只在局部刷新（`load(sources)`）合并之后用：那时手里是「旧的没动的 + 刚拿到的」
   * 两拨拼起来的，后端排好的数组顺序在拼接这一步没了，必须自己再排一次。
   *
   * 三个键与 `lib/view` 的 Snapshot 逐字对齐——**同一份顺序必须有同一个判据**。
   * 少一个都不行：只按 `(source, id)` 排（这是它以前的样子），`Order` 为 0/1 的
   * 「全局设置」「上游」会在每次静默刷新之后掉到十几张档位卡底下（Order 是 10），
   * 而这两个恰好是**装完就要配**的那两张，用户看到的症状是「它们经常会自己跑到
   * 下面」。`order` 缺省 0 与内核一致（没声明 Order 的贡献者排最前）。
   */
  function bySourceId(list: Concept[]): Concept[] {
    // 用 `<` 而不是 localeCompare：后端排的是 **Go 的字节序**（`Source < Source`），
    // 而 localeCompare 是 locale 敏感的——ICU 排序把 `-` 当可变字符，于是
    // `plugin-manager` 与 `pluginmanager` 的相对位置在两边可能不一样。模块名是机器
    // 标记，按字节比才是同一个判据；两条判据一致正是这个函数存在的理由。
    const cmp = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0);
    return [...list].sort(
      (a, b) => cmp(a.source, b.source) || (a.order ?? 0) - (b.order ?? 0) || cmp(a.id, b.id),
    );
  }

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

  /**
   * 默认位置：**声明了落点的那一节**（见 lib/view 的 Section.Default）。
   *
   * 在这之前它是「第一个有卡片的那一节」，而节的顺序按 Source 字母序——于是首屏
   * 落在哪一栏纯粹取决于谁的名字排前面（今天是 `breaker`）。想让「一屏全能」那一节
   * 先出现，唯一的办法是把 source 起成 `aaa-home` 之类来插队。
   *
   * 两条回落，都是真会发生的：
   *   - 没有哪一节声明落点（没装那个模块的装配，以及内核自己的测试图）——回到老
   *     规矩，第一个有卡片的。**逐字节与改动前一样**，那是这次改动的安全绳；
   *   - 声明了落点、但那一节此刻一张卡都没有（比如它唯一的卡是某个客户端没装才
   *     出现的）——空节进去只会看到一句「这里什么都没有」，所以让给第一个有卡片的。
   *     「落点」是偏好，不是强制：宁可落在别处，也别把用户扔进一张空页。
   */
  function defaultRoute(): Route {
    const withCards = sections.filter((s) => concepts.some((c) => c.source === s.source));
    const src =
      (withCards.find((s) => s.default) ?? withCards[0] ?? sections.find((s) => s.default) ?? sections[0])
        ?.source ?? "";
    return { section: src, card: "", split: route.split };
  }

  /**
   * 把位置修正到一个**真实存在**的地方，并把它写回地址栏。
   *
   * 三种失效都要接住，它们都不是故障而是日常：模块被关掉（那一节没了）、profile
   * 被删（那张卡没了）、手敲/被截断的链接。回落之后**改写 hash**——URL 不该说着
   * 一个屏幕上没有的东西（刷新一下又跳回来，那才叫费解）。
   */
  function resolveRoute() {
    if (sections.some((s) => s.source === route.section)) {
      if (route.card && !concepts.some((c) => c.id === route.card)) {
        route = { ...route, card: "" };
      }
    } else {
      route = defaultRoute();
    }
    writeHash(route);
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
      // 栏目表**每次都换**（两种刷新都带它）：模块是可以被关掉的，「这一节还在
      // 不在」正是刷新最该跟上的东西。
      sections = doc.sections;
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
        // 整份重读之后，两半都从盘上重新读了一遍——「另一半的草稿被挤掉」这件事
        // 已经过去了（该看的人看过这一眼了），留着那句话只会变成一条永远擦不掉的
        // 提示（它描述的是一个已经不存在的情况）。预览同理：盘上的内容已经就是
        // 「草稿生效之后」的样子，再拿草稿去覆盖显示就成了显示一份不存在的东西。
        dropped = "";
        previews = {};
      }
      resolveRoute();
      note = localTime(doc.generated_at, doc.lang);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      if (!quiet) busy = false;
    }
  }

  /**
   * 重载 = **把手里这份全丢掉，重新从 BFF 读一份**。
   *
   * 草稿（未保存的改动）也要丢——这正是「重载」这个动作的意思：屏幕上的一切回到
   * 盘上此刻的样子。之前重载只换 concepts、把 drafts 留着，于是「我点了重载，界面
   * 还是我刚才改的样子」——那个感觉像重载没生效，其实是我们把用户的改动又盖了回去。
   *
   * 保存过的那些早就从 drafts 里删掉了（见 saveAll），所以这里丢掉的**只有没存出去
   * 的东西**，而丢它们是用户按这个按钮时明确要求的。
   */
  function reloadAll() {
    drafts = {};
    conflicts = [];
    void load();
  }

  /**
   * 一份文件的两半：**最后被改的是哪一半**（`"ui"` 控件 / `"raw"` 原文）。
   *
   * 为什么必须有它：控件半与原文半是两个概念、两份草稿、两个基线，而它们写的是
   * **同一份文件**。改了原文之后控件那边手里还是「改之前那份盘上内容」——两边一起
   * 保存，后写的那一半必然撞在过期基线上（报「这个文件在页面加载之后被别人改过」），
   * 用户看到的是「怎么改都保存不了」。
   *
   * 所以：谁后改，谁说了算。另一半的草稿在**这边一改**的时候就作废丢掉——它是照着
   * 改动之前那份盘上内容渲染的，留着只会把人送进冲突。保存完的整份重读（见
   * saveAll）就是「编辑完马上同步另一半」那一步：两半都从盘上重新读一遍。
   */
  let lastEdit = $state<Record<string, "ui" | "raw">>({});

  /**
   * dropped 是「刚才丢掉的是哪一份文件另一半的草稿」——一句给用户看的话，不是错误。
   *
   * 为什么必须有：另一半的草稿是被**这一半**的编辑挤掉的（见 edit），而那是用户
   * 刚敲进去的字。不声不响地丢掉它违背这个仓库那条硬规矩（不静默），而且他多半
   * 会以为那段字还在——等他想起来回来看时，屏幕上已经是盘上那份旧内容了。
   */
  let dropped = $state("");

  /**
   * previews 是「原文那一半的草稿长这样时，**控件**那一半该显示成什么」——按控件
   * 那张卡的 id 存（见 api.ts 的 preview、内核 lib/view 的 Concept.Preview）。
   *
   * 为什么必须有它：一份文件的两半都能改，而控件那一半的编辑载荷是**整份文件**
   * （一张档位表整个交上去，不是那一格）。所以「在原文里粘一整份、再去动一个下拉
   * 框」如果没有这一问，交上去的就是**改之前**那份旧表——刚粘的东西当场没了，而
   * 屏幕上从头到尾没显示过它，用户不会觉得自己正在覆盖什么。
   *
   * 生命周期跟着**原文那份草稿**走：草稿在，预览在；草稿被挤掉/保存掉/撤销掉，
   * 预览跟着消失（否则控件那一半会停在一份磁盘上并不存在的内容上）。
   */
  let previews = $state<Record<string, unknown>>({});

  /** 这份文件上「控件那一半」：不是 code、且和它指同一份文件的那张卡。 */
  function controlOf(f: string): Concept | undefined {
    return concepts.find((c) => c.kind !== "code" && fileOf(c) === f);
  }

  /** 原文那一半此刻的草稿文本（没有草稿就是 undefined）。 */
  function rawDraftOf(f: string): string | undefined {
    const raw = concepts.find((c) => c.kind === "code" && fileOf(c) === f);
    const d = raw ? (drafts[raw.id] as { text?: unknown } | undefined) : undefined;
    return typeof d?.text === "string" ? d.text : undefined;
  }

  function dropPreview(f: string) {
    const ctl = controlOf(f);
    if (ctl && previews[ctl.id] !== undefined) {
      delete previews[ctl.id];
      previews = { ...previews };
    }
  }

  /**
   * 原文改了 → 问一句控件那一半现在该长什么样。
   *
   * **防抖 200ms**：CodeMirror 每敲一个字符就 onEdit 一次，而每敲一下就发一个请求
   * 是白费——敲到一半的 KV 本来就解析不了（后端那时回 error，界面保持上一次的
   * 样子，见 BFF 的 preview）。200ms 是「停手」的粗判：够短，看着像即时；够长，
   * 一次连续的输入只问一次。
   *
   * 回来晚了就用**内容**判一次：这中间草稿可能已经被挤掉或改过了，那时这一问的
   * 答案属于上一个版本，画上去就是在显示一份不存在的草稿。
   */
  let previewTimer: ReturnType<typeof setTimeout> | undefined;

  function schedulePreview(f: string, text: string) {
    const ctl = controlOf(f);
    if (!ctl?.previewable) return;
    clearTimeout(previewTimer);
    const id = ctl.id;
    previewTimer = setTimeout(() => {
      void (async () => {
        if (rawDraftOf(f) !== text) return;
        const res = await preview(id, text);
        if (res.error || rawDraftOf(f) !== text) return;
        previews[id] = res.data;
        previews = { ...previews };
      })();
    }, 200);
  }

  function edit(id: string, value: unknown) {
    const c = concepts.find((x) => x.id === id);
    const f = c ? fileOf(c) : undefined;
    if (c && f) {
      const side: "ui" | "raw" = c.kind === "code" ? "raw" : "ui";
      lastEdit[f] = side;
      let lost = "";
      for (const other of concepts) {
        if (other.id === id || fileOf(other) !== f) continue;
        const otherSide = other.kind === "code" ? "raw" : "ui";
        if (otherSide === side || drafts[other.id] === undefined) continue;
        lost = f;
        delete drafts[other.id];
      }
      dropped = lost
        ? t("both panes edit {file}, and only the one you touched last is saved — what was pending in the other pane has been dropped", {
            file: lost,
          })
        : "";
      // 改的是原文那一半 → 让控件那一半跟上（见 previews 的注释）。改的是控件那一
      // 半 → 原文的草稿刚被挤掉，预览也就没有依据了，跟着撤掉。
      if (side === "raw") {
        const text = (value as { text?: unknown } | null)?.text;
        if (typeof text === "string") schedulePreview(f, text);
      } else if (lost) {
        dropPreview(f);
      }
    }
    drafts[id] = value;
    drafts = { ...drafts };
  }

  function revert(id: string) {
    delete drafts[id];
    drafts = { ...drafts };
    // 撤销 = 把这张卡回到「没改过」。除了丢掉草稿，再整份重读一次——这样它显示
    // 的一定是**此刻盘上**的值，而不是上次快照那一刻的值（期间命令行可能改过）。
    // 与 saveAll 同一条：本地 BFF 无代价，不做联动计算。
    void load();
  }

  async function saveAll() {
    busy = true;
    error = "";
    const stillConflicting: Conflict[] = [];
    for (const id of dirty) {
      const c = concepts.find((x) => x.id === id);
      if (!c) continue;
      // 一份文件的两半只能有一半说了算（见 edit 里 lastEdit 的注释）：万一两边都
      // 还带着草稿（比如从别处塞进来的），只交**后改**的那一半——一起交必然有一半
      // 撞过期基线，用户看到的是「怎么保存都报错」。另一半的草稿就此丢掉：它写的
      // 是同一份文件的旧内容，留着只会再错一次。
      const f = fileOf(c);
      if (f && lastEdit[f]) {
        const side = c.kind === "code" ? "raw" : "ui";
        if (side !== lastEdit[f]) {
          delete drafts[id];
          continue;
        }
      }
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
      delete drafts[id];
    }
    drafts = { ...drafts };
    conflicts = stillConflicting;
    busy = false;
    if (!stillConflicting.length && !error) note = localTime(new Date().toISOString(), lang);
    // 存成功就**整份重读**（不做按源增量）：保存是写文件，界面上任何一张卡都可能
    // 因为这次写入而变——右栏原文、同源的别家卡、乃至 provider 列表。与其去算哪几
    // 张会变，不如无脑重拉一份快照，简单、正确、（本地 BFF 毫无性能代价）。
    if (!error && !stillConflicting.length) void load();
  }

  /**
   * 删掉**当前这张卡代表的那份档位文件**。
   *
   * 它打在**这张卡自己的 apply** 上（`{delete:true}` + 加载时的基线做 CAS）：一份
   * 文件一张卡，卡自己就能删自己——2026-09-20 之前这一步绕去另一张「档位文件」
   * 目录卡，而那张卡列的文件与这些卡一一对应，是同一件事说两遍（用户要求彻底删掉
   * 那张卡）。基线不对（别人刚改过）就让后端报冲突，不硬删。
   */
  async function deleteActive() {
    if (!active) return;
    busy = true;
    error = "";
    const id = active.id;
    const res = await apply(id, baseOf(active), { delete: true });
    busy = false;
    if (res.conflict) {
      conflicts = [...conflicts, res.conflict];
      return;
    }
    if (res.error) {
      error = `${active.title}: ${res.error}`;
      return;
    }
    delete drafts[id];
    drafts = { ...drafts };
    // 那张卡已经不存在了，别停在它上面（active 会落回这一节的第一张）。
    if (route.card === id) route = { ...route, card: "" };
    void load();
  }

  /**
   * 跑当前这一节上的一个动作（见 api.ts 的 runSectionAction）。
   *
   * 界面**不知道那个动作会干什么**，也不该知道：「再建一份档位文件」这个名字怎么挑、
   * 建出来是什么形状，全是拥有那一节的人的活。界面只做两件事——把点击转过去、
   * 然后重读（与保存那条路一样，干完活就重新拉一份快照）。
   */
  /**
   * 跑**某一张卡**上的一个动作（见 api.ts 的 runConceptAction）。
   *
   * 与 runAction 是同一件事的两个落点：那边认「这一节」，这边认「这一张卡」。
   * 两者都**不知道那个动作会干什么**——label 是贡献者写的一句话，改的是磁盘上的
   * 什么只有那一位知道。界面只做三件事：把点击转过去、丢掉手里的草稿（磁盘变了，
   * 基线全是旧的）、重读一遍。
   *
   * 为什么不像保存那样走 drafts / CAS：动作**不是**「把这份草稿写下去」。它是
   * 「干一件事」——比如「把这一份设为默认」，那件事改的是 state.json，与这张卡的
   * 文件没有关系。所以它没有 base、没有 edit，也不该让这张卡变脏。
   */
  async function runCardAction(id: string, a: ConceptAction) {
    busy = true;
    error = "";
    const res = await runConceptAction(id, a.id);
    busy = false;
    if (res.error) {
      error = res.error;
      return;
    }
    drafts = {};
    await load();
    const target = res.focus ? concepts.find((c) => c.id === res.focus) : undefined;
    if (target) {
      route = { section: target.source, card: target.id, split: route.split };
      writeHash(route);
    }
  }

  /**
   * 跑**表格里某一行**上的一个动作（见 api.ts 的 runRowAction）。
   *
   * 与 runCardAction 的关键差别：**不清草稿**。
   *
   * 那张卡上的动作（「把这一份设为默认」）会**改磁盘**，所以重读之后手里所有
   * 草稿的基线都成了旧的，必须丢掉；而一行上的动作（「探一下这条 binding」）
   * 动的是 daemon **内存里**的状态——它和这张卡读的那个文件没有关系。清掉的
   * 话，用户正开着的编辑会被一次「点个测试按钮」悄悄抹掉，那是纯粹的损失。
   *
   * 重读照做：这张表是 Live 的（摘帽、延迟、冷却都由这一次探活更新过），不重读
   * 的话界面会继续说几分钟前那件事。
   */
  async function runRow(id: string, row: string, a: RowAction) {
    busy = true;
    error = "";
    const res = await runRowAction(id, row, a.id);
    // **不管成没成都重读**，而且要在放错误之前：这一行上的动作改的是 daemon 的
    // 状态（探一发、结论记进健康表），表上那几列——探活、失败数、冷却到什么时候
    // ——正是这个按钮的结果。失败时不重读的话，横幅说「连不上」而表上还是点之前
    // 的样子，两句话互相矛盾，而用户没有办法判断哪个是真的。
    //
    // 顺序不能反：`load()` 开头会清 `error`，所以那句错误必须**最后**放回去。
    await load();
    busy = false;
    if (res.error) error = res.error;
  }

  async function runAction(a: SectionAction) {
    busy = true;
    error = "";
    const res = await runSectionAction(route.section, a.id);
    busy = false;
    if (res.error) {
      error = res.error;
      return;
    }
    // 草稿先丢掉：动作改的是磁盘，重读之后手里那些草稿的基线全是旧的。
    drafts = {};
    await load();
    // 动作说它做出了什么（res.focus），就切过去——「新建」的下一步一定是去填它，
    // 让用户自己在一堆卡里找刚建的那一张，等于把「它叫什么名字」这个问题的答案
    // 又藏起来一次。
    //
    // 界面**不猜**那个 id：找不到就留在原地（那边刚重读过，新卡就在导航里）。
    const target = res.focus ? concepts.find((c) => c.id === res.focus) : undefined;
    if (target) {
      route = { section: target.source, card: target.id, split: route.split };
      writeHash(route);
    }
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
    delete drafts[cf.concept];
    drafts = { ...drafts };
    conflicts = conflicts.filter((x) => x !== cf);
    // 写完了就整份重读（与 saveAll 同一条：本地 BFF、无性能代价、不联动）。
    void load();
  }

  /** 冲突里选「用磁盘上那份」：丢掉我的草稿，重新读一次。 */
  function takeTheirs(cf: Conflict) {
    delete drafts[cf.concept];
    drafts = { ...drafts };
    conflicts = conflicts.filter((x) => x !== cf);
    void load();
  }

  // 导航：三处入口（侧栏、tab、键盘）都只改 route，再由 writeHash 落到地址栏。
  // **只改一处状态**，URL 就不可能与屏幕说的不一样。
  function pickSection(source: string) {
    route = { ...route, section: source, card: "" };
    writeHash(route);
  }

  function pickCard(id: string) {
    route = { ...route, card: id };
    writeHash(route);
  }

  function toggleSplit() {
    route = { ...route, split: !route.split };
    writeHash(route);
  }

  /**
   * `[` / `]`：在这一节里换卡。到头了**绕回去**（而不是停在原地）：一个没有反馈
   * 的按键会让人以为快捷键没生效，然后去试第二次。
   */
  function stepCard(delta: 1 | -1) {
    if (sectionCards.length < 2 || !active) return;
    const i = sectionCards.findIndex((c) => c.id === active.id);
    const next = sectionCards[(i + delta + sectionCards.length) % sectionCards.length];
    if (next) pickCard(next.id);
  }

  function run(a: Action) {
    switch (a.kind) {
      case "save":
        // 没有草稿时也接（清掉上一次的报错），但不发请求。
        if (dirty.length && !busy) void saveAll();
        break;
      case "focus-filter":
        filterBox?.focus();
        filterBox?.select();
        break;
      case "card":
        stepCard(a.delta);
        break;
      case "section": {
        const s = sections[a.index];
        if (s) pickSection(s.source);
        break;
      }
      case "blur":
        break;
    }
  }

  /** 后退/前进（以及手动改 hash）：这是**唯一**从 URL 读回来的地方。 */
  function onHashChange() {
    route = parseHash(location.hash);
  }

  // 过滤时如果当前这一节一张都没命中，就跳到**第一个命中**的地方（跨节找东西那
  // 条路的最后一步）。写进 route 之后下一轮 `inSection` 就成立了，所以不会来回跳。
  $effect(() => {
    if (!filter || !concepts.length) return;
    if (concepts.some((c) => c.source === route.section && matches(c))) return;
    const first = concepts.find(matches);
    if (!first) return;
    route = { section: first.source, card: first.id, split: route.split };
    writeHash(route);
  });

  $effect(() => {
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  });

  /**
   * 有没保存的改动时，刷新/关标签页要先问一句。
   *
   * 草稿住在页面内存里（那是刻意的：一次编辑攒成一次 CAS 写），所以 F5 就是丢掉
   * 它——而「按错了刷新」与「只是想看看最新状态」长得一模一样。浏览器这一道问询
   * 是唯一拦得住它的地方（界面自己拦不住：刷新不是我们的代码发起的）。
   *
   * 文案由浏览器定（现代浏览器一律显示自己的那句），所以这里只 preventDefault。
   * 代价是这条会**跟着草稿来去**：没有草稿时不留监听，免得连正常刷新都弹框。
   */
  $effect(() => {
    if (!dirty.length) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  });

  route = parseHash(location.hash);
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
     保留的状态（草稿、打开的编辑器都还没建，route 在 App 自己身上）。 -->
{#key lang}
<div class="shell">
  <header class="top">
    <strong>newgate</strong>
    <input
      class="filter"
      bind:this={filterBox}
      placeholder={t("filter — id, title, kind")}
      bind:value={filter}
    />
    <span class="spacer"></span>
    {#if note}<span class="dim mono">{t("as of {time}", { time: note })}</span>{/if}
    <label class="dim row"><input type="checkbox" bind:checked={auto} /> {t("auto-refresh")}</label>
    <button onclick={reloadAll} disabled={busy}>{t("reload")}</button>
    <button class="primary" onclick={saveAll} disabled={busy || !dirty.length}>
      {t("save")}{dirty.length ? ` (${dirty.length})` : ""}
    </button>
  </header>

  <Sidebar {sections} active={route.section} {counts} onPick={pickSection} />

  <section class="content" class:subcol={verticalTabs}>
    <div class="errs">
      {#if error}
        <div class="banner">{error}</div>
      {/if}
      {#if dropped}
        <div class="notice">{dropped}</div>
      {/if}
      {#each conflicts as cf (cf.concept + cf.current)}
        <!-- 自己就是一块 .banner.conflict（不套壳：两层边框看着像两个东西）。 -->
        <ConflictDialog
          conflict={cf}
          onKeepMine={() => keepMine(cf)}
          onTakeTheirs={() => takeTheirs(cf)}
        />
      {/each}
    </div>

    <!-- 这一节的工具条：它自己注入的动作（「再建一份档位文件」这类）。
         动作住在**栏目**上而不是某张卡上——新建出来的那一份此刻还没有概念，没有哪张
         卡能挂这个按钮；挂在栏目上它还永远够得着，不管你正看着哪一张卡。
         没有动作就整条不画（空着的一条只会把内容往下推）。 -->
    {#if sectionActions.length}
      <div class="secbar">
        <span class="name">{section?.title}</span>
        <span class="spacer"></span>
        {#each sectionActions as a (a.id)}
          <button class="tiny ghost" disabled={busy} onclick={() => runAction(a)}>{a.label}</button>
        {/each}
      </div>
    {/if}

    <!-- 包一层 .nav-slot：TabStrip 是组件，App 的 scoped 样式给不了它根元素的网格
         位置，标在包这一层清楚了（见 app.css 的 .content.subcol）。 -->
    <div class="nav-slot">
      <TabStrip
        cards={sectionCards}
        active={active?.id ?? ""}
        {drafts}
        vertical={verticalTabs}
        onPick={pickCard}
      />
    </div>

    <div class="pane">
      {#if active}
        <SplitView
          left={leftCard}
          right={rightCard}
          {drafts}
          {previews}
          split={route.split}
          onEdit={edit}
          onRevert={revert}
          onDeleteFile={deleteActive}
          onAction={runCardAction}
          onRowAction={runRow}
          onToggleSplit={toggleSplit}
        />
      {:else if concepts.length}
        <p class="dim">{t("nothing in this section matches.")}</p>
      {:else}
        <p class="dim">{t("no concepts — nothing installed in this process contributes a view.")}</p>
      {/if}
    </div>
  </section>
</div>
{/key}

<!-- 键盘监听放在 `{#key}` **外面**：换语言没有理由把监听摘了再装一遍。 -->
<Shortcuts onAction={run} />

<style>
  .top {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 9px 16px;
    background: var(--panel);
    border-bottom: 1px solid var(--line);
    /* 不再 sticky：整页不滚了（见 app.css 的 .shell），没有东西需要它粘住。 */
    flex-wrap: wrap;
  }
  /* 只给过滤框定宽。原来是 `.top input`，于是「自动刷新」那个**复选框**也被拉成
     220px，把它的标签顶到几百像素之外——两个本该挨着的东西看起来毫不相干。 */
  .top .filter { width: 220px; }
</style>
