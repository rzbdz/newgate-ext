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

// 本文件是 GitHub Release 那条路：查 Release、挑本平台的产物、下载、对 SHA256SUMS。
//
// 只用公开 API，不需要 token；环境里有 GITHUB_TOKEN / GH_TOKEN 就带上。
//
// # API 被限流时退回网页那几条地址
//
// 未认证的 API 每小时 60 次、**按出口 IP 计**。公司网络、跳板机后面几十台机器共用
// 一个出口时，这个额度是别人花掉的——2026-10-29 第一次在开发机上试 `upgrade@v0.8.0`
// 就撞上了 `API rate limit exceeded for <出口 IP>`，而这台机器自己一次都没调过。
// 要求每台机器配 token 不现实（snode1 那类机器上没有任何 GitHub 凭据）。
//
// github.com 上的三条地址不走 API 额度，而且够用：
//
//   - `/<repo>/releases/latest` 302 到 `/<repo>/releases/tag/<tag>`（最新 tag）；
//   - `/<repo>/releases/tag/<tag>` 200/404（这个 tag 有没有 Release）；
//   - `/<repo>/releases/download/<tag>/<名字>`（产物本身——API 给的 browser_download_url
//     也就是它）。
//
// 产物清单从 SHA256SUMS 里读：它本来就列着这一版的每个产物，而没有它的 Release
// 反正也不装。所以这条退路不是「凑合」，它拿到的东西与 API 那条一样多（只少了
// 文件大小，那一项本来就只是个额外的核对）。

const sumsAsset = "SHA256SUMS"

// maxBinary 是一个产物的上限。今天的产物在 20 MB 上下；上限只拦「下成了别的
// 东西」（重定向到一个巨大的页面、断点续传拼错），不是在估算体积。
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

// errNoRelease 是「这个 tag 没有 Release」（404）。它不是故障：`upgrade@<tag>` 遇到
// 它就改走源码构建。
var errNoRelease = errors.New("no such release")

// errRateLimited 是 API 拒绝了（403/429）：换网页那几条地址再试。
var errRateLimited = errors.New("rate limited")

// rateLimitErr 带着给人看的那句话，同时 `errors.Is(err, errRateLimited)`。
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
		// 不设整体超时：下载的长短取决于带宽，由调用方的 ctx 管。
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

// latest 是最新的正式 Release（GitHub 的 /releases/latest 本来就跳过草稿与预发布）。
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

// byTag 是某个 tag 的 Release；没有就是 errNoRelease。
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

// webLatestTag 读 `/releases/latest` 那一跳重定向的目标。
func (c *releaseClient) webLatestTag(ctx context.Context) (string, error) {
	var loc string
	code, err := c.webStatus(ctx, fmt.Sprintf("%s/%s/releases/latest", c.web, c.repo), &loc)
	if err != nil {
		return "", err
	}
	const marker = "/releases/tag/"
	i := strings.LastIndex(loc, marker)
	if code/100 != 3 || i < 0 {
		// 没有任何 Release 时它重定向到 /releases（不带 tag）。
		return "", errNoRelease
	}
	tag, err := url.PathUnescape(loc[i+len(marker):])
	if err != nil || tag == "" {
		return "", errNoRelease
	}
	return tag, nil
}

// webStatus 发一个不跟随重定向的 GET，只要状态码（与 Location）。
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
		return 0, i18n.Ef(err, "cannot reach GitHub: {err}", nil)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if location != nil {
		*location = resp.Header.Get("Location")
	}
	return resp.StatusCode, nil
}

// webRelease 用 SHA256SUMS 拼出一份 Release：它列着的每个名字就是一个产物。
// 没有 SHA256SUMS 时只带回 tag——后面的「没有校验和就不装」照常拦住它。
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
		return nil, i18n.Ef(err, "cannot reach GitHub: {err}", nil)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errNoRelease
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		// 限流是最常见的 403：调用方换网页地址再试（见文件头）；那条也失败时这句
		// 话会出现在报错里，所以说清楚怎么绕开，而不是只报一个状态码。
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

// download 把一个产物写进 dst（调用方建好的文件），边写边算 sha256，返回十六进制。
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

// sums 下 SHA256SUMS 并解析成 文件名 → 摘要。
func (c *releaseClient) sums(ctx context.Context, r *ghRelease) (map[string]string, error) {
	a, ok := r.asset(sumsAsset)
	if !ok {
		// 没有校验和就不换：一个对不上号的二进制被换上去之后，坏的是每一条命令。
		return nil, i18n.E("release {tag} has no {file}; refusing to install an unverified binary",
			i18n.A{"tag": r.Tag, "file": sumsAsset})
	}
	var b strings.Builder
	if _, err := c.download(ctx, a, &b); err != nil {
		return nil, err
	}
	return parseSums(b.String()), nil
}

// parseSums 读 sha256sum 的输出（`<hex>  <name>`，二进制模式是 `<hex> *<name>`）。
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

// apiMessage 从 API 的错误体里取 `message`（整段 JSON 塞进一行报错里没法读）。
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
