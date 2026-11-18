// A **fake model list** for development: mounted only on the `vite dev` middleware
// (`apply: "serve"`), not one word of it appears in the output.
//
// # Why it came first
//
// The model selector was built **before the backend**: its interactions (ask once on open,
// fuzzy search, grouping, fall back to the declared list on failure) should not have to wait
// for the backend module to be built to be verified. This middleware answers
// `POST /ui/api/ext/modellist` in the shape the real backend would give, so opening the
// dropdown under `pnpm dev` shows the complete behavior.
//
// The truth about the shape is in `ModelList` in `src/api.ts`; the backend implementation is
// `modules/modellist`. This only fabricates samples: real upstreams return all sorts of field
// names, but the adapter layer converges them into this one shape -- which is exactly why the
// front end only needs to know one shape.
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

// "Does not support listing" is a state that must be demonstrated too: the dropdown has to
// **fall back to the declared list** and write the reason at the top.
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
      // Vite's connect middleware only gives the **minimal** shape of IncomingMessage here
      // (this project has no @types/node, see the comment in tsconfig). A dev-only fake
      // endpoint does not need that whole set of types, so it is explicitly narrowed to any
      // here -- it is not product code.
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
          // Same cap as the real BFF: the request body only has the two fields
          // provider/refresh.
          if (raw.length > 1 << 20) req.destroy();
        });
        req.on("end", () => {
          let provider = "";
          try {
            provider = JSON.parse(raw || "{}").provider ?? "";
          } catch {
            /* empty body / bad JSON: handled below as "no provider named" */
          }
          res.setHeader("Content-Type", "application/json; charset=utf-8");
          if (!provider) {
            res.statusCode = 400;
            res.end(JSON.stringify({ error: "no provider named in the request (mock)" }));
            return;
          }
          // The ones whose names end in "-offline" demonstrate "unsupported": call the
          // provider `anything-offline` to see the fallback state (they need not be in the
          // sample table).
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
