// 位置（哪一节、哪一张卡）与 URL 之间的翻译。**纯函数**，不持有状态。
//
// # 为什么用 hash 而不是路径段
//
// `/ui/config` 其实能用（BFF 对「basename 里没有点」的 GET 一律回 index.html，
// SPA 兜底本来就支持），所以这不是「路径不行」。理由是**别让那个 bug 长回来**：
// 相对地址的解析结果取决于当前文档的路径——文档在 `/ui/s/config/xxx` 里，
// `api/snapshot` 就会被解析成 `/ui/s/config/api/snapshot`，掉出挂载点、落进数据面。
// 今天运行期地址已经改成绝对的了（见 api.ts 的 API），但 hash 让文档地址**永远
// 只有一段深**，这层耦合就没有机会再长出来。
//
// 次要好处：改 hash 不发请求，`hashchange` 一条监听就够（pushState + popstate
// 还得自己补一遍）。
//
// # 为什么路由键是 source，不是栏目名
//
// 栏目名是**给人看的、会翻译的**。拿它当路由键，中文界面与英文界面就是两套 URL，
// 而且一改措辞所有书签全废。source 是机器标记（模块名），不翻译也不改。

/**
 * 界面上「现在在看什么」。
 *
 * # 两种模式
 *
 * `home` 为真 = **主页模式**：只剩首屏那一节的卡片，目录、栏目横条、卡片标签条
 * 全都不画。这是**打开就进**的那个模式（用户的原话：「大部分时候就是主要用主页」），
 * 它对应的地址是**没有 `#/s/…`** 的那一个（`/ui/`）。
 *
 * `home` 为假 = **完整界面**：目录 + 各节 + 所有控件。`section` 空串 = 还没定
 * （首屏由 App 挑一个）。
 *
 * 两件事分开两个字段，而不是拿「section 是不是落点那一节」推：那样在完整界面里
 * 点「主页」会当场把目录收掉，而用户只是想在完整界面里看那一节。
 */
export interface Route {
  /** 主页模式（见上）。与 section 互斥。 */
  home: boolean;
  /** 来源（模块名）——机器标记，不翻译。 */
  section: string;
  /** 概念 ID。空串 = 那一节里的第一张。 */
  card: string;
  /** 有「左控件 / 右原文」两份时是否并排。折叠起来只看控件那一半。 */
  split: boolean;
}

export const emptyRoute: Route = { home: false, section: "", card: "", split: true };

/** 主页模式那个位置（地址是 `/ui/`，没有 `#/s/…`）。 */
export const homeRoute: Route = { home: true, section: "", card: "", split: true };

/**
 * 把 location.hash 解析成一个位置。认不出来的一律当「还没定」——**不报错**：
 * 手敲的 URL、旧版本留下的 hash、被聊天软件截断的链接，都该落到首页而不是白屏。
 */
export function parseHash(hash: string): Route {
  const body = hash.replace(/^#\/?/, "");
  const [path, query] = body.split("?");
  const parts = path.split("/");
  // 没有 `s/<节>` 的一律是**主页模式**——空 hash、`#/`、手敲错的一律落到这里。
  // 「认不出来就落到首页」这条规矩本来就在，主页模式只是让它落得更好看。
  if (parts[0] !== "s" || !parts[1]) return { ...homeRoute };
  return {
    home: false,
    section: decodeURIComponent(parts[1]),
    // 概念 ID **自己含斜杠**（config.file.mappings/demo.json）——所以切一次就停，
    // 后面整段都是 ID。按 "/" 全切再取第三段会把那个 ID 截成 config.file.mappings，
    // 于是刷新之后落回第一节的第一张卡（看着像「书签不生效」）。
    card: parts.slice(2).map(decodeURIComponent).join("/"),
    split: new URLSearchParams(query ?? "").get("split") !== "0",
  };
}

/** 位置 → hash 片段（含开头的 #）。 */
export function routeTo(r: Route): string {
  // 主页模式写成**空串**（地址栏里连 `#` 都不留）：`/ui/` 是它本来的样子，而写一个
  // `#/` 会在开屏时凭空推一条历史（浏览器把「没有 fragment」与「空的 fragment」当成
  // 两个地址），于是「后退」第一次点看起来什么都没发生——见下面 writeHash 的那条。
  if (r.home) return "";
  const card = r.card ? "/" + r.card.split("/").map(encodeURIComponent).join("/") : "";
  return `#/s/${encodeURIComponent(r.section)}${card}${r.split ? "" : "?split=0"}`;
}

/**
 * writeHash 把位置写进地址栏。**只在真的不一样时写**：一样还写会推进一条历史，
 * 于是「后退」要点好几次才动得了（每次点完还停在同一屏）。
 */
export function writeHash(r: Route): void {
  const next = routeTo(r);
  if (location.hash === next) return;
  // 主页模式 = 空串，而地址栏里「没有 fragment」写出来是 `/ui/`、赋一个空 hash 是
  // `/ui/#`。两者是同一个地方，所以这里不写——写下去的话开屏就多一条历史记录，
  // 而那条记录点「后退」回到的是同一屏（看着像后退键坏了）。
  if (next === "" && location.hash === "") return;
  location.hash = next;
}

/**
 * fileOf 说「这个概念写的是哪一份文件」，相对配置根。
 *
 * 这是**一条约定**，不是从 Kind 推出来的：能用它，是因为两个 Kind 的数据形状里
 * 本来就带着自己那份文件的路径（`mapping-editor`/`toggles` 是 `file`，`code` 是
 * `path`）——那是渲染它们时本来就要认识的字段。同一个相对路径 = 同一份文件。
 *
 * 它不认识任何模块：`config` 这个词在这里一次都没出现（这正是「前端不认识模块」
 * 那条规矩的意思）。将来若有第三种 Kind 也要并排，就在这里补一个字段名。
 */
export function fileOf(c: { kind: string; data: unknown }): string | undefined {
  const d = c.data as { file?: unknown; path?: unknown } | null;
  const v = d?.file ?? d?.path;
  return typeof v === "string" && v ? v : undefined;
}

/**
 * 一次快捷键按下的**意思**（不是按键本身）。
 *
 * 分开的理由：按键映射住在 Shortcuts.svelte（那里也管「什么时候不该接」），而
 * 「按了之后发生什么」住在 App（只有它知道有哪些节、哪些卡）。中间过一道语义，
 * 是因为一个语义将来可能对应多个按键（`]` 与 `j` 都算「下一张」），而那种改动
 * 不该碰到 App。
 */
export type Action =
  | { kind: "save" }
  | { kind: "focus-filter" }
  | { kind: "blur" }
  | { kind: "card"; delta: 1 | -1 }
  | { kind: "section"; index: number };

/**
 * shortcutAllowed 判断这一次按键该不该被界面接走。
 *
 * 单键快捷键（`[`、`]`、`/`）在输入框里是**普通字符**。不判断就接走，等于让人
 * 在编辑配置文件时打不出一个方括号——而那是最难联想到快捷键身上的一种坏。
 * CodeMirror 的编辑器也在判据里：它是 contenteditable 的邻居，`.cm-editor`
 * 那条选择器是它自己的容器。
 */
export function shortcutAllowed(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el || typeof el.closest !== "function") return true;
  return !el.closest("input, textarea, select, [contenteditable='true'], .cm-editor");
}
