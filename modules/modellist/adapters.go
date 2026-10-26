package modellist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
)

// 本文件是**适配器**：不同上游列模型的方式不一样，但回答要收敛成一种形状。
//
// 今天两类：
//
//   - `openai`：`GET {base}/v1/models`（或者 `{base}/models`，取决于 base 是否
//     已经带 /v1），返回 `{"data":[{"id":"…","owned_by":"…"}]}`。所有 OpenAI
//     兼容网关（DeepSeek、GLM、OpenRouter、one-api 系）都走这一条。
//   - `anthropic`：`GET {base}/v1/models`，`x-api-key` 认证，分页字段是
//     `has_more` / `last_id`。
//
// # 为什么两条都试
//
// provider 的 `protocol` 说的是**转发**用哪种方言，而「能不能列模型」是另一件
// 事：有的网关标着 openai、却只在 /v1/models 上答；有的标着 anthropic、实际是
// 个 OpenAI 兼容壳。所以按 protocol 决定**先试哪个**，另一个在「前一个明确说
// 没有这个接口（404/405/501）」之后再试。**认证失败不换**——那说明地址找对了，
// 换一种认证只会把「key 不对」伪装成「不支持」。

// errUnsupported 说「这条路上没有模型列表接口」：调用方可以换下一条路。
// 它与网络错误、认证错误分开，因为只有它值得再试一个适配器。
type errUnsupported struct{ why string }

func (e *errUnsupported) Error() string { return e.why }

// unsupported 造一条「这条路上没有模型列表接口」的回答。文案在调用点用
// i18n.T 求值（扫描器只认字面量，见 adapters.go 开头的规矩）。
func unsupported(msg string) error {
	return &errUnsupported{why: msg}
}

// listResult 是适配器内部的结果（还没套上 provider / 缓存这些外层事实）。
type listResult struct {
	models   []Model
	groups   []string
	endpoint string
}

// attempt 是一条「适配器 × 认证方式」的路。
type attempt struct {
	name string
	list func(context.Context, *http.Client, domain.Provider) (*listResult, error)
}

// attemptsFor 按 provider 声明的方言决定先试哪一条。
func attemptsFor(p domain.Provider) []attempt {
	openaiAttempt := attempt{name: "openai", list: listOpenAI}
	anthropicAttempt := attempt{name: "anthropic", list: listAnthropic}
	if strings.EqualFold(strings.TrimSpace(p.Protocol), "anthropic") {
		return []attempt{anthropicAttempt, openaiAttempt}
	}
	return []attempt{openaiAttempt, anthropicAttempt}
}

// ---------- HTTP ----------

const (
	// maxBody 是一份模型清单的上限。真实清单通常几十 KB；给到 4MiB 是为了
	// 聚合网关那种一次列几百个模型的场景，同时挡住「这个地址其实是个下载站」。
	maxBody = 4 << 20
	// maxErrorBody 是错误响应里带给人看的那一段上限。上游的错误页可能很长，
	// 全塞进日志/界面没有意义；400 字节足够认出「HTML 404」还是「JSON 报错」。
	maxErrorBody = 400
)

type httpResult struct {
	body   []byte
	status int
}

// safeURL 去掉 URL 里的 userinfo 再交给界面/日志。
//
// base_url 正常只写地址，但有人会把凭据写在 `https://user:pass@host` 里。那份
// 凭据不该因为一次「问不到模型」被端到浏览器上——它不是这次探测的结果。
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.User != nil {
		u.User = nil
	}
	return u.String()
}

// getJSON 发一次 GET。非 2xx 也是「有回答」，由调用方按状态码解释。
func getJSON(ctx context.Context, c *http.Client, url string, headers map[string]string) (*httpResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, i18n.Ef(err, "cannot build the model-list request for {url}: {err}",
			i18n.A{"url": safeURL(url)})
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		// 错误里可能带完整 URL（没有凭据：key 只在头里），但登到日志/界面之前
		// 仍然把 userinfo 摘掉。
		return nil, i18n.Ef(err, "cannot reach {url}: {err}", i18n.A{"url": safeURL(url)})
	}
	defer resp.Body.Close()

	limit := int64(maxBody)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		limit = maxErrorBody
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, i18n.Ef(err, "cannot read the answer from {url}: {err}", i18n.A{"url": safeURL(url)})
	}
	if int64(len(raw)) > limit {
		return nil, i18n.E("{url} answered with more than {n} bytes — that is not a model list",
			i18n.A{"url": safeURL(url), "n": limit})
	}
	return &httpResult{body: raw, status: resp.StatusCode}, nil
}

// statusNote 把非 2xx 变成一句话。404/405/501 是**这条路不存在**；其余算错误。
func statusNote(url string, res *httpResult) error {
	body := strings.TrimSpace(string(res.body))
	if len(body) > maxErrorBody {
		body = body[:maxErrorBody]
	}
	if body == "" {
		body = i18n.T("(empty body)", nil)
	}
	url = safeURL(url)
	switch res.status {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return unsupported(i18n.T("{url} has no model list (HTTP {status})",
			i18n.A{"url": url, "status": res.status}))
	case http.StatusUnauthorized, http.StatusForbidden:
		return i18n.E("{url} rejected the request (HTTP {status}) — the key may be wrong or not allowed to list models: {body}",
			i18n.A{"url": url, "status": res.status, "body": body})
	default:
		return i18n.E("{url} answered HTTP {status}: {body}",
			i18n.A{"url": url, "status": res.status, "body": body})
	}
}

// modelURLs 给一个 base 生成**可能**的模型列表地址。
//
// OpenAI 的 base 惯例是带 `/v1`（`https://api.openai.com/v1`），而 DeepSeek 那种
// 是 `https://api.deepseek.com`。两种写法在真实世界里都常见，所以两种都试；
// 已经带 /v1 的不再重复拼。
func modelURLs(base string) []string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if b == "" {
		return nil
	}
	// 有人会把完整的模型端点贴进 base_url。认它，别拼成 `/models/models`。
	if strings.HasSuffix(b, "/models") {
		return []string{b}
	}
	if strings.HasSuffix(b, "/v1") {
		return []string{b + "/models"}
	}
	return []string{b + "/v1/models", b + "/models"}
}

// openAIHeaders 是 OpenAI 兼容接口的认证。没有 key 也照样发——本机/内网的
// 兼容服务常常不校验（而「没有 key」不该在客户端就被判成不支持）。
func openAIHeaders(p domain.Provider) map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.Key()}
}

func anthropicHeaders(p domain.Provider) map[string]string {
	return map[string]string{
		"x-api-key":         p.Key(),
		"anthropic-version": "2023-06-01",
	}
}

// ---------- openai ----------

func listOpenAI(ctx context.Context, c *http.Client, p domain.Provider) (*listResult, error) {
	var last error
	for _, url := range modelURLs(p.BaseURL) {
		res, err := getJSON(ctx, c, url, openAIHeaders(p))
		if err != nil {
			return nil, err
		}
		if res.status < 200 || res.status > 299 {
			err := statusNote(url, res)
			var un *errUnsupported
			if errors.As(err, &un) {
				last = err
				continue
			}
			return nil, err
		}
		models, groups, _, _, err := parseModels(res.body)
		if err != nil {
			return nil, i18n.Ef(err, "{url} did not answer with a model list: {err}", i18n.A{"url": url})
		}
		return &listResult{models: models, groups: groups, endpoint: safeURL(url)}, nil
	}
	if last != nil {
		return nil, last
	}
	return nil, unsupported(i18n.T("{provider} has no base_url to ask for models", i18n.A{"provider": p.BaseURL}))
}

// ---------- anthropic ----------

func listAnthropic(ctx context.Context, c *http.Client, p domain.Provider) (*listResult, error) {
	base := p.BaseURL
	if p.AnthropicURL != "" {
		base = p.AnthropicURL
	}
	var last error
	for _, url := range modelURLs(base) {
		models, groups, endpoint, err := anthropicPages(ctx, c, p, url)
		if err == nil {
			return &listResult{models: models, groups: groups, endpoint: safeURL(endpoint)}, nil
		}
		var un *errUnsupported
		if errors.As(err, &un) {
			last = err
			continue
		}
		return nil, err
	}
	if last != nil {
		return nil, last
	}
	return nil, unsupported(i18n.T("{provider} has no base URL to ask for models", i18n.A{"provider": base}))
}

// anthropicPages 走完分页。Anthropic 用 `has_more` + `last_id`，下一页的查询参数
// 是 `after_id`。
func anthropicPages(ctx context.Context, c *http.Client, p domain.Provider, endpoint string) ([]Model, []string, string, error) {
	var all []Model
	var groups []string
	after := ""
	// 十条上限：真实清单一页一千个，十页足够任何一家；再多的形态我们也不认识。
	for page := 0; page < 10; page++ {
		u := endpoint
		if after == "" {
			u += "?limit=1000"
		} else {
			u += "?limit=1000&after_id=" + url.QueryEscape(after)
		}
		res, err := getJSON(ctx, c, u, anthropicHeaders(p))
		if err != nil {
			return nil, nil, "", err
		}
		if res.status < 200 || res.status > 299 {
			return nil, nil, "", statusNote(u, res)
		}
		models, gs, more, lastID, err := parseModels(res.body)
		if err != nil {
			return nil, nil, "", i18n.Ef(err, "{url} did not answer with a model list: {err}", i18n.A{"url": u})
		}
		all, groups = mergeModels(all, models, groups, gs)
		if !more || lastID == "" {
			return all, groups, endpoint, nil
		}
		after = lastID
	}
	return all, groups, endpoint, nil
}

// ---------- 解析 ----------

type wireModel struct {
	ID          string   `json:"id"`
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	OwnedBy     string   `json:"owned_by"`
	Provider    string   `json:"provider"`
	Group       string   `json:"group"`
	Groups      []string `json:"groups"`
}

// parseModels 从一份上游回答里取出模型。返回 hasMore/lastID 供分页用。
//
// 它认三种常见外壳（都是实测见过的）：
//
//	{"data":[…]}                 OpenAI / Anthropic
//	{"models":[…]}               一些兼容网关
//	[…]                          少数自建服务直接回数组
//
// 元素可以是对象，也可以是裸字符串（`{"data":["gpt-4"]}`）。
func parseModels(raw []byte) ([]Model, []string, bool, string, error) {
	arr, more, lastID, err := modelArray(raw)
	if err != nil {
		return nil, nil, false, "", err
	}
	var out []Model
	var groups []string
	for _, item := range arr {
		var id string
		if err := json.Unmarshal(item, &id); err == nil {
			out = append(out, Model{ID: strings.TrimSpace(id)})
			continue
		}
		var w wireModel
		if err := json.Unmarshal(item, &w); err != nil {
			// 一条认不出的元素不该让整份清单失败：跳过它，其余照常（fail-open）。
			continue
		}
		m := Model{ID: firstNonEmpty(w.ID, w.Slug), Name: firstNonEmpty(w.Name, w.DisplayName)}
		if m.ID == "" {
			m.ID = strings.TrimSpace(w.Name)
		}
		m.Group = groupOf(w)
		if m.ID == "" {
			continue
		}
		out = append(out, m)
	}
	out, groups = mergeModels(nil, out, groups, nil)
	return out, groups, more, lastID, nil
}

func modelArray(raw []byte) ([]json.RawMessage, bool, string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, false, "", i18n.E("the answer is empty", nil)
	}
	if strings.HasPrefix(trimmed, "[") {
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, false, "", err
		}
		return arr, false, "", nil
	}
	var root struct {
		Data    json.RawMessage `json:"data"`
		Models  json.RawMessage `json:"models"`
		HasMore bool            `json:"has_more"`
		LastID  string          `json:"last_id"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, false, "", err
	}
	for _, field := range []json.RawMessage{root.Data, root.Models} {
		if len(field) == 0 || string(field) == "null" {
			continue
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(field, &arr); err == nil {
			return arr, root.HasMore, root.LastID, nil
		}
		// `{"data":{"models":[…]}}` 这种再套一层的形态。
		var nested struct {
			Models []json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal(field, &nested); err == nil && nested.Models != nil {
			return nested.Models, root.HasMore, root.LastID, nil
		}
	}
	return nil, root.HasMore, root.LastID, i18n.E("the answer has neither a data nor a models array", nil)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// groupOf 取上游给的分组。优先级：group > groups[0] > provider > owned_by。
//
// 都没有时，id 里带 `/` 的按第一段分组（OpenRouter 那种 `anthropic/…`）。
// 不做更花哨的推断：`gpt-5.6-sol` 按 `-` 切出来的 `gpt` 不是分组，是噪音。
func groupOf(w wireModel) string {
	if g := strings.TrimSpace(w.Group); g != "" {
		return g
	}
	for _, g := range w.Groups {
		if s := strings.TrimSpace(g); s != "" {
			return s
		}
	}
	if g := firstNonEmpty(w.Provider, w.OwnedBy); g != "" {
		return g
	}
	id := firstNonEmpty(w.ID, w.Slug)
	if i := strings.IndexByte(id, '/'); i > 0 {
		return id[:i]
	}
	return ""
}

// mergeModels 把一页并进累积结果：按 id 去重，分组按第一次出现排序。
//
// 去重不是洁癖：分页边界上同一页可能被重复返回（上游 bug），而去重之后界面
// 不会出现两个一模一样的选项。
func mergeModels(dst, add []Model, dstGroups, addGroups []string) ([]Model, []string) {
	seen := make(map[string]bool, len(dst)+len(add))
	for _, m := range dst {
		seen[m.ID] = true
	}
	for _, m := range add {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		dst = append(dst, m)
	}
	groups := append([]string(nil), dstGroups...)
	has := make(map[string]bool, len(groups))
	for _, g := range groups {
		has[g] = true
	}
	for _, g := range addGroups {
		if g != "" && !has[g] {
			has[g] = true
			groups = append(groups, g)
		}
	}
	// 模型自己带的分组也算「出现过」，否则前端分组标题的顺序会缺。
	for _, m := range dst {
		if m.Group != "" && !has[m.Group] {
			has[m.Group] = true
			groups = append(groups, m.Group)
		}
	}
	return dst, groups
}

// cacheKey 把「这一份 provider 定义」压成一个键。
//
// 它包含 key 的哈希而**不是 key 本身**：缓存表住在内存里，没有理由让它拿着一
// 份明文；而换了 key 之后清单可能完全不同，所以 key 必须参与。
func cacheKey(name string, p domain.Provider) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		name, p.Protocol, p.BaseURL, p.AnthropicURL, p.APIKeyEnv, p.Key())))
	return hex.EncodeToString(sum[:])
}
