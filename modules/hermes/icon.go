package hermes

// 这个客户端在界面上的图标：SVG path 的 `d` 那一串，`24 24` 视口。
//
// # 它从哪来
//
// 本模块自己画的：一个带双翼的 H（Hermes 的翼盔）。LobeHub 的图标集里没有 Hermes
// Agent，而它的出品方（Nous Research）那一枚是公司标志，不是这个客户端的——拿公司
// 标志代表一个工具会让人以为那是官方集成。
//
// # 为什么它住在这个模块里，而不是界面那边
//
// 与 codex / opencode 同一条（见 modules/codex/icon.go）：图标是**这个客户端的知识**，
// 界面不该认识任何一家。颜色由界面用 `currentColor` 填。
const iconPath = "M8 5h2.5v5.75h3V5H16v14h-2.5v-5.75h-3V19H8V5z" +
	"M7 7.5 1.5 6l1.6 2.2L1 9.2l3.2.6L3 11.5l4-1.2V7.5z" +
	"M17 7.5 22.5 6l-1.6 2.2 2.1 1-3.2.6 1.2 1.7-4-1.2V7.5z"
