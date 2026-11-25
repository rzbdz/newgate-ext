package gemini

// This client's icon in the UI: the `d` string of an SVG path, `24 24` viewport.
//
// A four-pointed sparkle: the shape this client's own product family uses for "automated
// assistant". Drawn here rather than lifted from a brand file -- the module brings its own
// visual, and the UI only assembles `<svg viewBox="0 0 24 24">` and fills `currentColor`
// (same rule as codex's icon.go).
const iconPath = "M12 2c.4 3.6 1.9 6.1 4.9 8.4l1 .8-1 .8c-3 2.3-4.5 4.8-4.9 8.4h-.1c-.4-3.6-1.9-6.1-4.9-8.4l-1-.8 1-.8C9.9 8.1 11.4 5.6 11.8 2h.2zM19.5 14c.2 1.7.9 2.9 2.3 4l.5.4-.5.4c-1.4 1.1-2.1 2.3-2.3 4h-.1c-.2-1.7-.9-2.9-2.3-4l-.5-.4.5-.4c1.4-1.1 2.1-2.3 2.3-4h.1z"
