/**
 * 演示页的**假数据**：一份完整的 `Snapshot`。
 *
 * # 为什么形状要抄真的
 *
 * 下面这些形状不是「差不多」，是**照着这台机器上那份真快照**（`GET /api/snapshot`，
 * 2026-09-22）与 `modules/` 下各模块真实的产出代码写下来的。手写一份「差不多的」
 * 不行：真组件的 props 形状变了，屏幕上的表现是**这张卡画不出来或者少了半张**，
 * 而演示页没有后端、没有测试，没有任何东西会红。抄下来的话，漂移的那天至少形状
 * 还是对的。
 *
 * 有一处不是抄的、是**算出来的**：`home.chains` 那张链卡（文件末尾那一大段）。
 * 它的数据是拿内核自己的解析器（`modules/config/store` + `resolve.BuildChain`）
 * 对着本机 12 份 profile 跑出来的——链的展开（跨 profile 的 fallback、去重、跳过
 * 计数）是解析器的结论，手写一定会写成一个**看起来合理但算不出来**的样子，而那
 * 正好是这张卡要展示的东西。
 *
 * # 节与概念
 *
 * 五节，照 `modules/` 下真实的模块会报的来：配置（`config`）、网关（`gateway`）、
 * 客户端（`codex`）、聚合主页（`home`）、接管（`runtime`）。首屏落在 `home`——
 * 它是唯一带了 `default: true` 的那一节（见 `lib/view` 的 `Section.Default`）。
 *
 * 数据里出现的 provider 名与模型名（`smt-deepseek` / `gpt-5.6-sol` …）是**这台机器
 * 那份配置里的名字**，不是真实厂商的地址或凭据：`providers.json` 里的 key 与 base URL
 * 一个字都没抄过来，那些东西不该出现在一个公开站的演示页上。
 *
 * # 语言
 *
 * 语言写死中文：这个演示页的观众是中文读者（站点的 `site.json` 里 English 还是
 * 未写状态）。产品那边语言由后端给（见 src/i18n.ts 的 setLang）——这里没有后端，
 * 所以下面那行 setLang 就是「后端」。
 */
import type { Concept, Section, Snapshot } from "../src/api";
import { setLang } from "../src/i18n";

export const DEMO_LANG = "zh-Hans";

// ---------- 节 ----------

const configSection: Section = {
  source: "config",
  title: "配置",
  // 这一节的动作照真模块给（见 core/modules/config/view.go 的 registerView）。
  actions: [
    { id: "new-profile", label: "＋新建档位" },
    { id: "apply-default-all", label: "把默认应用到全部客户端" },
  ],
};
const gatewaySection: Section = {
  source: "gateway",
  title: "网关",
  group: "数据面",
};
const codexSection: Section = {
  source: "codex",
  title: "Codex",
  group: "客户端",
};
/** 聚合主页：首屏落在它上面（唯一带 Default 的一节，见 lib/view 的 Section.Default）。 */
const homeSection: Section = { source: "home", title: "主页", default: true };
const runtimeSection: Section = {
  source: "runtime",
  title: "接管",
  group: "运行",
};

export function sections(): Section[] {
  return [
    configSection,
    gatewaySection,
    codexSection,
    homeSection,
    runtimeSection,
  ];
}

/** 这台机器上那 12 份 profile 的名字（按名字；`ds` 是生效的那一份）。 */
const PROFILES = [
  "ark",
  "cheap",
  "claude",
  "complex",
  "ds",
  "expensive",
  "gemini",
  "glm",
  "gpt",
  "kimi",
  "minimax",
  "top-only",
];

/** 一份 profile 的名字与描述（见 ~/.config/newgate/mappings/*.kv 的 `desc=`）。 */
const PROFILE_DESC: Record<string, string> = {
  ark: "火山引擎",
  cheap: "省钱优先，可以往上打",
  claude: "假 Claude",
  complex: "美国混搭",
  ds: "DeepSeek",
  expensive: "最贵优先（固定 + 排除）",
  gemini: "Gemini",
  glm: "智谱",
  gpt: "GPT 家族",
  kimi: "月之暗面",
  minimax: "MiniMax（全档 M3）",
  "top-only": "稀疏层：只覆盖 heavy",
};
/** 三份 profile 写的是 .json（其余是 .kv）——文件的写法是配置目录里的事实。 */
const PROFILE_FILE: Record<string, string> = {
  cheap: "mappings/cheap.json",
  expensive: "mappings/expensive.json",
  "top-only": "mappings/top-only.json",
};
const profileFile = (name: string) =>
  PROFILE_FILE[name] ?? `mappings/${name}.kv`;

/** 档位，按能力从高到低（照 `domain.Roles`）。 */
const TIERS = ["heavy", "normal", "mid", "light", "vision"];

/** provider 名单（按名字）。 */
const PROVIDERS = [
  "ark",
  "kimi",
  "minimax",
  "smt-claude",
  "smt-codex",
  "smt-deepseek",
  "smt-gemini",
  "smt-glm",
];

/** 每家的模型清单（照真 providers.json 里那份，去掉凭据与地址）。 */
const PROVIDER_MODELS: Record<string, string[]> = {
  ark: ["ark-code-latest", "doubao-seed-2.0-lite"],
  kimi: ["kimi-k2.7-code", "kimi-k3"],
  minimax: ["MiniMax-M3"],
  "smt-claude": ["claude-opus-5", "claude-sonnet-4-6", "claude-sonnet-5"],
  "smt-codex": ["gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"],
  "smt-deepseek": ["deepseek-flash"],
  "smt-gemini": ["gemini-3.1-flash-lite-preview", "gemini-3.1-pro-preview"],
  "smt-glm": ["glm-5.3", "glm-5.3-flash"],
};

// ---------- 快照 ----------

/**
 * 一份完整的快照。**没有参数**（不像从前那份要在两种模式之间切）：演示页现在铺开
 * 的是整个前端的样子，模式那种「一屏里的两态」不再有地方挂——它真在产品里也是
 * `codex` 那一节里的一张卡，不在这里。
 */
export function snapshot(): Snapshot {
  return {
    contract: 1,
    generated_at: new Date().toISOString(),
    lang: DEMO_LANG,
    sections: sections(),
    concepts: [
      stateConcept(),
      providersConcept(),
      profileConcept("ds"),
      profileFileConcept("ds"),
      metricsConcept(),
      specialConcept(),
      codexModelConcept(),
      codexModelsConcept(),
      homeChainsConcept(),
      takeoverConcept(),
    ],
  };
}

/** 让真组件按中文渲染（`t()` 查不到时回落英文，所以这一步不做的话界面上会中英混着）。 */
export function useDemoLanguage(): void {
  setLang(DEMO_LANG);
}

// ---------- config.state：档位与上游（KindToggles）----------

function stateConcept(): Concept {
  return {
    id: "config.state",
    kind: "toggles",
    title: "全局设置",
    source: "config",
    writable: true,
    order: 0,
    data: {
      file: "state.json",
      base: "demo",
      items: [
        {
          id: "default_profile",
          label: "默认 profile",
          kind: "select",
          value: "ds",
          options: PROFILES,
          why: "没有为某个 agent 单独指定时，链从哪个 profile 开始。",
        },
        {
          id: "host",
          label: "监听地址",
          kind: "text",
          value: "127.0.0.1",
          placeholder: "留空 = 只监听本机回环（127.0.0.1）",
          why: "填一个 IP（比如 Tailscale 的 100.x 地址），或 0.0.0.0 表示所有网卡。留空 = 只监听回环。改完要重启才生效。",
        },
      ],
    },
  };
}

// ---------- config.providers：上游（KindRecords）----------

function providersConcept(): Concept {
  return {
    id: "config.providers",
    kind: "records",
    title: "上游（provider）",
    source: "config",
    writable: true,
    order: 1,
    data: {
      file: "providers.json",
      base: "demo",
      can_add: true,
      add_label: "＋新增上游",
      items: PROVIDERS.map((name) => ({
        id: name,
        label: name,
        removable: true,
        fields: [
          {
            id: "name",
            label: "名字",
            kind: "text",
            value: name,
            why: "档位绑定引用的就是这个名字。改名等于换了新的一家——原来绑在旧名字上的那些还指着旧的。",
          },
          {
            id: "protocol",
            label: "协议",
            kind: "select",
            value: "openai",
            options: ["", "openai", "anthropic"],
            why: "这家上游说的是哪种方言。留空 = openai。",
          },
          {
            id: "base_url",
            label: "base URL",
            kind: "text",
            value: "https://gw.example.com/v1",
            placeholder: "https://…",
            why: "请求发到哪。后面的路径由 newgate 拼。",
          },
          {
            id: "api_key_env",
            label: "从环境变量取 key",
            kind: "text",
            value: `NEWGATE_KEY_${name.toUpperCase().replace(/-/g, "_")}`,
            placeholder: "例如 DEEPSEEK_API_KEY",
            why: "从环境变量读 key，而不是写在文件里。填了它就压过别处那一格。",
          },
          {
            id: "models",
            label: "模型清单",
            kind: "lines",
            value: (PROVIDER_MODELS[name] ?? []).join("\n"),
            placeholder: "一行一个",
            why: "这家能提供的模型名。客户端点名要某个模型时，候选就是从这些里面挑。",
          },
        ],
      })),
    },
  };
}

// ---------- config.profile.ds：一份档位文件（KindMappingEditor + KindCode）----------

/**
 * 12 份里只摆 `ds` 一份：它是**生效的那一份**（`state.json` 的 `default_profile`），
 * 也正是 `home.chains` 里排在最前的那张卡。两半都画（控件 + 原文）——那是这一节
 * 十张卡里唯一一对**同一份文件的两半**，正是 `SplitView` 要演示的东西。
 */
function profileConcept(name: string): Concept {
  return {
    id: `config.profile.${name}`,
    kind: "mapping-editor",
    title: `${name} — ${PROFILE_DESC[name]}`,
    source: "config",
    writable: true,
    group: `档位/${name}`,
    order: 10,
    note: { text: "当前配置", tone: "ok" },
    data: {
      profile: name,
      file: profileFile(name),
      base: "demo",
      description: PROFILE_DESC[name],
      default: true,
      extends_options: ["", ...PROFILES.filter((p) => p !== name)],
      // ds 的原文：heavy/mid/light/vision 四条都指向 smt-deepseek。normal 没写——
      // 它走内置别名落到 mid（见 core/modules/config/domain 的 builtinAliases）。
      roles: [
        { id: "heavy", bindings: [binding("smt-deepseek", "deepseek-flash")] },
        { id: "mid", bindings: [binding("smt-deepseek", "deepseek-flash")] },
        { id: "light", bindings: [binding("smt-deepseek", "deepseek-flash")] },
        { id: "vision", bindings: [binding("smt-deepseek", "deepseek-flash")] },
      ],
      providers: PROVIDERS.map((p) => ({
        name: p,
        protocol: "openai",
        base_url: "https://gw.example.com/v1",
        has_key: true,
        models: PROVIDER_MODELS[p],
      })),
    },
  };
}

/** 同一份文件的原文那一半（KindCode）。两半靠 `file` 配对，见 nav.ts 的 fileOf。 */
function profileFileConcept(name: string): Concept {
  return {
    id: `config.file.mappings/${name}.kv`,
    kind: "code",
    title: profileFile(name),
    source: "config",
    writable: true,
    order: 20,
    data: {
      path: profileFile(name),
      language: "kv",
      base: "demo",
      text: [
        `desc=${PROFILE_DESC[name]}`,
        "prio=19",
        "window=1000000",
        "compact=500000",
        "heavy=smt-deepseek/deepseek-flash",
        "mid=smt-deepseek/deepseek-flash",
        "light=smt-deepseek/deepseek-flash",
        "vision=smt-deepseek/deepseek-flash",
        "",
      ].join("\n"),
    },
  };
}

function binding(provider: string, model: string) {
  return { provider, model };
}

// ---------- gateway.metrics：计数器（KindSeries）----------

function metricsConcept(): Concept {
  return {
    id: "gateway.metrics",
    kind: "series",
    title: "计数",
    source: "gateway",
    writable: false,
    live: true,
    order: 0,
    data: {
      groups: [
        {
          id: "chain",
          label: "链",
          counters: [
            { name: "chain.budget_exhausted", value: 3, hint: "链总预算用尽" },
            {
              name: "chain.failover",
              value: 100,
              hint: "前序候选失败，换到后续候选后成功",
            },
            {
              name: "chain.step_failed",
              value: 134,
              hint: "链上某站失败（连接 / 可转移错误）",
            },
          ],
        },
        {
          id: "requests",
          label: "请求",
          counters: [
            { name: "requests.total", value: 2543, hint: "进入网关的请求" },
          ],
        },
        {
          id: "timeout",
          label: "超时",
          counters: [
            {
              name: "timeout.first_byte.non_stream",
              value: 34,
              hint: "首字节超时（非流式），沿链下移",
            },
          ],
        },
      ],
      total: 5326,
    },
  };
}

// ---------- gateway.special：上游怪癖补丁（KindTable）----------

function specialConcept(): Concept {
  return {
    id: "gateway.special",
    kind: "table",
    title: "上游怪癖补丁",
    source: "gateway",
    writable: false,
    live: true,
    order: 0,
    data: {
      columns: [
        { id: "state", label: "状态" },
        { id: "plugin", label: "插件" },
        { id: "why", label: "为什么存在" },
      ],
      rows: [
        {
          cells: {
            state: { text: "生效", tone: "ok" },
            plugin: { text: "claude-bg" },
            why: {
              text: "Claude Code 的后台非流式请求（Bash 分类器等）不带 thinking，国模却默认思考 → 15-30 秒、成波超时卡死会话",
            },
          },
        },
        {
          cells: {
            state: { text: "生效", tone: "ok" },
            plugin: { text: "codex-deepseek" },
            why: {
              text: "Codex 把工具定义放在 input[0]（additional_tools）里，而 DeepSeek 只从顶层 tools 读工具；不抬上去模型一个工具都看不见，会用它自己的文本格式作答，客户端因此一个 tool_result 都拿不到",
            },
          },
        },
        {
          cells: {
            state: { text: "生效", tone: "ok" },
            plugin: { text: "deepseek" },
            why: {
              text: "DeepSeek 思考模式要求逐字回传推理内容，客户端却会把它剥掉 → 400",
            },
          },
        },
        {
          cells: {
            state: { text: "没装" },
            plugin: { text: "glm" },
            why: {
              text: "GLM 系模型把「没写 thinking」当默认开思考（Anthropic 语义是关）→ 没要求思考的请求被拖进十几秒",
            },
          },
        },
      ],
    },
  };
}

// ---------- codex.model：档位 + 链头（KindToggles）----------

function codexModelConcept(): Concept {
  return {
    id: "codex.model",
    kind: "toggles",
    title: "Codex 档位",
    source: "codex",
    writable: true,
    order: 20,
    data: {
      // file 那一格空着：这张卡写的是 state.json 的 module_config，而它在界面上
      // 没有对应的「原文」那一半（见 modules/codex/view.go 的注释）。
      file: "",
      items: [
        {
          id: "profile",
          label: "Profile",
          kind: "select",
          value: "",
          options: ["", ...PROFILES],
          option_labels: { "": "ds（缺省）" },
          why: "这个客户端从哪条链起步。链决定每个档位最终落到哪家模型上；改完要等下次接管这个客户端才生效（newgate on codex）。 · 跟随全局缺省（ds）",
        },
        {
          id: "model",
          label: "model",
          kind: "select",
          value: "normal",
          options: TIERS,
          why: "codex 跑的模型；由接管写进 config.toml",
        },
        {
          id: "review_model",
          label: "review_model",
          kind: "select",
          value: "normal",
          options: TIERS,
          why: "`codex review` 跑的模型；与主模型分开",
        },
      ],
    },
  };
}

// ---------- codex.models：那张能填的映射表（KindRecords）----------

const CODEX_ROWS = [
  { slug: "gpt-6-astra", tier: "heavy" },
  { slug: "gpt-5.6-sol", tier: "normal" },
  { slug: "gpt-5.6-terra", tier: "normal" },
  { slug: "gpt-5.6-luna", tier: "normal" },
  { slug: "gpt-5.5", tier: "light" },
];

function codexModelsConcept(): Concept {
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
      add_label: "＋模型名",
      items: CODEX_ROWS.map((r) => ({
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
            why: "codex 自己发出来的那个模型名——从 `codex debug models` 里抄（抄 slug 那一栏）。",
          },
          {
            id: "tier",
            label: "档位",
            kind: "select",
            value: r.tier,
            options: ["", ...TIERS],
            why: "点名这个模型的请求落到哪一档。必填——没有档位的行保存时会被拒。",
          },
        ],
      })),
    },
    note: { text: "接管模式：一个档位名被写进 config.toml", tone: "ok" },
  };
}

// ---------- home.chains：一屏摊开的候选链（KindChains，**主角**）----------

/**
 * 12 份 profile，`ds` 在最前（它是此刻生效的那一份），每张卡里五档。
 *
 * **这一份数据是用内核对本机那份配置跑出来的**，没有一行是手写的：每一条链就是
 * `resolve.BuildChain` 的结论。所以能看到真正的跨 profile fallback——`ds` 的重档
 * 走不动时先去 `ark`，再去 `glm`、`minimax`、`kimi`… 一路到 `gpt`，甚至落到
 * `complex` 里写着的那个 `smt-codex/gpt-5.6-terra`。那些站带着出处（`profile`），
 * 渲染器会把它画成一个标记。
 *
 * 行的身份（`id`）与后端给的一样是 `profile/档位`（见 `view.ChainRow.ID`：档位名
 * 单独一个在 12 份文件里会出现 12 次，光有档位名定位不到一行）。`actions` 照
 * `chainActions` 的规矩：**第一个候选不给按钮**（它就是此刻的链头，点了不改变任何
 * 事），从第二个开始每站一个，label 就是那个 `provider/model` 本身。
 */
function homeChainsConcept(): Concept {
  return {
    id: "home.chains",
    kind: "chains",
    title: "候选链",
    source: "home",
    writable: false,
    order: 0,
    data: { cards: CHAIN_CARDS },
  };
}

/** 一趟链：`[provider, model, 来自哪份 profile?]`。第三项省略 = 本站就是本卡的 profile。 */
type Stop = [string, string, string?];

type ChainCard = {
  profile: string;
  file?: string;
  default?: boolean;
  roles: {
    id: string;
    tier: string;
    head?: string;
    steps?: Stop[];
    note?: string;
    tone?: string;
    actions?: { id: string; label: string }[];
  }[];
};

const CHAIN_CARDS: ChainCard[] = [
  {
    profile: "ds",
    file: "mappings/ds.kv",
    default: true,
    roles: [
      {
        id: "ds/heavy",
        tier: "heavy",
        head: "smt-deepseek/deepseek-flash",
        steps: [
          ["smt-deepseek", "deepseek-flash"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "ds/normal",
        tier: "normal",
        head: "smt-deepseek/deepseek-flash",
        steps: [
          ["smt-deepseek", "deepseek-flash"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-luna", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
      {
        id: "ds/mid",
        tier: "mid",
        head: "smt-deepseek/deepseek-flash",
        steps: [
          ["smt-deepseek", "deepseek-flash"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "ds/light",
        tier: "light",
        head: "smt-deepseek/deepseek-flash",
        steps: [
          ["smt-deepseek", "deepseek-flash"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-4-6", "claude"],
          ["smt-codex", "gpt-5.6-luna", "complex"],
          ["smt-gemini", "gemini-3.1-flash-lite-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "ds/vision",
        tier: "vision",
        head: "smt-deepseek/deepseek-flash",
        steps: [
          ["smt-deepseek", "deepseek-flash"],
          ["ark", "doubao-seed-2.0-lite", "ark"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "complex"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
    ],
  },

  {
    profile: "ark",
    file: "mappings/ark.kv",
    roles: [
      {
        id: "ark/heavy",
        tier: "heavy",
        head: "ark/ark-code-latest",
        steps: [
          ["ark", "ark-code-latest"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "ark/normal",
        tier: "normal",
        head: "ark/ark-code-latest",
        steps: [
          ["ark", "ark-code-latest"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-luna", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
      {
        id: "ark/mid",
        tier: "mid",
        head: "ark/ark-code-latest",
        steps: [
          ["ark", "ark-code-latest"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "ark/light",
        tier: "light",
        head: "ark/ark-code-latest",
        steps: [
          ["ark", "ark-code-latest"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-4-6", "claude"],
          ["smt-codex", "gpt-5.6-luna", "complex"],
          ["smt-gemini", "gemini-3.1-flash-lite-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "ark/vision",
        tier: "vision",
        head: "ark/doubao-seed-2.0-lite",
        steps: [
          ["ark", "doubao-seed-2.0-lite"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "complex"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
    ],
  },

  {
    profile: "cheap",
    file: "mappings/cheap.json",
    roles: [
      {
        id: "cheap/heavy",
        tier: "heavy",
        head: "ark/ark-code-latest",
        steps: [
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 5 个候选",
        actions: [
          {
            id: "head:upstream-b/model-large",
            label: "upstream-b/model-large",
          },
        ],
      },
      {
        id: "cheap/normal",
        tier: "normal",
        head: "ark/ark-code-latest",
        steps: [
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-luna", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
      {
        id: "cheap/mid",
        tier: "mid",
        head: "ark/ark-code-latest",
        steps: [
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
        actions: [
          {
            id: "head:upstream-b/model-medium",
            label: "upstream-b/model-medium",
          },
        ],
      },
      {
        id: "cheap/light",
        tier: "light",
        head: "ark/ark-code-latest",
        steps: [
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-4-6", "claude"],
          ["smt-codex", "gpt-5.6-luna", "complex"],
          ["smt-gemini", "gemini-3.1-flash-lite-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
        actions: [
          {
            id: "head:upstream-b/model-small",
            label: "upstream-b/model-small",
          },
        ],
      },
      {
        id: "cheap/vision",
        tier: "vision",
        head: "ark/doubao-seed-2.0-lite",
        steps: [
          ["ark", "doubao-seed-2.0-lite", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "complex"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
    ],
  },

  {
    profile: "claude",
    file: "mappings/claude.kv",
    roles: [
      {
        id: "claude/heavy",
        tier: "heavy",
        head: "smt-claude/claude-opus-5",
        steps: [
          ["smt-claude", "claude-opus-5"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "claude/normal",
        tier: "normal",
        head: "smt-claude/claude-sonnet-5",
        steps: [
          ["smt-claude", "claude-sonnet-5"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-luna", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
      {
        id: "claude/mid",
        tier: "mid",
        head: "smt-claude/claude-sonnet-5",
        steps: [
          ["smt-claude", "claude-sonnet-5"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "claude/light",
        tier: "light",
        head: "smt-claude/claude-sonnet-4-6",
        steps: [
          ["smt-claude", "claude-sonnet-4-6"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-codex", "gpt-5.6-luna", "complex"],
          ["smt-gemini", "gemini-3.1-flash-lite-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "claude/vision",
        tier: "vision",
        head: "smt-claude/claude-opus-5",
        steps: [
          ["smt-claude", "claude-opus-5"],
          ["ark", "doubao-seed-2.0-lite", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-gemini", "gemini-3.1-pro-preview", "complex"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
    ],
  },

  {
    profile: "complex",
    file: "mappings/complex.kv",
    roles: [
      {
        id: "complex/heavy",
        tier: "heavy",
        head: "smt-claude/claude-opus-5",
        steps: [
          ["smt-claude", "claude-opus-5"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "complex/normal",
        tier: "normal",
        head: "smt-codex/gpt-5.6-terra",
        steps: [
          ["smt-codex", "gpt-5.6-terra"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-luna", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
      {
        id: "complex/mid",
        tier: "mid",
        head: "smt-codex/gpt-5.6-terra",
        steps: [
          ["smt-codex", "gpt-5.6-terra"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "complex/light",
        tier: "light",
        head: "smt-codex/gpt-5.6-luna",
        steps: [
          ["smt-codex", "gpt-5.6-luna"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-4-6", "claude"],
          ["smt-gemini", "gemini-3.1-flash-lite-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "complex/vision",
        tier: "vision",
        head: "smt-gemini/gemini-3.1-pro-preview",
        steps: [
          ["smt-gemini", "gemini-3.1-pro-preview"],
          ["ark", "doubao-seed-2.0-lite", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
    ],
  },

  {
    profile: "expensive",
    file: "mappings/expensive.json",
    roles: [
      {
        id: "expensive/heavy",
        tier: "heavy",
        note: "跳过 13 个候选",
        tone: "bad",
        actions: [
          {
            id: "head:upstream-a/model-large",
            label: "upstream-a/model-large",
          },
        ],
      },
      {
        id: "expensive/normal",
        tier: "normal",
        note: "跳过 12 个候选",
        tone: "bad",
      },
      { id: "expensive/mid", tier: "mid", note: "跳过 12 个候选", tone: "bad" },
      {
        id: "expensive/light",
        tier: "light",
        note: "跳过 12 个候选",
        tone: "bad",
      },
      {
        id: "expensive/vision",
        tier: "vision",
        note: "跳过 12 个候选",
        tone: "bad",
      },
    ],
  },

  {
    profile: "gemini",
    file: "mappings/gemini.kv",
    roles: [
      {
        id: "gemini/heavy",
        tier: "heavy",
        head: "smt-gemini/gemini-3.1-pro-preview",
        steps: [
          ["smt-gemini", "gemini-3.1-pro-preview"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "gemini/normal",
        tier: "normal",
        head: "smt-gemini/gemini-3.1-pro-preview",
        steps: [
          ["smt-gemini", "gemini-3.1-pro-preview"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-codex", "gpt-5.6-luna", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
      {
        id: "gemini/mid",
        tier: "mid",
        head: "smt-gemini/gemini-3.1-pro-preview",
        steps: [
          ["smt-gemini", "gemini-3.1-pro-preview"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "gemini/light",
        tier: "light",
        head: "smt-gemini/gemini-3.1-flash-lite-preview",
        steps: [
          ["smt-gemini", "gemini-3.1-flash-lite-preview"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-4-6", "claude"],
          ["smt-codex", "gpt-5.6-luna", "complex"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "gemini/vision",
        tier: "vision",
        head: "smt-gemini/gemini-3.1-pro-preview",
        steps: [
          ["smt-gemini", "gemini-3.1-pro-preview"],
          ["ark", "doubao-seed-2.0-lite", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
    ],
  },

  {
    profile: "glm",
    file: "mappings/glm.kv",
    roles: [
      {
        id: "glm/heavy",
        tier: "heavy",
        head: "smt-glm/glm-5.3",
        steps: [
          ["smt-glm", "glm-5.3"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "glm/normal",
        tier: "normal",
        head: "smt-glm/glm-5.3",
        steps: [
          ["smt-glm", "glm-5.3"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-luna", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
      {
        id: "glm/mid",
        tier: "mid",
        head: "smt-glm/glm-5.3",
        steps: [
          ["smt-glm", "glm-5.3"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "glm/light",
        tier: "light",
        head: "smt-glm/glm-5.3-flash",
        steps: [
          ["smt-glm", "glm-5.3-flash"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-4-6", "claude"],
          ["smt-codex", "gpt-5.6-luna", "complex"],
          ["smt-gemini", "gemini-3.1-flash-lite-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "glm/vision",
        tier: "vision",
        head: "smt-glm/glm-5.3-flash",
        steps: [
          ["smt-glm", "glm-5.3-flash"],
          ["ark", "doubao-seed-2.0-lite", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "complex"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
    ],
  },

  {
    profile: "gpt",
    file: "mappings/gpt.kv",
    roles: [
      {
        id: "gpt/heavy",
        tier: "heavy",
        head: "smt-codex/gpt-5.6-sol",
        steps: [
          ["smt-codex", "gpt-5.6-sol"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "gpt/normal",
        tier: "normal",
        head: "smt-codex/gpt-5.6-luna",
        steps: [
          ["smt-codex", "gpt-5.6-luna"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
        ],
        note: "跳过 4 个候选",
      },
      {
        id: "gpt/mid",
        tier: "mid",
        head: "smt-codex/gpt-5.6-terra",
        steps: [
          ["smt-codex", "gpt-5.6-terra"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "gpt/light",
        tier: "light",
        head: "smt-codex/gpt-5.6-luna",
        steps: [
          ["smt-codex", "gpt-5.6-luna"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-4-6", "claude"],
          ["smt-gemini", "gemini-3.1-flash-lite-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "gpt/vision",
        tier: "vision",
        head: "smt-codex/gpt-5.6-sol",
        steps: [
          ["smt-codex", "gpt-5.6-sol"],
          ["ark", "doubao-seed-2.0-lite", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "complex"],
        ],
        note: "跳过 4 个候选",
      },
    ],
  },

  {
    profile: "kimi",
    file: "mappings/kimi.kv",
    roles: [
      {
        id: "kimi/heavy",
        tier: "heavy",
        head: "kimi/kimi-k3",
        steps: [
          ["kimi", "kimi-k3"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "kimi/normal",
        tier: "normal",
        head: "kimi/kimi-k2.7-code",
        steps: [
          ["kimi", "kimi-k2.7-code"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-luna", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
      {
        id: "kimi/mid",
        tier: "mid",
        head: "kimi/kimi-k2.7-code",
        steps: [
          ["kimi", "kimi-k2.7-code"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "kimi/light",
        tier: "light",
        head: "kimi/kimi-k2.7-code",
        steps: [
          ["kimi", "kimi-k2.7-code"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["smt-claude", "claude-sonnet-4-6", "claude"],
          ["smt-codex", "gpt-5.6-luna", "complex"],
          ["smt-gemini", "gemini-3.1-flash-lite-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "kimi/vision",
        tier: "vision",
        head: "kimi/kimi-k3",
        steps: [
          ["kimi", "kimi-k3"],
          ["ark", "doubao-seed-2.0-lite", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "complex"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
    ],
  },

  {
    profile: "minimax",
    file: "mappings/minimax.kv",
    roles: [
      {
        id: "minimax/heavy",
        tier: "heavy",
        head: "minimax/MiniMax-M3",
        steps: [
          ["minimax", "MiniMax-M3"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "minimax/normal",
        tier: "normal",
        head: "minimax/MiniMax-M3",
        steps: [
          ["minimax", "MiniMax-M3"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-luna", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
      {
        id: "minimax/mid",
        tier: "mid",
        head: "minimax/MiniMax-M3",
        steps: [
          ["minimax", "MiniMax-M3"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "minimax/light",
        tier: "light",
        head: "minimax/MiniMax-M3",
        steps: [
          ["minimax", "MiniMax-M3"],
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-4-6", "claude"],
          ["smt-codex", "gpt-5.6-luna", "complex"],
          ["smt-gemini", "gemini-3.1-flash-lite-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "minimax/vision",
        tier: "vision",
        head: "minimax/MiniMax-M3",
        steps: [
          ["minimax", "MiniMax-M3"],
          ["ark", "doubao-seed-2.0-lite", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "complex"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
    ],
  },

  {
    profile: "top-only",
    file: "mappings/top-only.json",
    roles: [
      {
        id: "top-only/heavy",
        tier: "heavy",
        head: "ark/ark-code-latest",
        steps: [
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "top-only/normal",
        tier: "normal",
        head: "ark/ark-code-latest",
        steps: [
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
          ["smt-codex", "gpt-5.6-luna", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
      {
        id: "top-only/mid",
        tier: "mid",
        head: "ark/ark-code-latest",
        steps: [
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-5", "claude"],
          ["smt-codex", "gpt-5.6-terra", "complex"],
          ["smt-gemini", "gemini-3.1-pro-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "top-only/light",
        tier: "light",
        head: "ark/ark-code-latest",
        steps: [
          ["ark", "ark-code-latest", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k2.7-code", "kimi"],
          ["smt-claude", "claude-sonnet-4-6", "claude"],
          ["smt-codex", "gpt-5.6-luna", "complex"],
          ["smt-gemini", "gemini-3.1-flash-lite-preview", "gemini"],
        ],
        note: "跳过 5 个候选",
      },
      {
        id: "top-only/vision",
        tier: "vision",
        head: "ark/doubao-seed-2.0-lite",
        steps: [
          ["ark", "doubao-seed-2.0-lite", "ark"],
          ["smt-deepseek", "deepseek-flash", "ds"],
          ["smt-glm", "glm-5.3-flash", "glm"],
          ["minimax", "MiniMax-M3", "minimax"],
          ["kimi", "kimi-k3", "kimi"],
          ["smt-claude", "claude-opus-5", "claude"],
          ["smt-gemini", "gemini-3.1-pro-preview", "complex"],
          ["smt-codex", "gpt-5.6-sol", "gpt"],
        ],
        note: "跳过 4 个候选",
      },
    ],
  },
];

// ---------- runtime.takeover：谁在走 newgate（KindTable）----------

function takeoverConcept(): Concept {
  return {
    id: "runtime.takeover",
    kind: "table",
    title: "谁在走 newgate",
    source: "runtime",
    writable: false,
    order: 0,
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
            state: { text: "已接管", tone: "ok" },
            mechanism: { text: "shim" },
            detail: { text: "~/.config/newgate/bin/claude → newgate" },
          },
        },
        {
          cells: {
            agent: { text: "codex" },
            state: { text: "已接管", tone: "ok" },
            mechanism: { text: "config" },
            detail: { text: "配置文件已改写 → 本地代理" },
          },
        },
        {
          cells: {
            agent: { text: "opencode" },
            state: { text: "未安装" },
            mechanism: { text: "config" },
            detail: { text: "opencode 不在 PATH 上 —— 没有可接管的东西" },
          },
        },
      ],
    },
  };
}
