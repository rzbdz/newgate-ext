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
    type Themes,
    themes,
    setTheme,
  } from "./api";
  import { setLang, t } from "./i18n";
  import { applyTheme } from "./theme";
  import {
    emptyRoute,
    fileOf,
    homeRoute,
    parseHash,
    writeHash,
    type Action,
    type Route,
  } from "./nav";
  import ConflictDialog from "./ConflictDialog.svelte";
  import Shortcuts from "./Shortcuts.svelte";
  import Sidebar from "./Sidebar.svelte";
  import SplitView from "./SplitView.svelte";
  import TabStrip from "./TabStrip.svelte";
  import ThemePicker from "./ThemePicker.svelte";

  let concepts = $state<Concept[]>([]);
  let sections = $state<Section[]>([]);
  /** 每个概念**攒着**的改动。空 = 没有未保存的东西。 */
  let drafts = $state<Record<string, unknown>>({});
  let conflicts = $state<Conflict[]>([]);
  let error = $state("");
  let busy = $state(false);
  let filter = $state("");
  /**
   * 自动刷新**永远开着**（2026-09-29 去掉那个勾选框）。
   *
   * 用户的原话是「自动刷新这个功能也很垃圾啊，不要了吧，直接默认自动刷新得了」，
   * 以及「尽量减少不常用的控件按钮」。那个框是个只关不用的开关：这一屏是拿来
   * **放着看**的（探活回来的延迟、命令行那边改过的配置都要落上来），关掉它之后
   * 唯一的补救是手点「重载」，而那颗按钮也一起去掉了——两颗为同一件事服务的控件。
   *
   * 两条节奏见下面那个 effect：会自己变的那几位 3 秒，首屏那一节 15 秒（它贵）。
   */
  const auto = true;
  let note = $state("");
  let filterBox = $state<HTMLInputElement | undefined>(undefined);
  /**
   * lang 是**后端解析出来的**语言（快照带来的）。它在这里单独存一份的原因见
   * 模板外面那个 `{#key}`：`t()` 读的语言住在 i18n.ts 的模块级变量里，而那
   * 不是 Svelte 的响应式状态——所以换语言这件事必须由**重新渲染整棵树**来落地。
   */
  let lang = $state("");

  /**
   * themeDoc 是这一刻装着的皮肤表（见 api.ts 的 Themes 与后端
   * modules/web-dashboard/api.go）。
   *
   * 它**不进快照**：皮肤是界面自己的偏好，不是任何一个模块的贡献（谁贡献了
   * 「这一屏用什么颜色」？）。所以它走自己那条口，而且**读失败不是错误**——一个
   * 读不出皮肤表的后端（老版本）不该让整个界面打不开：空表就是「只有出厂那套」，
   * 界面照常画。
   */
  let themeDoc = $state<Themes>({ active: "", themes: [] });

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
  /**
   * **首屏那一节**（声明了落点的那一节，见 api.ts 的 Section.default）。
   *
   * 主页模式画的就是它，而且只画它的**第一张卡**：那一节今天是只有一张（`home.chains`
   * 2026-09-29 删了），而就算将来多回来几张，主页模式也不该有标签条——它是「一屏看完」
   * 的那个模式。要看别的，去完整界面。
   */
  const homeSection = $derived(sections.find((s) => s.default) ?? sections[0]);
  const homeConcept = $derived(
    concepts.filter((c) => c.source === homeSection?.source)[0],
  );

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
  /**
   * 此刻画的是哪一张卡。
   *
   * 主页模式下就是**首屏那一节的第一张**，而且不经 `sectionCards`——那一份要过
   * `matches()`（顶栏那个过滤器），而主页模式里没有过滤器；它也不看 `route.card`
   * （那一节只有一张卡，见 homeConcept）。
   */
  const active = $derived(
    route.home
      ? homeConcept
      : (sectionCards.find((c) => c.id === route.card) ?? sectionCards[0]),
  );

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
  /**
   * 此刻那一栏（完整界面里由 route.section 指，主页模式下就是落点那一节）。
   *
   * **主页模式下必须回落到落点那一节**：那时 route.section 是空的，所以 `sections.find`
   * 找不到任何东西——于是那一栏上的动作（`全部探一遍` / `auto fallback`）的**地址**
   * 也丢了。按钮照常画（它们直接读 homeSection），点下去却带着空来源发出去，后端回
   * 一句「没有叫 fallback 的动作」——一个只有点击时才炸的错。
   */
  const section = $derived(
    route.home ? homeSection : sections.find((s) => s.source === route.section),
  );
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
   * **首屏那一节**的来源（声明了落点的那一节，见 lib/view 的 Section.Default）。
   *
   * 它单独列出来是因为它要按**另一条节奏**刷：那一屏的产出要重读并重新解析每一份
   * profile（见 core/lib/view 的 Concept.Live——声明的判据是「读一次贵不贵」，配置
   * 那一位因此不声明 Live）。跟着计数器那条 3 秒的路走，等于把「看一眼请求数」变成
   * 「每 3 秒解析一次全部配置」，而这一屏开着不动的时候，那笔钱换不到任何新信息。
   *
   * 但它**也确实会变**，而且是这个界面自己的动作让它变的：探活回来的延迟落在健康表
   * 里，而首屏每一站的颜色就读那张表。所以它得有自己的节奏，不能只靠「用户手动点
   * 重载」——那一屏是拿来放着看的。
   */
  const homeSources = $derived(sections.filter((s) => s.default).map((s) => s.source));

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
    return { home: false, section: src, card: "", split: route.split };
  }

  /**
   * 把位置修正到一个**真实存在**的地方，并把它写回地址栏。
   *
   * 三种失效都要接住，它们都不是故障而是日常：模块被关掉（那一节没了）、profile
   * 被删（那张卡没了）、手敲/被截断的链接。回落之后**改写 hash**——URL 不该说着
   * 一个屏幕上没有的东西（刷新一下又跳回来，那才叫费解）。
   */
  function resolveRoute() {
    // **主页模式不走这条修正**：它的 `section` 本来就是空的（那一屏由落点那一节
    // 决定，见 homeSection），而这条修正会把「空 section」判成「那一节没了」，随手
    // 挑一个落点写成完整界面——主页模式于是一开屏就被自己顶掉。实测踩过。
    if (route.home) {
      writeHash(route);
      return;
    }
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

  /**
   * 跳到**编辑这份文件**的那张卡上。
   *
   * 这一跳能成立，靠的是「文件身份」这一件事已经在前端了（`fileOf`，见 nav.ts）；
   * 而「哪一张卡是它的编辑器」这条规则也只有一份（controlOf，下面几行）——两处各
   * 写一遍的话，从首屏跳过去可能落到**原文那一半**上，而那两半是同一份文件的两种
   * 看法，落错一半用户还得再点一次。
   *
   * 找不到目标时**什么都不做**：那说明这份文件此刻没有可编辑的卡（比如贡献它的模块
   * 被关掉了）。跳到一个不存在的地址比不跳更糟——它会把当前这一屏也弄丢。
   */
  function openFile(f: string) {
    const target = controlOf(f) ?? concepts.find((c) => fileOf(c) === f);
    if (!target) return;
    route = { home: false, section: target.source, card: target.id, split: route.split };
    writeHash(route);
  }

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
    if (!error && !stillConflicting.length) {
      void load();
      // **皮肤表也要重读**，与快照一起。
      //
      // 它是一条独立的接口（皮肤不在账本里，见 api.ts 的 Themes），所以 `load()`
      // 够不着它。少了这一句，症状是「从配置那一节的皮肤卡上换了一套，存完界面
      // 一点没变」——而顶栏那个下拉是当场变的（它走 pickTheme，那里会重读）。同
      // 一件设置的两个入口，只有一个生效，那是最难查的一类不一致。
      void loadThemes();
    }
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
      route = { home: false, section: target.source, card: target.id, split: route.split };
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
      route = { home: false, section: target.source, card: target.id, split: route.split };
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
    // 点**首屏那一栏** = 回主页模式（目录收起来、只剩卡片）。
    //
    // 与顶栏那颗「设置」是一对：那一颗从主页进完整界面，这一颗从完整界面回主页。
    // 少了这条回路，「主页」在目录里就与别的栏目没有分别了——而它本来就是另一个
    // 模式，不是另一节。
    if (source === homeSection?.source) {
      toHome();
      return;
    }
    route = { home: false, section: source, card: "", split: true };
    writeHash(route);
  }

  /** 回主页模式（地址里连 `#/s/…` 都不留，见 nav.ts 的 routeTo）。 */
  function toHome() {
    route = { ...homeRoute };
    writeHash(route);
  }

  /**
   * 从主页模式进**完整界面**，落在**配置文件那一页**上。
   *
   * 用户的原话是「处于主页的时候点击设置，应该跳转到配置文件那一页」。
   *
   * 判据是**「有卡、且不是首屏那一节」的栏目里的第一个**，不写死模块名——这一层不
   * 认识任何模块（同 fileOf 那条）。落在「第一节」上是错的：那一节就是主页自己。
   * 万一这个发行版只有首屏那一节（骨架配置），退回它自己。
   */
  function toSettings() {
    const target =
      sections.find((s) => s.source !== homeSection?.source && concepts.some((c) => c.source === s.source)) ??
      homeSection;
    if (!target) return;
    route = { home: false, section: target.source, card: "", split: true };
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
    route = { home: false, section: first.source, card: first.id, split: route.split };
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

  /**
   * 读一次皮肤表。
   *
   * **读不出来不是错误**：老版本的后端没有这条口（404），而一个读不到皮肤的界面
   * 该照常打开——空表就是「只有出厂那套令牌」。为它弹一句红字，等于把「这个后端
   * 少一个装饰性功能」说成「这个界面坏了」。
   */
  async function loadThemes() {
    try {
      themeDoc = await themes();
    } catch {
      themeDoc = { active: "", themes: [] };
    }
  }

  /** 换一套皮肤。空 id = 回到出厂那套令牌（见 theme.ts 的 applyTheme）。 */
  async function pickTheme(id: string) {
    try {
      await setTheme(id);
      // 换完重读而不是就地改本地那份：**存下来的是哪一套**只有后端知道
      // （它可能拒绝、也可能是另一个标签页刚改过），本地猜一份就会与盘上不一致。
      await loadThemes();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  /**
   * 皮肤落到 DOM 上（见 theme.ts）。放在 effect 里而不是 pickTheme 里：这样
   * **一处理**——首屏读到的那份、换完重读的那份，走的都是同一条路。
   */
  $effect(() => {
    applyTheme(themeDoc);
  });

  route = parseHash(location.hash);
  load();
  void loadThemes();

  // 自动刷新走**两条节奏**。
  //
  //   - 自己会变的那几位（计数器、日志）每 3 秒问一次——它们便宜，而且变得快。
  //   - **首屏那一节每 15 秒问一次**：它的产出要重读并重新解析每一份 profile（贵），
  //     但探活的结果、以及命令行那边改过的配置都会落在它身上，而这一屏正是拿来
  //     放着看的。整份重读（把首屏也按 3 秒刷）会把「看一眼请求数」变成「每 3 秒
  //     解析一次全部配置」，而配置文件不会自己变——那个成本换不到新信息。
  //
  // 两条各自算各自的来源，重叠的部分（一节同时是 Live 又是首屏）只走快的那条：
  // 同一份数据问两遍，多出来的那一遍没有任何用处。
  // **来源在这两个回调里面读，不在 effect 身上读**——这不是风格问题，是这一条能
  // 不能工作的分界：effect 会跟踪它同步执行期间读到的每一份状态，而那两份来源都是
  // 派生自 `sections` / `concepts` 的**新数组**。于是每一次刷新（包括这两条定时器
  // 自己发起的那些）都会让 effect 重跑、把两个定时器都清掉重建——而 15 秒那个永远
  // 等不到第 15 秒，它每 3 秒就被重置一次。
  //
  // 实测过：改之前首屏那一节在 17 秒里被刷了 **0** 次，而 3 秒那条看着是好的
  // （它比重置的间隔短，所以偶尔能跑完一次）——两条都错，只是错的症状不一样。
  $effect(() => {
    if (!auto) return;
    const fast = setInterval(() => {
      const src = liveSources;
      if (src.length) void load(src, true);
    }, 3000);
    const slow = setInterval(() => {
      const live = liveSources;
      const home = homeSources.filter((s) => !live.includes(s));
      if (home.length) void load(home, true);
    }, 15000);
    return () => {
      clearInterval(fast);
      clearInterval(slow);
    };
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
<div class="shell" class:home={route.home}>
  <header class="top">
    <!-- 设置那一颗在**最左边**、搜索框左边，而且只在主页模式出现（用户的原话：
         「设置按钮放页面左边，搜索栏的左边。处于主页才显示」，以及「设置和主页不要
         用文字，用 icon」）。为什么主页模式下有它、完整界面下没有：完整界面的目录
         里就有一栏「主页」可以点回来，而主页模式下目录收起来了——两颗按钮各自补
         对方缺的那条路，不留一对多余的。 -->
    {#if route.home}
      <button class="icon" title={t("settings")} aria-label={t("settings")} onclick={toSettings}>
        <svg viewBox="0 0 16 16" aria-hidden="true">
          <circle cx="8" cy="8" r="2.3" />
          <path d="M8 1.6v1.7M8 12.7v1.7M1.6 8h1.7M12.7 8h1.7M3.5 3.5l1.2 1.2M11.3 11.3l1.2 1.2M12.5 3.5l-1.2 1.2M4.7 11.3l-1.2 1.2" />
        </svg>
      </button>
    {/if}

    <strong>newgate</strong>
    {#if !route.home}
      <!-- 过滤框只在完整界面里：主页模式那一屏的卡片是**全部**（它就是一屏看完
           的那一屏），而给一个十来张卡的网格配一个过滤器，是又一件不常用的控件。 -->
      <input
        class="filter"
        bind:this={filterBox}
        placeholder={t("filter — id, title, kind")}
        bind:value={filter}
      />
    {/if}
    <span class="spacer"></span>

    {#if route.home}
      <!-- 主页模式就这几颗：`auto fallback` 那颗状态按钮、皮肤、保存（按需）。
           没有重载（自动刷新一直在跑）、没有过滤、没有自动刷新那个勾、没有「全部
           探一遍」——用户的原话是「尽量减少不常用的控件按钮」，「主页全部探一遍那个
           按钮也是傻啊，直接移除掉吧」。 -->
      {#each homeSection?.actions ?? [] as a (a.id)}
        {#if a.id === "fallback"}
          <button
            class="tiny ghost"
            class:toned={!!a.tone}
            data-tone={a.tone}
            disabled={busy}
            onclick={() => runAction(a)}
          >
            {a.label}
          </button>
        {/if}
      {/each}
    {:else}
      {#if note}<span class="dim mono">{t("as of {time}", { time: note })}</span>{/if}
      <button onclick={reloadAll} disabled={busy}>{t("reload")}</button>
    {/if}

    {#if themeDoc.themes.length}
      <ThemePicker doc={themeDoc} onPick={pickTheme} />
    {/if}
    <!-- 保存**按需出现**（有东西没存才画）。用户的原话是「只有保存是 dynamic 出现
         的」——一颗永远挂在那里的保存按钮，在没有改动的时候只是一块占着顶栏的灰。 -->
    {#if dirty.length}
      <button class="primary" onclick={saveAll} disabled={busy}>
        {t("save")} ({dirty.length})
      </button>
    {/if}
    {#if !route.home}
      <!-- 回主页模式。同样是**图标**（与设置那一颗对称）。 -->
      <button class="icon" title={t("home mode")} aria-label={t("home mode")} onclick={toHome}>
        <svg viewBox="0 0 16 16" aria-hidden="true">
          <path d="M2.6 7.4 8 3l5.4 4.4V13a.6.6 0 0 1-.6.6h-3v-3.4h-3.6v3.4h-3A.6.6 0 0 1 2.6 13z" />
        </svg>
      </button>
    {/if}
  </header>

  {#if route.home}
    <!-- 主页模式：**目录不画**。这一屏是「大部分时候就用它」的那一屏，而目录是
         一栏常驻的导航——要看别的，右上角那颗进完整界面。 -->
  {:else}
    <Sidebar {sections} active={route.section} {counts} onPick={pickSection} />
  {/if}

  <section class="content" class:home={route.home} class:subcol={!route.home && verticalTabs}>
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
    {#if !route.home && sectionActions.length}
      <div class="secbar">
        <span class="name">{section?.title}</span>
        <span class="spacer"></span>
        {#each sectionActions as a (a.id)}
          <button
          class="tiny ghost"
          class:toned={!!a.tone}
          data-tone={a.tone}
          disabled={busy}
          onclick={() => runAction(a)}
        >
          {a.label}
        </button>
        {/each}
      </div>
    {/if}

    <!-- 包一层 .nav-slot：TabStrip 是组件，App 的 scoped 样式给不了它根元素的网格
         位置，标在包这一层清楚了（见 app.css 的 .content.subcol）。 -->
    {#if !route.home}
    <div class="nav-slot">
      <TabStrip
        cards={sectionCards}
        active={active?.id ?? ""}
        {drafts}
        vertical={verticalTabs}
        onPick={pickCard}
      />
    </div>
    {/if}

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
          onOpenFile={openFile}
          bare={route.home}
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
