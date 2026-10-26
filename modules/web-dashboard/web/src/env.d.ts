// 构建期的两个事实（由 vite 的 `define` 注入）：
//
//   __NEWGATE_DEMO__  true = 站点上那个「可点的演示页」。它没有后端，所以模型
//                     下拉不往上游问（问了只会得到一条 404，然后在屏幕上写着
//                     「问不到」——那不是这个页面要演的东西）。
//
// 用 `declare const` 而不是 `import.meta.env`：这两个值不是环境变量，是**构建
// 配置**决定的（见 vite.config.ts / vite.demo.config.ts 的 define）。环境变量的
// 名字一旦拼错，表现是 `undefined` 静默为假；这里拼错会在 `pnpm check` 那一层
// 就红。
declare const __NEWGATE_DEMO__: boolean;
