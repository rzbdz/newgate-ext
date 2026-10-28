package verify

import (
	"context"
	"encoding/json"
	"strings"

	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// 本文件是交给网页的那半边：`POST /ui/api/ext/fingerprint`。
//
// 请求体只有一个 action：
//
//	{"action":"state"}                                    这一屏的全部素材
//	{"action":"cache","provider":"x","model":"y"}        只看缓存（模型下拉用）
//	{"action":"run","provider":"x","model":"y","api":"auto","count":3}
//
// 形状由本模块定义，BFF 原样转交（见 web-dashboard 的 ExtensionService）。
// `run` 是一次**真的会花 token** 的动作：界面上每一行/每一颗按钮各发一条，前端
// 自己决定并发，BFF 不替它批量——「这一条现在验」与「这一份 profile 全验」在
// 界面上是两件事，后端只提供最小的一块砖。
type webRequest struct {
	Action   string `json:"action"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	API      string `json:"api,omitempty"`
	Count    int    `json:"count,omitempty"`
}

func (s *service) handle(r webapi.ExtensionRequest) (any, error) {
	var req webRequest
	if body := strings.TrimSpace(string(r.Body)); body != "" {
		if err := json.Unmarshal(r.Body, &req); err != nil {
			return nil, i18n.Ef(err, "the fingerprint request is not valid JSON: {err}",
				i18n.A{"err": err})
		}
	}
	ctx := r.Context
	if ctx == nil {
		ctx = context.Background()
	}
	switch req.Action {
	case "", "state":
		return s.State(), nil
	case "cache":
		return s.cached(req.Provider, req.Model)
	case "run":
		return s.Run(ctx, RunRequest{
			Provider: req.Provider, Model: req.Model, API: req.API, Count: req.Count,
		})
	default:
		return nil, i18n.E("unknown fingerprint action {action} (use state, cache or run)",
			i18n.A{"action": req.Action})
	}
}
