import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// 公开站上那个**可点的界面演示**：同一份前端的第二个入口。
//
// # 为什么是第二个入口，而不是第二个工程
//
// ConceptCard 静态 import 了全部七个 kind，其中 CodeEditor 静态 import 了五个
// CodeMirror 包（见 package.json）——静态 import 摇不掉。所以「新起一个 vite 工程去
// import 这些源码」必须**再装一遍依赖**（复制 package.json + lockfile，或者引一个
// pnpm workspace）。为了一个演示页把整个前端工具链复制一份，是这里最大的浪费。
//
// 共用一份工程就没有这个问题：一次 `pnpm install`、同一套 svelte 插件、同一份
// 依赖版本。代价是这个文件本身（二十行配置）。
//
// # 它进不了产品的产物
//
// outDir 指到 `site/dist/demo`（仓库根下），而产品那条路（vite.config.ts）读的是
// `web/dist`，`//go:embed all:web/dist` 也只读那一份。两边唯一的交集是**源码**：
// 演示页 import 的是 `../src/...`，也就是说它跑的就是产品那几个组件。
//
// `site/dist` 在 .gitignore 里（与 dist/architecture.html 同一条：那是「这一刻」的
// 产物），所以演示页的产物不进版本控制，也就不会让那两个 Go 侧棘轮（web_test.go /
// i18n_test.go）看到一个凭空多出来的东西。
const here = dirname(fileURLToPath(import.meta.url));

// base 是**站点根那一层**推出来的，不是各写一份。
//
// 为什么留这个口子：本地 `python3 -m http.server -d site/dist` 是从**根**服务的，
// 而 Pages 上的站点在 `/newgate-ext/` 下。写死的话本地打开演示页就是白屏 + 一串
// 404（资源都在一个不存在的 `/newgate-ext/` 下）——而那正是改这个页面时最常见的
// 一条路。
//
// 为什么是**推出来的**而不是第二个环境变量：站点生成器与演示页这两个 base 必须
// 同时指对，而它们其实是同一个数的两次抄写（演示页在站点根下的 `/demo/`）。两份
// 环境变量就有「拧了这一个、忘了那一个」的余地，而那种错的表现在浏览器里是白屏
// ——和「根本没建成功」长得一样。所以只留一个口子（`SITEGEN_BASE`，与
// tools/sitegen 的 basePath 是同一个变量），`demo/` 这一节在这里拼上：
//
//	SITEGEN_BASE=/ bash site/build.sh --local   # 本地看：base 变成 /demo/
const SITE_BASE = process.env.SITEGEN_BASE ?? "/newgate-ext/";
const DEMO_BASE = (SITE_BASE.endsWith("/") ? SITE_BASE : SITE_BASE + "/") + "demo/";

export default defineConfig({
  // root 是 demo/：vite 从这里找 index.html。publicDir 显式指回上一层的 public/，
  // 那样 favicon 两个入口共用一份（默认会去 demo/public 找，那里没有）。
  root: "demo",
  publicDir: resolve(here, "public"),
  base: DEMO_BASE,
  plugins: [svelte()],
  build: {
    outDir: resolve(here, "../../../site/dist/demo"),
    // root 之外的 outDir 默认**不清理**（vite 怕误删），而这里要的就是「整份重来」：
    // 删掉一行之后，旧 chunk 留着会让页面加载到上一个版本。
    emptyOutDir: true,
    target: "es2022",
    rollupOptions: { output: { manualChunks: undefined } },
  },
});
