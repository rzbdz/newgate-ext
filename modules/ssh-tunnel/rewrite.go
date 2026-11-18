package sshtunnel

import (
	"bytes"
	"net/http"
	"strings"
)

// RemoteUIPrefix is the remote UI's absolute prefix on its own machine (= web-dashboard's Prefix).
//
// It is also **the whole reason this rewriting works**: that one literal is the only
// absolute address in the remote's artifacts. vite.config.ts's `base: "/ui/"` is baked
// into three places at build time:
//
//	dist/index.html   <script src="/ui/assets/index-XXXX.js">
//	dist/index.html   <link rel="icon" href="/ui/favicon.svg">
//	dist/assets/*.js  import.meta.env.BASE_URL folded into "/ui/api"
//
// All three are the same "/ui/", so **one text replacement covers all** - including /ui/api,
// which hits this machine's own web-dashboard (local UI, remote data - worse than blank).
const RemoteUIPrefix = "/ui/"

// LocalPrefix is a target's root on this route, **with a trailing slash**.
//
// The trailing slash is required: rewriting goes "remote absolute -> local absolute", and
// `/ui/remote/ds/ui/api` depends entirely on whether that slash is here. Missing it, every
// remote request hits `/ui/remoteds/...` - a 404 invisible in the browser.
func LocalPrefix(id string) string {
	return Prefix + "/" + id + "/"
}

// textBodies are the content types that get rewritten.
//
// # Why JS and CSS are in it too (learned the hard way)
//
// The first judgment was "only text/html, since CSS and JS are referenced from HTML and
// their **addresses** were rewritten in that pass". First half true, second false: the
// `/ui/` in JS is not an asset address but **vite baking import.meta.env.BASE_URL into
// the artifact**, used at runtime to build API URLs -
//
//	const Fn="/ui/api";   // <- literally in the artifact
//
// Unrewritten, the tunneled page sends requests to **this machine's own** `/ui/api`; the UI
// renders fine, the data is all local. Users see a "working UI" - far worse than a blank
// page, since they must first notice the data is wrong (measured: caught on day one).
//
// CSS is in for the same reason: vite's url() also carries the absolute prefix; today it
// only shows in asset addresses (already rewritten in HTML), but writing the rule by
// content type rather than by "the cases I had in mind" costs one less judgment.
//
// The criterion is still **the prefix itself**: a body without "/ui/" is untouched (see
// rewritePrefix's fast return), so including JS does not mean rewriting every bundle.
var textBodies = map[string]bool{
	"text/html":                true,
	"text/css":                 true,
	"text/javascript":          true,
	"application/javascript":   true,
	"application/x-javascript": true, // 老一点的服务器还在发这个
}

// shouldRewrite says whether this response should be rewritten.
//
// Three criteria, each guarding one specific error:
//
//   - **content type in textBodies**. Others (JSON, images, fonts) are never touched:
//     they are data, not addresses, and text replacement must not see them.
//   - **no Content-Encoding**. A compressed body has no plaintext path to rewrite; touching
//     it destroys the response. The caller already dropped Accept-Encoding (see proxy.go),
//     so this is normally unreachable - it is here for "the remote compresses anyway":
//     then better **not rewrite** (asset 404s are visible) than a garbled page.
//   - **2xx / 3xx / 4xx**. The remote's error pages are its own HTML and need rewriting to
//     display - else "the remote reported an error" shows a blank page, identical to "the
//     route is broken". 5xx is left alone: those bodies are usually the proxy's own.
func shouldRewrite(resp *http.Response) bool {
	if resp.StatusCode >= 500 {
		return false
	}
	if resp.Header.Get("Content-Encoding") != "" {
		return false
	}
	ct := resp.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return textBodies[strings.ToLower(strings.TrimSpace(ct))]
}

// urlDelims is "where an address literal ends".
//
// Its criterion is in isAbsoluteURL: tell `/ui/x` (the remote's own absolute path, rewrite
// it) from `https://example.com/ui/x` (elsewhere; rewriting turns it into a path on our
// host). The latter only appears in bodies between quotes, parens or tags - so walk back
// along those characters; that segment is "the full spelling of this address".
const urlDelims = " \t\r\n\"'<>()=,;`"

// rewritePrefix swaps the remote body's absolute prefix for this route's prefix.
//
// Why **bare text replacement** and not an HTML tokenizer: a tokenizer only touches
// attribute values, not JS strings, and the JS one (`"/ui/api"`) is the one that makes the
// UI work. Bare replacement's collateral - the four characters "/ui/" in a body - in the
// remote's own HTML can only be paths: it is its own artifact, not user data.
//
// **The one exception is a host-qualified address** (`https://example.com/ui/x`): it is
// not "another remote path"; rewriting it points an outbound link back at us. Bare
// replacement cannot tell them apart, so look back (see isAbsoluteURL) to keep
// "external links are not silently rerouted" - an error with no symptom when wrong.
//
// The boundary: **if a remote module emits user content into HTML** (a page showing
// request bodies), this replacement hits user data. Then add a "only attribute values
// and string literals" criterion, or open that route to JSON only.
func rewritePrefix(body []byte, local string) []byte {
	if !bytes.Contains(body, []byte(RemoteUIPrefix)) {
		// Most responses (JSON, images, the one 404 sentence) do not contain it: one Contains
		// returns early, no new slice - so "what rewriting touched" is assertable.
		return body
	}
	var out []byte
	// cursor = "how far the body is written out", scan = "where we found it". Separated
	// because skipped ones (external links) must still be written, just not replaced - one
	// cursor would turn "skip" into "drop".
	cursor, scan := 0, 0
	for {
		i := bytes.Index(body[scan:], []byte(RemoteUIPrefix))
		if i < 0 {
			break
		}
		i += scan
		scan = i + len(RemoteUIPrefix)
		if isAbsoluteURL(body, i) {
			continue
		}
		if out == nil {
			out = make([]byte, 0, len(body)+len(local))
		}
		out = append(out, body[cursor:i]...)
		out = append(out, local...)
		cursor = scan
	}
	if out == nil {
		return body
	}
	return append(out, body[cursor:]...)
}

// isAbsoluteURL reports whether the `/ui/` here is part of a **host-qualified address**.
//
// Only two criteria, both "walk back to a delimiter and look at the segment":
//
//	https://example.com/ui/x   -> the segment has "://"  -> elsewhere
//	//example.com/ui/x          -> the segment starts "//"   -> elsewhere
//	/ui/assets/x.js            -> the segment is a path     -> the remote itself
//
// Not a full URL parse: the body is **someone else's artifact bytes** (minified JS with
// strings, templates, regexes); a parser turns "I do not recognize it" into "I broke it".
func isAbsoluteURL(body []byte, at int) bool {
	j := at
	for j > 0 && !strings.ContainsRune(urlDelims, rune(body[j-1])) {
		j--
	}
	token := body[j:at]
	return bytes.Contains(token, []byte("://")) || bytes.HasPrefix(token, []byte("//"))
}

// rewriteLocation replaces a redirect's Location too.
//
// Unrewritten, it **jumps to this machine's own /ui/**: the remote BFF 302s `/ui` (no
// trailing slash) to `/ui/`, which locally is web-dashboard - one click takes the user
// from a "remote UI" to a "local UI" that looks almost identical. The only **invisible**
// error in this rewriting, so its own function and its own test.
//
// Only **same-site absolute paths** (starting with "/ui/"); scheme-full (http://...) and
// relative addresses are never touched - the former is elsewhere, the latter follows the document.
func rewriteLocation(loc, local string) string {
	if !strings.HasPrefix(loc, RemoteUIPrefix) {
		return loc
	}
	return local + strings.TrimPrefix(loc, RemoteUIPrefix)
}
