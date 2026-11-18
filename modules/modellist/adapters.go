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

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is the **adapter** layer: different upstreams list models differently, but the answer
// must converge on one shape.
//
// Two kinds today:
//
//   - `openai`: `GET {base}/v1/models` (or `{base}/models`, depending on whether base already
//     carries /v1), returning `{"data":[{"id":"…","owned_by":"…"}]}`. Every OpenAI-compatible
//     gateway (DeepSeek, GLM, OpenRouter, the one-api family) takes this route.
//   - `anthropic`: `GET {base}/v1/models`, `x-api-key` auth, pagination fields are
//     `has_more` / `last_id`.
//
// # Why both are tried
//
// A provider's `protocol` says which dialect **forwarding** uses, while "can it list models" is
// another matter: some gateways are labeled openai yet only answer on /v1/models; some labeled
// anthropic are really an OpenAI-compatible shell. So protocol decides **which to try first**, and
// the other is tried after "the first one explicitly says it has no such endpoint
// (404/405/501)". **Auth failure does not switch** -- that means the address was right, and
// switching auth would only disguise "wrong key" as "not supported".

// errUnsupported says "there is no model-list endpoint on this route": the caller may try the next
// route. It is kept apart from network and auth errors because only it is worth trying another
// adapter for.
type errUnsupported struct{ why string }

func (e *errUnsupported) Error() string { return e.why }

// unsupported builds an "there is no model-list endpoint on this route" answer. The copy is
// evaluated with i18n.T at the call site (the scanner only sees literals, see the rule atop
// adapters.go).
func unsupported(msg string) error {
	return &errUnsupported{why: msg}
}

// listResult is the adapter's internal result (not yet wrapped in outer facts like provider and
// cache).
type listResult struct {
	models   []Model
	groups   []string
	endpoint string
}

// attempt is one "adapter × auth method" route.
type attempt struct {
	name string
	list func(context.Context, *http.Client, domain.Provider) (*listResult, error)
}

// attemptsFor decides which route to try first, by the dialect the provider declares.
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
	// maxBody caps one model listing. Real listings are usually tens of KB; 4MiB is for the
	// aggregator-gateway case of hundreds of models at once, while also blocking "this address is
	// really a download site".
	maxBody = 4 << 20
	// maxErrorBody caps the portion of an error response kept for humans. An upstream error page
	// can be long, and stuffing it all into the log/UI means nothing; 400 bytes is enough to tell an
	// "HTML 404" from a "JSON error".
	maxErrorBody = 400
)

type httpResult struct {
	body   []byte
	status int
}

// safeURL strips userinfo out of the URL before handing it to the UI/log.
//
// base_url normally holds only an address, but some people put credentials in
// `https://user:pass@host`. That credential should not reach the browser because of one "could not
// ask for models" -- it is not the result of this probe.
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

// getJSON sends one GET. A non-2xx is still "an answer", interpreted by the caller by status code.
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
		// The error may carry the full URL (with no credentials: the key is only in the header), but
		// userinfo is still stripped before it is logged/shown.
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

// statusNote turns a non-2xx into a sentence. 404/405/501 mean **this route does not exist**; the
// rest count as errors.
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

// modelURLs generates the **possible** model-list addresses for one base.
//
// OpenAI's base convention carries `/v1` (`https://api.openai.com/v1`), while DeepSeek-style ones
// are `https://api.deepseek.com`. Both spellings are common in the real world, so both are tried;
// one that already has /v1 is not joined again.
func modelURLs(base string) []string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if b == "" {
		return nil
	}
	// Some people paste the full model endpoint into base_url. Recognize it; do not build
	// `/models/models`.
	if strings.HasSuffix(b, "/models") {
		return []string{b}
	}
	if strings.HasSuffix(b, "/v1") {
		return []string{b + "/models"}
	}
	return []string{b + "/v1/models", b + "/models"}
}

// openAIHeaders is the OpenAI-compatible endpoint's auth. Sent even without a key -- local/intranet
// compatible services often do not check (and "no key" should not be judged unsupported on the
// client side).
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

// anthropicPages walks the pagination. Anthropic uses `has_more` + `last_id`, and the next page's
// query parameter is `after_id`.
func anthropicPages(ctx context.Context, c *http.Client, p domain.Provider, endpoint string) ([]Model, []string, string, error) {
	var all []Model
	var groups []string
	after := ""
	// Ten pages max: a real listing is a thousand per page, ten pages is enough for anyone; any more
	// shapes we would not recognize anyway.
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

// ---------- parsing ----------

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

// parseModels pulls models out of one upstream answer. Returns hasMore/lastID for pagination.
//
// It recognizes three common envelopes (all seen in practice):
//
//	{"data":[…]}                 OpenAI / Anthropic
//	{"models":[…]}               some compatible gateways
//	[…]                          a few self-hosted services return a bare array
//
// Elements can be objects or bare strings (`{"data":["gpt-4"]}`).
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
			// One unrecognizable element must not fail the whole listing: skip it, the rest carry on
			// (fail-open).
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
		// The extra-nested shape `{"data":{"models":[…]}}`.
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

// groupOf takes the grouping the upstream gives. Priority: group > groups[0] > provider > owned_by.
//
// When none is present, an id containing `/` is grouped by its first segment (OpenRouter-style
// `anthropic/…`). No fancier inference: the `gpt` that `gpt-5.6-sol` yields by splitting on `-` is
// noise, not a group.
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

// mergeModels merges one page into the accumulated result: dedupe by id, groups ordered by first
// appearance.
//
// Dedupe is not fussiness: at page boundaries the same page can be returned twice (an upstream
// bug), and after deduping the UI will not show two identical options.
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
	// A model's own group also counts as "seen", otherwise the front end's group headings would be
	// missing some.
	for _, m := range dst {
		if m.Group != "" && !has[m.Group] {
			has[m.Group] = true
			groups = append(groups, m.Group)
		}
	}
	return dst, groups
}

// cacheKey squeezes "this provider definition" into one key.
//
// It includes a hash of the key and **not the key itself**: the cache table lives in memory, and
// there is no reason for it to hold cleartext; and since the listing after a key change may be
// completely different, the key must take part.
func cacheKey(name string, p domain.Provider) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		name, p.Protocol, p.BaseURL, p.AnthropicURL, p.APIKeyEnv, p.Key())))
	return hex.EncodeToString(sum[:])
}
