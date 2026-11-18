package verify

import (
	"context"
	"encoding/json"
	"strings"

	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is the web-facing half: `POST /ui/api/ext/fingerprint`.
//
// The request body has one action:
//
//	{"action":"state"}                          all material for this screen
//	{"action":"cache","provider":"x","model":"y"} cache only (model dropdown)
//	{"action":"run","provider":"x","model":"y","api":"auto","count":3}
//
// The shape is this module's; the BFF passes it through (see web-dashboard's ExtensionService).
// `run` **really spends tokens**: each row/button sends one, the front end decides concurrency,
// and the BFF does not batch - "verify this one now" and "verify a whole profile" are two
// different things, and the backend offers only the smallest brick.
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
