/**
 * 演示页的**假数据**。
 *
 * # 为什么形状要抄真的
 *
 * 这几坨 JSON 是直接从一份真 daemon 的 `GET /api/snapshot` 上摘下来、裁掉几条之后
 * 粘在这里的（2026-09-21）。手写一份「差不多的」不行：真组件的 props 形状变了，
 * 屏幕上的表现是**这张卡画不出来或者少了半张**，而演示页没有后端、没有测试，没有
 * 任何东西会红。抄下来的话，漂移的那天至少形状还是对的。
 *
 * # 为什么是这份数据
 *
 * 用户 2026-09-21 点名要 codex / claude 那一面。选了这三张，因为它们正好把三种
 * Kind 都占上了，而且都**点得动**：
 *
 *   codex.model      toggles    下拉就地改（档位 + 链头）
 *   codex.models     records    加一行 / 删一行 / 改档位
 *   runtime.takeover table      只读，但看着像产品
 *
 * 语言写死中文：这个演示页的观众是中文读者（站点的 `site.json` 里 English 还是
 * 未写状态）。产品那边语言由后端给（见 src/i18n.ts 的 setLang）——这里没有后端，
 * 所以下面那行 setLang 就是「后端」。
 */
import type { Concept, ConceptAction, Section, Snapshot } from "../src/api";
import { setLang } from "../src/i18n";

export const DEMO_LANG = "zh-Hans";

/** 演示里那几个动作的 id。与真模块里的取值一致（见 modules/codex/modelsview.go）。 */
export const ACTION_TAKEOVER = "mode-takeover";
export const ACTION_RENAME = "mode-rename";

const codexSection: Section = { source: "codex", title: "Codex", group: "客户端" };
const runtimeSection: Section = { source: "runtime", title: "运行时" };

export function sections(): Section[] {
  return [codexSection, runtimeSection];
}

/** 此刻的模式（演示页把它放在内存里，切一次就换一批按钮）。 */
export type Mode = "takeover" | "rename";

export function snapshot(mode: Mode): Snapshot {
  return {
    contract: 1,
    generated_at: new Date().toISOString(),
    lang: DEMO_LANG,
    sections: sections(),
    concepts: [modelConcept(mode), modelsConcept(mode), takeoverConcept()],
  };
}

/** 让真组件按中文渲染（`t()` 查不到时回落英文，所以这一步不做的话界面上会中英混着）。 */
export function useDemoLanguage(): void {
  setLang(DEMO_LANG);
}

// ---------- codex.model：档位 + 链头（KindToggles）----------

function modelConcept(mode: Mode): Concept {
  const rename = mode === "rename";
  return {
    id: "codex.model",
    kind: "toggles",
    title: "Codex 档位",
    source: "codex",
    writable: true,
    order: 20,
    data: {
      // file 那一格是**空**的：这张卡写的是 state.json 的 module_config，而它在
      // 界面上没有对应的「原文」那一半（见 modules/codex/view.go 的注释）。
      file: "",
      items: [
        {
          id: "profile",
          label: "Profile",
          kind: "select",
          value: "",
          options: ["", "ark", "kimi"],
          option_labels: { "": "ark（全局默认）" },
          why: "这个客户端从哪条链起步。链决定每一档解析到什么；改它要等下次接管（newgate on codex）才生效。",
        },
        {
          id: "model",
          label: "model",
          kind: "select",
          value: rename ? "gpt-5.6-luna" : "normal",
          options: rename
            ? ["gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5"]
            : ["heavy", "normal", "mid", "light"],
          why: "codex 跑的模型；由接管写进 config.toml",
        },
        {
          id: "review_model",
          label: "review_model",
          kind: "select",
          value: rename ? "gpt-5.5" : "normal",
          options: rename
            ? ["gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5"]
            : ["heavy", "normal", "mid", "light"],
          why: "`codex review` 跑的模型；与主模型分开",
        },
      ],
    },
  };
}

// ---------- codex.models：那张能填的映射表（KindRecords）----------

type Row = { slug: string; tier: string };

const DEFAULT_ROWS: Row[] = [
  { slug: "gpt-6-astra", tier: "heavy" },
  { slug: "gpt-5.6-sol", tier: "normal" },
  { slug: "gpt-5.6-terra", tier: "normal" },
  { slug: "gpt-5.6-luna", tier: "normal" },
  { slug: "gpt-5.5", tier: "light" },
];

const TIERS = ["", "heavy", "normal", "mid", "light", "vision"];

function modelsConcept(mode: Mode): Concept {
  return {
    id: "codex.models",
    kind: "records",
    title: "Codex 模型名",
    source: "codex",
    writable: true,
    order: 21,
    data: {
      file: "codex-models.json",
      base: "demo",
      can_add: true,
      add_label: "+ 模型名",
      items: DEFAULT_ROWS.map(record),
    },
    note: {
      text:
        mode === "rename"
          ? "改名模式：codex 自己的模型名映射到档位"
          : "接管模式：一个档位名被写进 config.toml",
      tone: "ok",
    },
    actions: [
      mode === "rename"
        ? { id: ACTION_TAKEOVER, label: "接管模式" }
        : { id: ACTION_RENAME, label: "改名模式" },
    ],
  };
}

function record(r: Row) {
  return {
    id: r.slug,
    label: r.slug,
    removable: true,
    fields: [
      {
        id: "slug",
        label: "codex 模型名",
        kind: "text",
        value: r.slug,
        placeholder: "例如 gpt-5.6-luna",
        why: "codex 自己发的那个模型名——从 `codex debug models` 里抄（那个 slug）。",
      },
      {
        id: "tier",
        label: "档位",
        kind: "select",
        value: r.tier,
        options: TIERS,
        why: "点名这个模型的请求落到哪一档。必填——没有档位的那一行保存时会被拒。",
      },
    ],
  };
}

// ---------- runtime.takeover：谁在走 newgate（KindTable）----------

function takeoverConcept(): Concept {
  return {
    id: "runtime.takeover",
    kind: "table",
    title: "谁在走 newgate",
    source: "runtime",
    writable: false,
    data: {
      columns: [
        { id: "agent", label: "agent" },
        { id: "state", label: "状态" },
        { id: "mechanism", label: "机制" },
        { id: "detail", label: "怎么接管的" },
      ],
      rows: [
        {
          cells: {
            agent: { text: "claude" },
            state: { text: "生效", tone: "ok" },
            mechanism: { text: "shim" },
            detail: { text: "PATH 上有一个同名入口，环境变量已注入" },
          },
        },
        {
          cells: {
            agent: { text: "codex" },
            state: { text: "生效", tone: "ok" },
            mechanism: { text: "config" },
            detail: { text: "改写了 ~/.codex/config.toml（原文件已备份）" },
          },
        },
        {
          cells: {
            agent: { text: "opencode" },
            state: { text: "没装" },
            mechanism: { text: "-" },
            detail: { text: "-" },
          },
        },
      ],
    },
  };
}

/** 演示页假装跑了一个动作：把模式翻过去，并把链头那一格跟着换掉。 */
export function actionOf(c: Concept): ConceptAction | undefined {
  return c.actions?.[0];
}
