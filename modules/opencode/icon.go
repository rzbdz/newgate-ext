package opencode

// This client's icon in the UI: the `d` string of an SVG path, `24 24` viewport.
//
// # Where it comes from
//
// LobeHub's icon set (`github.com/lobehub/lobe-icons`, MIT, taken from
// `@lobehub/icons-static-svg@1.95.1`'s `icons/opencode.svg`). **Only that path string**: the
// outer `<svg>` is assembled by the UI itself (it decides how big and what color), so that layer
// is not copied here.
//
// # Why it lives in this module and not on the UI side
//
// The icon is **this client's knowledge** (the same rule as the `ContextWindowEnv` string in
// agent.go): a different client means a different icon, and the UI should not know any client. The
// front end looking icons up by ID is the reverse of this cell -- adding a client would mean
// changing the front end, which equals this cell never having been designed.
//
// The color is not set here: the UI fills with `currentColor`, so the same icon set works under all
// four skins.
const iconPath = "M16 6H8v12h8V6zm4 16H4V2h16v20z"
