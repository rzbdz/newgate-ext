package sshtunnel

import (
	"bytes"
	"hash/fnv"
	"html"
	"net/http"
	"strings"
)

// Why this file exists: a tunneled remote UI is the **same artifact** as the local one -
// same "newgate" tab title, same blue torii icon. With two or three tabs open (local +
// remotes) nothing tells them apart, and acting in the wrong tab means changing the wrong
// machine. So the proxy marks each remote page on the way through: the target's label in
// the title, and a per-target accent color on the favicon.

// localAccent is the favicon's foreground color in web-dashboard/web/public/favicon.svg.
// The remote serves the same artifact, so this literal is what we recolor. If the icon's
// color changes there, recoloring silently stops (the icon stays blue) - visible, harmless.
const localAccent = "#5b8def"

// targetAccents are deliberately **not blue**: blue is the local UI's color, so any remote
// is distinguishable from local at a glance, and targets from each other by hash.
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

// labelTitle prefixes the first <title> with the target label: "ds · newgate".
// Only the first occurrence: the remote's index.html has exactly one.
func labelTitle(body []byte, label string) []byte {
	const open = "<title>"
	i := bytes.Index(body, []byte(open))
	if i < 0 || label == "" {
		return body
	}
	i += len(open)
	prefix := html.EscapeString(label) + " · "
	out := make([]byte, 0, len(body)+len(prefix))
	out = append(out, body[:i]...)
	out = append(out, prefix...)
	return append(out, body[i:]...)
}

// isFavicon reports whether this response is the remote's SVG favicon.
func isFavicon(resp *http.Response) bool {
	if resp.Request == nil || !strings.HasSuffix(resp.Request.URL.Path, "/favicon.svg") {
		return false
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "image/svg+xml")
}

// recolorFavicon swaps the local accent for the target's.
func recolorFavicon(body []byte, id string) []byte {
	return bytes.ReplaceAll(body, []byte(localAccent), []byte(accentFor(id)))
}

// isHTML reports whether the response body is an HTML document.
func isHTML(resp *http.Response) bool {
	return strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/html")
}
