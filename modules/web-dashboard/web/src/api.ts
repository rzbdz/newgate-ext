// BFF 客户端。**只有这四个东西**：契约版本、快照、保存、健康。
//
// 这里刻意不认识 profile、网关、计数器——它只认概念的**形状**（Kind）。加一个
// 模块的界面不需要改这个文件，正如加一个模块的界面不需要改 BFF：那条规矩的
// 前端一半就是这个文件。

export type Kind = "mapping-editor" | "code" | "toggles" | "series" | "table" | "log";

export interface Concept {
  id: string;
  kind: Kind;
  title: string;
  /** 谁贡献的（模块名）。只用来分组，前端**不需要知道那个模块是什么**。 */
  source: string;
  /** false = 只读（贡献者没给 Apply，比如带凭据的文件）。 */
  writable: boolean;
  data: any;
  /** 非空 = 这个概念此刻读不出来（文件删了、JSON 坏了）。卡片照常显示，写原因。 */
  error?: string;
}

export interface Snapshot {
  contract: number;
  generated_at: string;
  /** 后端解析出来的语言（见 i18n.ts：界面骨架上的字用它挑目录）。 */
  lang: string;
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

/** 挂在 /ui 前缀下（见 module.go 的 Prefix）。 */
const API = "api";

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
  const doc = await json<Snapshot & { error?: string }>(
    await fetch(`${API}/snapshot${q ? "?" + q : ""}`),
  );
  if (doc.error) throw new Error(doc.error);
  if (doc.contract !== CONTRACT) {
    throw new Error(
      `the interface is version ${CONTRACT}, the backend speaks ${doc.contract} — reload after rebuilding`,
    );
  }
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
