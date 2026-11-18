package hermes

// This client's icon in the UI: the `d` string of an SVG path, in a `24 24` viewport.
//
// # Where it comes from
//
// Drawn by this module: an H with twin wings (Hermes' winged helm). LobeHub's icon set has no
// Hermes Agent, and its maker's (Nous Research) mark is a company logo, not this client's --
// using a company logo to represent a tool makes people think it is official integration.
//
// # Why it lives in this module and not on the UI side
//
// The same rule as codex / opencode (see modules/codex/icon.go): the icon is **this client's
// knowledge**, and the UI should know none of them. The UI fills the color with
// `currentColor`.
const iconPath = "M8 5h2.5v5.75h3V5H16v14h-2.5v-5.75h-3V19H8V5z" +
	"M7 7.5 1.5 6l1.6 2.2L1 9.2l3.2.6L3 11.5l4-1.2V7.5z" +
	"M17 7.5 22.5 6l-1.6 2.2 2.1 1-3.2.6 1.2 1.7-4-1.2V7.5z"
