package setup

// 本文件是「用户只写一个 base 和 key，模型名从上游列出来挑」那一格。
//
// 它问的是 OpenAI 兼容的 `GET {base}/models`——newapi / one-api / DeepSeek /
// GLM / OpenRouter 那一片聚合网关都答这个。**问不到不是错误**：私有部署、只认
// 自家 SDK 的服务、公司网关都可能没有这个接口，那时用户手填一个模型名就行
// （那本来就是这条向导的最小要求）。
//
// # 为什么不去 import modules/modellist
//
// modellist 是**发行版的另一个模块**：它把清单交给网页下拉（BFF 扩展端点）、
// 带进程内缓存与分组。向导只要「此刻列一下」，而把它拉进来会让 setup 对一份
// 别的模块的内部实现（service 结构、缓存策略）产生依赖——archmap 那条
// 「import 了却没有任何依赖边」的探针正是为这种依赖准备的。
//
// 两边共用的是**协议**（`/models` 的形状），不是代码。这一点与「内核与发行版
// 共用同一把尺子但各自实现」是同一种取舍：协议是稳定的，实现会动。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	term "github.com/rzbdz/newgate-ext/lib/term"
	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	"github.com/rzbdz/newgate/modules/config/domain"
)

const (
	modelsTimeout = 8 * time.Second
	modelsMaxBody = 2 << 20
)

// probeModels 是问清单的那一次网络调用。
//
// 留成一个可替换的函数，是为了**测试不出网**（见 docs/10-testing-security.md：
// 单测一律 httptest，出网即失败）——向导的流程测试要验的是「拿到清单之后怎么
// 选」，不该顺带把自己的 DNS 也验一遍。
var probeModels = listModels

// listModels 问上游要模型名。返回名字（去重、已排序）与一句「为什么没问到」。
//
// 第二个返回值非空 = 这次没问到（不是致命错误）；调用方提示用户手填。
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

// fetchModelList 问一个地址。retry=true 表示「这个地址没有这个接口，试下一个」。
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
		// 地址找对了、认证没过。这不是「换个地址再试」能解决的——直说。
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

// parseModelNames 认三种形状：
//
//	{"data":[{"id":"…"}]}        OpenAI / newapi / DeepSeek / GLM / OpenRouter
//	{"models":[{"name":"…"}]}    Ollama 一类
//	["a","b"]                    最裸的那种
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
			// newapi 有时把名字放在 name 里、id 是数字。
			Name string `json:"name"`
		} `json:"data"`
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	pick := func(id, name string) {
		// newapi 一类有时把 `id` 写成一个数字序号、真名字在 `name` 里。数字 id
		// 不是模型名（贴进绑定里上游不认），有 name 时用 name。
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

// modelURLs 与 modellist 的同一套规则：base 带不带 /v1 两种写法都试。
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

// safeURL 去掉 URL 里的 userinfo（有人把凭据贴在地址里，那不该进终端）。
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

// pickModel 让用户在清单里挑一个，或者手填。
//
// 默认光标停在**清单的第一个**；按 `m` 切到「自己填」。清单是上游此刻的真话
// （有的聚合网关一次列几百个），而手填那一条必须永远在——它是「问不到清单」
// 那个场景的正式出口，不是兜底。
func pickModel(s Screen, models []string, why string) (string, bool) {
	if len(models) == 0 {
		// 没清单就直接问一句，不摆一个只有「自己填」的列表。
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
