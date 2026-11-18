package setup

// This file is the cell "the user writes one base and key, models are listed upstream".
//
// It asks the OpenAI-compatible `GET {base}/models` - newapi / one-api / DeepSeek /
// GLM / OpenRouter aggregators all answer it. **A failed ask is not an error**: private
// deployments, SDK-only services, corporate gateways may lack it; typing a model name
// is fine then (that is the wizard's minimum requirement anyway).
//
// # Why not import modules/modellist
//
// modellist is **another module of the distribution**: it feeds the web dropdown (a BFF
// extension endpoint), with in-process cache and groups. The wizard only needs "list it
// now", and pulling it in makes setup depend on another module's internals (service
// struct, cache policy) - archmap's "imports with no dependency edge" probe exists for this.
//
// What both share is the **protocol** (`/models`'s shape), not code. Same trade as
// "kernel and distribution share one ruler but each implements it": protocol stable, impl moves.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/lib/style"
	term "github.com/rzbdz/newgate-ext/lib/term"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate/lib/i18n"
)

const (
	modelsTimeout = 8 * time.Second
	modelsMaxBody = 2 << 20
)

// probeModels is the one network call that asks for the list.
//
// It stays a replaceable function so **tests never go to the network** (see
// docs/10-testing-security.md: unit tests use httptest only) - the wizard's flow tests
// check "how to pick after getting the list", not the machine's DNS.
var probeModels = listModels

// listModels asks upstream for model names. Returns names (deduped, sorted) plus why not.
//
// Non-empty second return = could not ask (not fatal); the caller offers manual entry.
func listModels(baseURL, apiKey, protocol string) ([]string, string) {
	p := domain.Provider{BaseURL: baseURL, APIKey: apiKey, Protocol: protocol}
	ctx, cancel := context.WithTimeout(context.Background(), modelsTimeout)
	defer cancel()
	client := &http.Client{Timeout: modelsTimeout}

	var last string
	for _, url := range modelURLs(p.BaseURL) {
		names, why, retry := fetchModelList(ctx, client, url, p)
		if retry {
			last = why
			continue
		}
		if why != "" {
			return nil, why
		}
		return names, ""
	}
	if last == "" {
		last = i18n.T("the provider does not list models", nil)
	}
	return nil, last
}

// fetchModelList asks one address. retry=true means "no such endpoint here, try the next".
func fetchModelList(ctx context.Context, c *http.Client, url string, p domain.Provider) ([]string, string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, i18n.T("cannot build the request: {err}", i18n.A{"err": err.Error()}), false
	}
	req.Header.Set("Accept", "application/json")
	key := p.Key()
	switch {
	case strings.EqualFold(p.Protocol, "anthropic"):
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, i18n.T("cannot reach {url}: {err}", i18n.A{"url": safeURL(url), "err": err.Error()}), false
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return nil, i18n.T("{url} has no model list (HTTP {status})",
			i18n.A{"url": safeURL(url), "status": resp.StatusCode}), true
	case http.StatusUnauthorized, http.StatusForbidden:
		// Right address, auth failed. Retrying another address will not fix it - say so.
		return nil, i18n.T("{url} rejected the key (HTTP {status}); paste a model name by hand",
			i18n.A{"url": safeURL(url), "status": resp.StatusCode}), false
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, i18n.T("{url} answered HTTP {status}", i18n.A{"url": safeURL(url), "status": resp.StatusCode}), false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, modelsMaxBody))
	if err != nil {
		return nil, i18n.T("cannot read the model list: {err}", i18n.A{"err": err.Error()}), false
	}
	names := parseModelNames(body)
	if len(names) == 0 {
		return nil, i18n.T("{url} answered with no models", i18n.A{"url": safeURL(url)}), false
	}
	return names, "", false
}

// parseModelNames knows three shapes:
//
//	{"data":[{"id":"…"}]}        OpenAI / newapi / DeepSeek / GLM / OpenRouter
//	{"models":[{"name":"…"}]}    Ollama and friends
//	["a","b"]                    the barest one
func parseModelNames(b []byte) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	var obj struct {
		Data []struct {
			ID string `json:"id"`
			// newapi sometimes puts the name in name and a number in id.
			Name string `json:"name"`
		} `json:"data"`
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	pick := func(id, name string) {
		// newapi-type servers sometimes put a numeric index in `id`, the real name in `name`.
		// A numeric id is not a model name (upstream rejects it); prefer name when present.
		if id != "" && !allDigits(id) {
			add(id)
			return
		}
		add(name)
	}
	if err := json.Unmarshal(b, &obj); err == nil {
		for _, m := range obj.Data {
			pick(m.ID, m.Name)
		}
		for _, m := range obj.Models {
			pick(m.ID, m.Name)
		}
	}
	if len(out) == 0 {
		var arr []any
		if err := json.Unmarshal(b, &arr); err == nil {
			for _, v := range arr {
				switch t := v.(type) {
				case string:
					add(t)
				case map[string]any:
					if s, ok := t["id"].(string); ok {
						add(s)
					} else if s, ok := t["name"].(string); ok {
						add(s)
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// modelURLs follows modellist's rule: try base with and without /v1.
func modelURLs(base string) []string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if b == "" {
		return nil
	}
	if strings.HasSuffix(b, "/models") {
		return []string{b}
	}
	if strings.HasSuffix(b, "/v1") {
		return []string{b + "/models"}
	}
	return []string{b + "/v1/models", b + "/models"}
}

// safeURL strips userinfo from a URL (people paste credentials in; it must not reach the terminal).
func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func safeURL(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if j := strings.IndexByte(rest, '@'); j >= 0 {
			if k := strings.IndexAny(rest, "/?"); k < 0 || j < k {
				return raw[:i+3] + rest[j+1:]
			}
		}
	}
	return raw
}

// pickModel lets the user pick from the list, or type one.
//
// The cursor starts on **the first list entry**; `m` switches to "type my own". The list
// is upstream's truth right now (some aggregators list hundreds), and the manual entry
// must always be there - it is the official exit for "no list", not a fallback.
func pickModel(s Screen, models []string, why string) (string, bool) {
	if len(models) == 0 {
		// With no list, just ask - do not show a list holding only "type my own".
		f := []field{{
			Label:       i18n.T("Model name", nil),
			Placeholder: i18n.T("the exact name this endpoint accepts, e.g. deepseek-chat", nil),
			Note:        why,
		}}
		r := form(s, i18n.T("which model?", nil), "", f)
		if r.Cancel {
			return "", true
		}
		return r.Values[0], false
	}

	manual := false
	var typed string
	cur := 0
	for {
		drawModels(models, cur, manual, typed, why)
		if manual {
			r := s.ReadLine(term.LineMode{
				Placeholder: i18n.T("type the model name", nil),
				Validate: func(t string) string {
					if t == "" {
						return i18n.T("this one is required", nil)
					}
					return ""
				},
			}, func(text, errMsg string, _ int) {
				drawModels(models, cur, true, text, errMsg)
			})
			if r.Cancel {
				return "", true
			}
			if r.EOF {
				return "", true
			}
			return r.Text, false
		}
		ev := s.ReadEvent()
		switch ev.Key {
		case term.KeyUp:
			if cur > 0 {
				cur--
			}
		case term.KeyDown:
			if cur < len(models)-1 {
				cur++
			}
		case term.KeyRune:
			switch ev.Rune {
			case 'j':
				if cur < len(models)-1 {
					cur++
				}
			case 'k':
				if cur > 0 {
					cur--
				}
			case 'm', 'M':
				manual = true
			}
		case term.KeyEnter:
			return models[cur], false
		case term.KeyEsc, term.KeyInterrupt, term.KeyEOF:
			return "", true
		}
	}
}

func drawModels(models []string, cur int, manual bool, typed, note string) {
	var b strings.Builder
	b.WriteString(clear)
	b.WriteString(style.Bold(" newgate · "+i18n.T("first setup", nil)) + "\n")
	b.WriteString(style.Dim(" "+i18n.T(
		"↑/↓ or j/k move · enter choose · m type a name instead · esc back · ctrl-c quit", nil)) + "\n\n")
	b.WriteString(" " + style.Bold(i18n.T("The endpoint lists these models — pick the default one", nil)) + "\n")
	if note != "" {
		b.WriteString(style.Dim(" "+note) + "\n")
	}
	b.WriteString("\n")
	for i, m := range models {
		line := "   " + m
		if i == cur && !manual {
			b.WriteString(rev + padRight(line, 74) + reset + "\n")
		} else {
			b.WriteString(line + "\n")
		}
	}
	b.WriteString("\n")
	if manual {
		b.WriteString(" " + style.Cyan("›") + " " + i18n.T("Type it yourself", nil) + "\n")
		b.WriteString("   " + rev + " " + typed + " " + reset + "\n")
	} else {
		b.WriteString(style.Dim("   "+i18n.T("press m to type a name that is not listed", nil)) + "\n")
	}
	fmt.Print(b.String())
}
