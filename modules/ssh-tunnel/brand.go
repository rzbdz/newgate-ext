package sshtunnel

import (
	"bytes"
	"encoding/base64"
	"hash/fnv"
	"html"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Why this file exists: a tunneled remote UI is the **same artifact** as the local one -
// same "newgate" tab title, same icon. With local and remote tabs open side by side nothing
// tells them apart, and acting in the wrong tab changes the wrong machine. So the proxy marks
// each remote page on the way through: the target's label in the title, and an icon of its own.
//
// # Only HTML-standard hooks, never the remote's internals
//
// Both marks touch **standard elements** (`<title>`, `<link rel="icon">` before `</head>`),
// not anything web-dashboard happens to ship. An earlier version recolored the remote's
// favicon.svg by swapping its accent literal: that read another module's private artifact,
// and broke silently (no compile error, a green test copying the same literal) the day the
// icon changed. So this module draws **its own** icon and brings it inline as a data: URI -
// nothing to match on the remote side, no extra route.

// targetAccents are deliberately **not blue** (the factory UI's accent), so a remote tab
// reads as "not local" at a glance, and targets differ from each other by hash.
var targetAccents = []string{
	"#f0883e", // orange
	"#3fb950", // green
	"#db61a2", // pink
	"#a371f7", // purple
	"#d29922", // yellow
	"#f85149", // red
	"#39c5cf", // teal
}

// accentFor picks a stable color per target ID (same ID -> same color across restarts).
func accentFor(id string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return targetAccents[h.Sum32()%uint32(len(targetAccents))]
}

// markPage applies both marks to a remote HTML document.
func markPage(body []byte, t Target) []byte {
	return injectIcon(labelTitle(body, t.Label), targetIcon(t))
}

// labelTitle prefixes the first <title> with the target label: "snode1 · newgate".
// No <title> = unchanged: the icon still marks the tab.
func labelTitle(body []byte, label string) []byte {
	const open = "<title>"
	i := bytes.Index(body, []byte(open))
	if i < 0 || label == "" {
		return body
	}
	return insertAt(body, i+len(open), html.EscapeString(label)+" · ")
}

// injectIcon adds an icon link right before </head>. Browsers use the **last** declared
// icon, so this one wins over the remote's own without having to find and remove it.
func injectIcon(body []byte, href string) []byte {
	i := bytes.Index(body, []byte("</head>"))
	if i < 0 {
		return body
	}
	return insertAt(body, i, `<link rel="icon" type="image/svg+xml" href="`+href+`">`)
}

// targetIcon draws the target's icon: a rounded square in its accent with the label's
// initial. A letter plus a color stays legible at the 16px a tab gives it.
func targetIcon(t Target) string {
	label := t.Label
	if label == "" {
		label = t.ID
	}
	r, _ := utf8.DecodeRuneInString(label)
	if r == utf8.RuneError {
		r = '?'
	}
	initial := html.EscapeString(string(unicode.ToUpper(r)))
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">` +
		`<rect width="32" height="32" rx="7" fill="` + accentFor(t.ID) + `"/>` +
		`<text x="16" y="23" text-anchor="middle" font-family="system-ui,sans-serif" ` +
		`font-size="20" font-weight="700" fill="#12151b">` + initial + `</text></svg>`
	// base64 rather than percent-encoding: no character in it can end the attribute.
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg))
}

func insertAt(body []byte, at int, s string) []byte {
	out := make([]byte, 0, len(body)+len(s))
	out = append(out, body[:at]...)
	out = append(out, s...)
	return append(out, body[at:]...)
}

// isHTML reports whether the content type is an HTML document.
func isHTML(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "text/html")
}
