// 开发期的**假模型清单**：只挂在 `vite dev` 的中间件上（`apply: "serve"`），
// 产物里一个字都不会出现。
//
// # 为什么先有它
//
// 模型选择器是**先于后端**做出来的：它的交互（打开时问一次、模糊搜索、分组、失败
// 回落到声明清单）不该等到后端模块编好才能验。这个中间件按真后端要给的形状回答
// `POST /ui/api/ext/modellist`，于是 `pnpm dev` 里点开下拉就能看到完整行为。
//
// 形状的真相在 `src/api.ts` 的 `ModelList`；后端实现是 `modules/modellist`。
// 这里只造样例：真实上游返回的字段名各不相同，但适配器那一层会把它们收敛成这
// 一个形状——这正是前端只需要认识一种形状的原因。
import type { Plugin } from "vite";

type Mock = {
  provider: string;
  supported: boolean;
  adapter?: string;
  endpoint?: string;
  models: { id: string; name?: string; group?: string }[];
  groups?: string[];
  fetched_at?: string;
  cached?: boolean;
  note?: string;
};

function catalog(
  provider: string,
  adapter: string,
  models: { id: string; name?: string; group?: string }[],
): Mock {
  const groups: string[] = [];
  for (const m of models) if (m.group && !groups.includes(m.group)) groups.push(m.group);
  return {
    provider,
    supported: true,
    adapter,
    endpoint: `https://${provider}.example.invalid/v1/models`,
    models,
    groups: groups.length ? groups : undefined,
    fetched_at: new Date().toISOString(),
    cached: false,
  };
}

const CATALOGS: Record<string, Mock> = {
  anthropic: catalog("anthropic", "anthropic", [
    { id: "claude-fable-5", name: "Claude Fable 5", group: "claude" },
    { id: "claude-opus-5", name: "Claude Opus 5", group: "claude" },
    { id: "claude-sonnet-5", name: "Claude Sonnet 5", group: "claude" },
    { id: "claude-haiku-5", name: "Claude Haiku 5", group: "claude" },
  ]),
  deepseek: catalog("deepseek", "openai", [
    { id: "deepseek-chat", name: "DeepSeek Chat", group: "chat" },
    { id: "deepseek-reasoner", name: "DeepSeek Reasoner", group: "reasoning" },
    { id: "deepseek-v3.2", name: "DeepSeek V3.2", group: "chat" },
    { id: "deepseek-v3.2-reasoner", name: "DeepSeek V3.2 Reasoner", group: "reasoning" },
  ]),
  glm: catalog("glm", "openai", [
    { id: "glm-4.6", name: "GLM 4.6", group: "glm-4" },
    { id: "glm-4.5-air", name: "GLM 4.5 Air", group: "glm-4" },
    { id: "glm-4.5v", name: "GLM 4.5V", group: "vision" },
  ]),
  minimax: catalog("minimax", "anthropic", [
    { id: "MiniMax-M2.7-highspeed", name: "MiniMax M2.7 Highspeed", group: "minimax" },
    { id: "MiniMax-M2.7", name: "MiniMax M2.7", group: "minimax" },
  ]),
  openrouter: catalog("openrouter", "openai", [
    { id: "anthropic/claude-sonnet-5", name: "Claude Sonnet 5", group: "anthropic" },
    { id: "anthropic/claude-haiku-5", name: "Claude Haiku 5", group: "anthropic" },
    { id: "deepseek/deepseek-v3.2", name: "DeepSeek V3.2", group: "deepseek" },
    { id: "google/gemini-3-pro", name: "Gemini 3 Pro", group: "google" },
    { id: "openai/gpt-6", name: "GPT-6", group: "openai" },
  ]),
};

// 「不支持列表」也是要演的一态：下拉必须**回落到声明清单**并把原因写在顶上。
const UNSUPPORTED: Mock = {
  provider: "",
  supported: false,
  models: [],
  note: "mock: this provider has no model-list endpoint",
};

export function mockModelList(): Plugin {
  return {
    name: "newgate-mock-modellist",
    apply: "serve",
    configureServer(server) {
      // Vite 的 connect 中间件在这里只给了 `IncomingMessage` 的**最小**形状
      // （这份工程没有 @types/node，见 tsconfig 的注释）。dev-only 的假端点不需要
      // 那一整套类型，所以这里显式收窄成 any——它不是产品代码。
      server.middlewares.use("/ui/api/ext/modellist", (req: any, res: any) => {
        if (req.method !== "POST") {
          res.statusCode = 405;
          res.setHeader("Content-Type", "application/json; charset=utf-8");
          res.end(JSON.stringify({ error: "extensions use POST" }));
          return;
        }
        let raw = "";
        req.on("data", (chunk: string) => {
          raw += chunk;
          // 与真 BFF 同一条上限：请求体只有 provider/refresh 两个字段。
          if (raw.length > 1 << 20) req.destroy();
        });
        req.on("end", () => {
          let provider = "";
          try {
            provider = JSON.parse(raw || "{}").provider ?? "";
          } catch {
            /* 空体 / 坏 JSON：下面按「没点名」处理 */
          }
          res.setHeader("Content-Type", "application/json; charset=utf-8");
          if (!provider) {
            res.statusCode = 400;
            res.end(JSON.stringify({ error: "no provider named in the request (mock)" }));
            return;
          }
          // 名字里带 "-offline" 的那几家演示「不支持」：把 provider 叫成
          // `anything-offline` 即可看到回落那一态（它们不必在样例表里）。
          if (provider.endsWith("-offline")) {
            res.end(JSON.stringify({ ...UNSUPPORTED, provider }));
            return;
          }
          const found = CATALOGS[provider];
          if (!found) {
            res.statusCode = 400;
            res.end(JSON.stringify({ error: `no provider named ${provider} in providers.json (mock)` }));
            return;
          }
          res.end(JSON.stringify(found));
        });
      });
    },
  };
}
