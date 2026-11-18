package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is the GitHub Release route: find the Release, pick this platform's artifact,
// download it, check it against SHA256SUMS.
//
// Public API only, no token needed; GITHUB_TOKEN / GH_TOKEN in the env are used if set.
//
// # When the API is rate-limited, fall back to the web pages
//
// The unauthenticated API allows 60 requests an hour, **counted per egress IP**. Behind
// one shared egress that quota is spent by others - 2026-10-29 saw `API rate limit
// exceeded for <egress IP>` on a dev machine that never called; a token per machine is
// unrealistic (snode1 and the like hold no GitHub credentials).
//
// Three github.com addresses do not draw on the quota, and are enough:
//
//   - `/<repo>/releases/latest` 302s to `/<repo>/releases/tag/<tag>` (the latest tag);
//   - `/<repo>/releases/tag/<tag>` 200/404 (does this tag have a Release);
//   - `/<repo>/releases/download/<tag>/<name>` (the artifact - what API's
//     browser_download_url points at anyway).
//
// The artifact list comes from SHA256SUMS: it lists every artifact of the version, so
// the fallback gets as much as the API path (only file size). It is no makeshift.

const sumsAsset = "SHA256SUMS"

// maxBinary caps one artifact. Today's artifacts run about 20 MB; the cap only catches
// "downloaded something else" (a redirect to a huge page, a broken resume), not sizing.
const maxBinary = 512 << 20

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type ghRelease struct {
	Tag        string    `json:"tag_name"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Assets     []ghAsset `json:"assets"`
}

func (r *ghRelease) asset(name string) (ghAsset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return ghAsset{}, false
}

// errNoRelease is "this tag has no Release" (404). Not a fault: `upgrade@<tag>` builds
// from source when it sees this.
var errNoRelease = errors.New("no such release")

// errRateLimited is the API refusing (403/429): retry with the web addresses.
var errRateLimited = errors.New("rate limited")

// rateLimitErr carries the human sentence, plus `errors.Is(err, errRateLimited)`.
type rateLimitErr struct{ msg error }

func (e rateLimitErr) Error() string        { return e.msg.Error() }
func (e rateLimitErr) Is(target error) bool { return target == errRateLimited }

type releaseClient struct {
	api  string // https://api.github.com（测试换成 httptest）
	web  string // https://github.com
	repo string // owner/repo
	http *http.Client
}

func newReleaseClient(repo string) *releaseClient {
	return &releaseClient{
		api:  "https://api.github.com",
		web:  "https://github.com",
		repo: repo,
		// No overall timeout: download time depends on bandwidth, the caller's ctx governs it.
		http: &http.Client{},
	}
}

func (c *releaseClient) token() string {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// latest is the newest stable Release (GitHub's /releases/latest skips drafts and pre-releases).
func (c *releaseClient) latest(ctx context.Context) (*ghRelease, error) {
	r, err := c.get(ctx, fmt.Sprintf("%s/repos/%s/releases/latest", c.api, c.repo))
	if !errors.Is(err, errRateLimited) {
		return r, err
	}
	tag, werr := c.webLatestTag(ctx)
	if werr != nil {
		return nil, joinErr(err, werr)
	}
	return c.webRelease(ctx, tag)
}

// byTag is the Release for a tag; none = errNoRelease.
func (c *releaseClient) byTag(ctx context.Context, tag string) (*ghRelease, error) {
	r, err := c.get(ctx, fmt.Sprintf("%s/repos/%s/releases/tags/%s", c.api, c.repo, url.PathEscape(tag)))
	if !errors.Is(err, errRateLimited) {
		return r, err
	}
	code, werr := c.webStatus(ctx, fmt.Sprintf("%s/%s/releases/tag/%s", c.web, c.repo, url.PathEscape(tag)), nil)
	switch {
	case werr != nil:
		return nil, joinErr(err, werr)
	case code == http.StatusNotFound:
		return nil, errNoRelease
	case code != http.StatusOK:
		return nil, joinErr(err, i18n.E("GitHub answered HTTP {code}: {body}", i18n.A{"code": code, "body": ""}))
	}
	return c.webRelease(ctx, tag)
}

// webLatestTag reads the redirect target of `/releases/latest`.
func (c *releaseClient) webLatestTag(ctx context.Context) (string, error) {
	var loc string
	code, err := c.webStatus(ctx, fmt.Sprintf("%s/%s/releases/latest", c.web, c.repo), &loc)
	if err != nil {
		return "", err
	}
	const marker = "/releases/tag/"
	i := strings.LastIndex(loc, marker)
	if code/100 != 3 || i < 0 {
		// With no Release at all it redirects to /releases (no tag).
		return "", errNoRelease
	}
	tag, err := url.PathUnescape(loc[i+len(marker):])
	if err != nil || tag == "" {
		return "", errNoRelease
	}
	return tag, nil
}

// webStatus sends a GET without following redirects, returning only the status (and Location).
func (c *releaseClient) webStatus(ctx context.Context, endpoint string, location *string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "newgate-self-update")
	noFollow := *c.http
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noFollow.Do(req)
	if err != nil {
		return 0, unreachable(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if location != nil {
		*location = resp.Header.Get("Location")
	}
	return resp.StatusCode, nil
}

// webRelease builds a Release from SHA256SUMS: every name it lists is an artifact.
// Without SHA256SUMS it carries only the tag - "no checksum, no install" still blocks it.
func (c *releaseClient) webRelease(ctx context.Context, tag string) (*ghRelease, error) {
	r := &ghRelease{Tag: tag}
	base := fmt.Sprintf("%s/%s/releases/download/%s/", c.web, c.repo, url.PathEscape(tag))
	sums := ghAsset{Name: sumsAsset, URL: base + sumsAsset}
	var b strings.Builder
	if _, err := c.download(ctx, sums, &b); err != nil {
		return r, nil
	}
	r.Assets = append(r.Assets, sums)
	for name := range parseSums(b.String()) {
		r.Assets = append(r.Assets, ghAsset{Name: name, URL: base + url.PathEscape(name)})
	}
	return r, nil
}

func joinErr(api, web error) error {
	return i18n.E("{api}; the github.com fallback failed too: {web}", i18n.A{"api": api.Error(), "web": web.Error()})
}

func (c *releaseClient) get(ctx context.Context, endpoint string) (*ghRelease, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "newgate-self-update")
	if t := c.token(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, unreachable(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errNoRelease
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		// Rate limiting is the most common 403: the caller retries the web addresses (file
		// header); if that fails too this lands in the error, so say how to get around it.
		return nil, rateLimitErr{i18n.E("GitHub API refused the request (HTTP {code}; rate limited? set GITHUB_TOKEN): {body}",
			i18n.A{"code": resp.StatusCode, "body": apiMessage(body)})}
	case resp.StatusCode != http.StatusOK:
		return nil, i18n.E("GitHub answered HTTP {code}: {body}",
			i18n.A{"code": resp.StatusCode, "body": apiMessage(body)})
	}
	var r ghRelease
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, i18n.Ef(err, "cannot read GitHub's answer: {err}", nil)
	}
	if r.Tag == "" {
		return nil, i18n.E("GitHub's answer has no tag_name", nil)
	}
	return &r, nil
}

// download writes an artifact into dst (created by the caller), hashing as it goes, returning hex.
func (c *releaseClient) download(ctx context.Context, a ghAsset, dst io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "newgate-self-update")
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", i18n.Ef(err, "cannot download {name}: {err}", i18n.A{"name": a.Name})
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", i18n.E("downloading {name} answered HTTP {code}",
			i18n.A{"name": a.Name, "code": resp.StatusCode})
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(resp.Body, maxBinary+1))
	if err != nil {
		return "", i18n.Ef(err, "downloading {name} broke off: {err}", i18n.A{"name": a.Name})
	}
	if n > maxBinary {
		return "", i18n.E("{name} is larger than {limit} MB; refusing it",
			i18n.A{"name": a.Name, "limit": maxBinary >> 20})
	}
	if a.Size > 0 && n != a.Size {
		return "", i18n.E("{name} is {got} bytes, GitHub said {want}",
			i18n.A{"name": a.Name, "got": n, "want": a.Size})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sums downloads SHA256SUMS and parses it into a name -> digest map.
func (c *releaseClient) sums(ctx context.Context, r *ghRelease) (map[string]string, error) {
	a, ok := r.asset(sumsAsset)
	if !ok {
		// No checksum, no swap: a binary that does not match breaks every command once swapped.
		return nil, i18n.E("release {tag} has no {file}; refusing to install an unverified binary",
			i18n.A{"tag": r.Tag, "file": sumsAsset})
	}
	var b strings.Builder
	if _, err := c.download(ctx, a, &b); err != nil {
		return nil, err
	}
	return parseSums(b.String()), nil
}

// parseSums reads sha256sum's output (`<hex>  <name>`, or `<hex> *<name>` in binary mode).
func parseSums(text string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
	}
	return out
}

// unreachable is the "cannot reach GitHub" sentence. Intranet machines mostly need a proxy
// (snode1: the proxy is set only in the login shell, so `ssh host cmd` misses it); when
// none is set, say so - a bare context deadline exceeded makes one think GitHub is down.
func unreachable(err error) error {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		if os.Getenv(k) != "" {
			return i18n.Ef(err, "cannot reach GitHub: {err}", nil)
		}
	}
	return i18n.Ef(err, "cannot reach GitHub (no https_proxy is set; does this machine need one?): {err}", nil)
}

// apiMessage pulls `message` out of the API error body (raw JSON in one error line unreadable).
func apiMessage(body []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &m) == nil && m.Message != "" {
		if i := strings.Index(m.Message, " ("); i > 0 {
			return m.Message[:i]
		}
		return m.Message
	}
	return firstLine(string(body))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
