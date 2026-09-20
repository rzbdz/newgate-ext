// BFF 客户端。**只有这四个东西**：契约版本、快照、保存、健康。
//
// 这里刻意不认识 profile、网关、计数器——它只认概念的**形状**（Kind）。加一个
// 模块的界面不需要改这个文件，正如加一个模块的界面不需要改 BFF：那条规矩的
// 前端一半就是这个文件。

export type Kind =
  | "mapping-editor"
  | "code"
  | "toggles"
  | "records"
  | "series"
  | "table"
  | "log";

export interface Concept {
  id: string;
  kind: Kind;
  title: string;
  /** 谁贡献的（模块名）。只用来分组，前端**不需要知道那个模块是什么**。 */
  source: string;
  /** false = 只读（贡献者没给 Apply，比如带凭据的文件）。 */
  writable: boolean;
  /** true = 这一面会自己变，跟着那几秒一次的刷新走（贡献者声明，见 lib/view 的 Live）。
      不声明就不刷——「读一次贵不贵」只有贡献者知道，不能从 Kind 猜。 */
  live?: boolean;
  /** 导航里的分组（见 lib/view 的 Concept.Group）：**两级**，用 "/" 分开——
      前一段是大档（`档位`），后一段是族（`claude`）。空 = 不归任何一档。
      标题画不画由 TabStrip 按「这一段底下有没有 ≥2 张卡」决定。
      **只影响排列**，不参与任何身份判断（路由与草稿都按 id 走）。 */
  group?: string;
  /** true = 这张卡能拿**同一份文件另一半的草稿**问一句「我该显示成什么样」
      （见 lib/view 的 Concept.Preview 与 api.ts 的 preview）。只有一半能问是
      常态：原文那一半自己就是原文，没有第二半。 */
  previewable?: boolean;
  data: any;
  /** 非空 = 这个概念此刻读不出来（文件删了、JSON 坏了）。卡片照常显示，写原因。 */
  error?: string;
}

/**
 * 侧栏的一栏：机器标记 + 给人看的名字。
 *
 * 名字由**各模块在登记时报**（后端的 `view.Title`），不在这里硬编码，也不从
 * 来源名猜——名字住在界面里的话，加一个模块就得改一次界面，「前端不认识模块」
 * 那条规矩就没了。前端只负责把它画出来。
 *
 * 它**不走 t()**：那是模块的内容（与概念标题同一类），后端已经按当时的语言翻好
 * 了。t() 是界面自己的字（按钮、提示）。
 */
export interface Section {
  source: string;
  title: string;
}

export interface Snapshot {
  contract: number;
  generated_at: string;
  /** 后端解析出来的语言（见 i18n.ts：界面骨架上的字用它挑目录）。 */
  lang: string;
  /** 侧栏的栏目表：**登记过的全部来源**，含此刻一条概念都产不出来的那些。 */
  sections: Section[];
  concepts: Concept[];
}

/** 冲突：两边原文都在，摆出来让人决定。 */
export interface Conflict {
  concept: string;
  path: string;
  base: string;
  current: string;
  yours: string;
  theirs: string;
}

export interface ApplyResult {
  ok?: boolean;
  base?: string;
  conflict?: Conflict;
  error?: string;
}

/** 与后端 Contract 常量对齐。前端**必须先看它**，不认识的版本宁可白屏报一句。 */
export const CONTRACT = 1;

/**
 * 运行期的接口地址，从**构建期的 base** 推出来（vite.config.ts 的 base 是
 * "/ui/"，所以这里恒等于 "/ui/api"）。
 *
 * 为什么不是相对路径 `"api"`（这里原来是那么写的）：相对地址的解析结果取决于
 * **当前文档**的路径，而那是会变的——用户敲 `/ui` 不带尾斜杠就是一个。实测：
 * 从 `/ui` 出发，`api/snapshot` 被解析成 `/api/snapshot`，那个路径没被挂载，
 * 于是落进数据面的 catch-all，被当成一次形状不对的转发请求（400，而且计进
 * 数据面流量）。页面本身画得出来（资源的 URL 是绝对的），所以症状是「界面
 * 好好的、一操作就报错」——最费解的那一类。
 *
 * 绝对地址把这条路堵死：文档在哪一层都不影响接口地址。（module.go 那边同时做了
 * `/ui` → `/ui/` 的重定向，那是给人用的 URL 该有的样子；两条各自解决一半。）
 */
const API = `${import.meta.env.BASE_URL}api`;

/**
 * 把后端回来的 `error` 变成一个能看的字符串。
 *
 * 那里**不保证是字符串**：BFF 自己回的确实是字符串，但请求没落到 BFF 上时
 * （地址算错、前缀改了、端口上根本不是 newgate），对面可能是数据面转上来的
 * 上游错误——一个 `{"type":"error","error":{…}}` 的对象。`new Error(obj)` 会把
 * 它渲染成 `[object Object]`，横幅上写着这么一句，排查的人连去看哪里都不知道。
 */
function errText(v: unknown): string {
  if (typeof v === "string") return v;
  if (v == null) return "";
  if (typeof v === "object") {
    const o = v as { error?: unknown; message?: unknown };
    if (typeof o.message === "string") return o.message;
    if (o.error !== undefined && o.error !== v) {
      const inner = errText(o.error);
      if (inner) return inner;
    }
    try {
      return JSON.stringify(v);
    } catch {
      return String(v);
    }
  }
  return String(v);
}

async function json<T>(res: Response): Promise<T> {
  const text = await res.text();
  try {
    return JSON.parse(text) as T;
  } catch {
    // 上游不是 JSON（反代塞了一张错误页、或者端口上根本不是 newgate）。把原文
    // 前几百字节带出来——「Unexpected token <」对面是什么，只有看的人能判断。
    throw new Error(`${res.status} ${res.statusText}: ${text.slice(0, 300)}`);
  }
}

/**
 * 读一次快照。`sources` 给出时**只问这几位贡献者**。
 *
 * 自动刷新走这条路：计数器与日志的产出是廉价的，而配置那一位要重读并重新解析
 * 每一份 profile 与每一个源文件。不带参数 = 全部（首次加载要的就是全部）。
 */
export async function snapshot(sources?: string[]): Promise<Snapshot> {
  const q = (sources ?? []).map((s) => `source=${encodeURIComponent(s)}`).join("&");
  const doc = await json<Snapshot & { error?: unknown }>(
    await fetch(`${API}/snapshot${q ? "?" + q : ""}`),
  );
  if (doc.error) throw new Error(errText(doc.error));
  if (doc.contract !== CONTRACT) {
    throw new Error(
      `the interface is version ${CONTRACT}, the backend speaks ${doc.contract} — reload after rebuilding`,
    );
  }
  // 老后端不认识 sections（见 CONTRACT 的注释：这个字段是**加**出来的，不动版本
  // 号）。那种后端下侧栏回落到来源名——难看，但页面照常能用。
  doc.sections ??= [];
  return doc;
}

/** 保存一个概念的修改。base 是加载时拿到的那份（内容哈希）。 */
export async function apply(id: string, base: string, edit: unknown): Promise<ApplyResult> {
  return json<ApplyResult>(
    await fetch(`${API}/apply`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id, base, edit }),
    }),
  );
}

export interface PreviewResult {
  /** 这个概念在「文件长这样」时该显示的数据（形状与快照里的 data 一样）。 */
  data?: unknown;
  /** 草稿还解析不出来（**打字途中的常态**，不是故障）：界面保持上一次的样子。 */
  error?: string;
}

/**
 * 问一句：「这份文件**还没落盘的草稿**长这样时，这张卡该显示成什么样？」
 *
 * 用途只有一处，但很要命：一份文件的两半（控件 + 原文）都能改。用户在原文里粘了
 * 一整份档位、再去动一个下拉框——控件那一半手里还是**改之前**那份盘上内容，它交
 * 上去的是整份旧表，刚粘的东西当场没了。有了这一问，两半始终说的是同一份内容。
 *
 * 解析是**贡献者**的事（那份文件的格式是它的知识），所以这里只搬字节。
 */
export async function preview(id: string, text: string): Promise<PreviewResult> {
  return json<PreviewResult>(
    await fetch(`${API}/preview`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id, text }),
    }),
  );
}
