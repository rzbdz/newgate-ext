import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { mockModelList } from "./vite.mock-modellist";

// base 必须是 "/ui/"：产物被嵌进二进制、挂在 porthub 的 /ui 前缀下（见
// module.go 的 Prefix 与 AssetDir）。这条对应关系只有一处真相——改这里就要同时
// 改那个常量，而两边不一致的症状是「页面白屏、控制台一堆 404」，很难一眼看出
// 是前缀写错了。
//
// outDir 是 web/dist（提交进版本控制）：Go 的 go:embed 读它，所以 `go build`
// 与 CI 仍然完全离线，前端工具链只在开发机上出现。
export default defineConfig({
  base: "/ui/",
  // 产品这一份**有**后端：模型下拉真的去问 /ui/api/ext/modellist。
  define: { __NEWGATE_DEMO__: "false" },
  // mockModelList 只在 `vite dev` 里生效（插件自己声明 apply: "serve"）：它按真
  // 后端的形状回答 /ui/api/ext/modellist，让模型下拉在前端阶段就能完整验一遍。
  // 产物的路径一条都不会经过它。
  plugins: [svelte(), mockModelList()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    target: "es2022",
    // 不拆 chunk：产物是嵌进二进制的、内网离线用的，一个 bundle 比多几个请求
    // 路径更好排查（打开 devtools 只有一个 js）。
    rollupOptions: { output: { manualChunks: undefined } },
  },
});
