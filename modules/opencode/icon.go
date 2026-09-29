package opencode

// 这个客户端在界面上的图标：SVG path 的 `d` 那一串，`24 24` 视口。
//
// # 它从哪来
//
// LobeHub 的图标集（`github.com/lobehub/lobe-icons`，MIT，取自
// `@lobehub/icons-static-svg@1.95.1` 的 `icons/opencode.svg`）。**只取那一串路径**：
// 外层的 `<svg>` 由界面自己拼（它要决定多大、用什么颜色），所以这里不搬那一层。
//
// # 为什么它住在这个模块里，而不是界面那边
//
// 图标是**这个客户端的知识**（与 agent.go 里那串 `ContextWindowEnv` 同一条）：
// 换一家客户端就换一个图标，而界面不该认识任何一家。前端按 ID 查表是这一格的
// 反面——加一家客户端要改前端，那等于这一格从来没被设计过。
//
// 颜色不在这里定：界面用 `currentColor` 填，于是同一套图标在四套皮肤下都对。
const iconPath = "M16 6H8v12h8V6zm4 16H4V2h16v20z"
