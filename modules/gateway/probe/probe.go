package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/config/targets"
	"github.com/rzbdz/newgate-ext/modules/gateway/dialect"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Result holds the main probe conclusion and the auxiliary dialect capabilities together;
// Latency and Total are kept apart so auxiliary checks do not pollute the primary health
// score that routing uses.
type Result struct {
	Profile  string        `json:"profile"`
	Role     string        `json:"role"`
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	OK       bool          `json:"ok"`
	Status   int           `json:"status"`
	Latency  time.Duration `json:"latency_ms"`       // main probe latency; health scoring uses this
	Total    time.Duration `json:"total_latency_ms"` // includes the extra dialect/quirk probes
	Err      string        `json:"error,omitempty"`
	Cached   bool          `json:"-"` // each provider/model is really hit only once
	// Dialects are the dialects found supported ("openai+anthropic"); empty = the main
	// probe failed, nothing was probed.
	Dialects string `json:"dialects,omitempty"`
	// CountTokens says whether the anthropic-private endpoint /messages/count_tokens works.
	// nil = not probed (the main probe failed or the connection errored).
	CountTokens *bool `json:"count_tokens,omitempty"`
}

// Light condenses a probe conclusion into a terminal status lamp; it takes no part in
// routing decisions.
func (r Result) Light() string {
	switch {
	case r.OK && r.Latency < 3*time.Second:
		return "🟢"
	case r.OK:
		return "🟡" // reachable but slow
	default:
		return "🔴"
	}
}

// Target is the minimal identity a probe is deduplicated by; profile/role are only config
// views that reference it.
//
// The type itself lives in config/targets: `newgate verify` needs the same list of "which
// upstreams a profile resolves to", and it must not import the probe's implementation
// package (see the package comment there).
type Target = targets.Target

// Options controls one probing run. Callbacks let the caller render progress live.
type Options struct {
	Only        string
	Timeout     time.Duration
	SlowAfter   time.Duration
	Concurrency int

	// OnlyTarget hits **this one** binding (provider/model) only; empty = follow Only/all
	// profiles.
	//
	// The difference from Only is "what you look by": Only is a profile name (under it are
	// the chain's several candidates), while this is one specific binding. Its use case is
	// "does this one work right now" - one row of the health table, one manual single-point
	// recheck. The profile route cannot answer that: the same provider/model may hang under
	// several profiles, and the user is asking about that one.
	//
	// Set it and it **ignores profiles**: the target is named, and walking the config anyway
	// would make "click this one" hit a bunch of others on the side.
	OnlyTarget *Target

	// OnPlan is called once before probing starts, saying how many targets will be hit in total.
	OnPlan func(targets []Target)
	// OnDone is called the moment each target finishes (completion order, not a fixed order).
	OnDone func(t Target, status int, lat, totalLat time.Duration, err error)
	// OnWaiting periodically reports which targets are still stuck, and how long each has waited.
	OnWaiting func(inflight map[Target]time.Duration)
	// WaitTick is the interval of OnWaiting; 0 disables it.
	WaitTick time.Duration

	// OnNote reports a "probed, but not a failure" event: what new quirk this probe found,
	// the cache cannot be read, the cache cannot be written back.
	//
	// Why a separate channel instead of folding into OnDone's err: none of these change this
	// conclusion (this shot works), but they decide the **next** one - if the cache cannot be
	// read, the next run spends another round of tokens; if a newly learned quirk is not
	// mentioned, whoever ran probe does not know what they just found (the passive path logs
	// it; the active path used to say nothing at all).
	OnNote func(string)
}

// note reports one line that does not change the conclusion. Dropped when OnNote is not
// installed - it is display only, not a conclusion.
func (o *Options) note(format string, args ...interface{}) {
	if o.OnNote != nil {
		o.OnNote(fmt.Sprintf(format, args...))
	}
}

// Run hits every tier of the given profile (empty = all).
// The same (provider, model) is really hit only once and the result reused - saves time and
// quota.
// It also sends one CheckQuirks shot to probe for upstream quirks.
func Run(o Options) ([]Result, error) {
	if o.Concurrency <= 0 {
		o.Concurrency = 8
	}
	if o.Timeout <= 0 {
		o.Timeout = 120 * time.Second
	}
	cache, cacheErr := loadCapabilityCache()
	if cacheErr != nil {
		// Unreadable = this round probes again (spending tokens). Keep going, but say so.
		o.note("%s", i18n.T("capability cache could not be read; every target is probed again this round: {err}",
			i18n.A{"err": cacheErr}))
	}

	// Both the config view and the deduplicated targets come from config/targets: `newgate
	// verify` uses the same copy (see that package's comment). Here we only translate its Row
	// into Result's fields.
	rows, uniq, err := targets.Enumerate(targets.Options{Only: o.Only, OnlyTarget: o.OnlyTarget})
	if err != nil {
		return nil, err
	}
	// targets only answers "which upstreams"; sending requests also needs the provider
	// definition (base, key).
	provs, err := store.LoadProviders()
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(rows))
	for _, r := range rows {
		results = append(results, Result{
			Profile: r.Profile, Role: r.Role, Provider: r.Provider, Model: r.Model, Err: r.Err,
		})
	}
	if o.OnPlan != nil {
		o.OnPlan(uniq)
	}

	type outcome struct {
		status   int
		lat      time.Duration
		totalLat time.Duration
		err      error
	}
	var mu sync.Mutex
	got := map[Target]outcome{}
	inflight := map[Target]time.Time{}

	// Report periodically who is still stuck - the claude family takes 40s+ easily, and
	// without this the user thinks it died
	stopTick := make(chan struct{})
	if o.OnWaiting != nil && o.WaitTick > 0 {
		go func() {
			tk := time.NewTicker(o.WaitTick)
			defer tk.Stop()
			for {
				select {
				case <-stopTick:
					return
				case <-tk.C:
					mu.Lock()
					snap := map[Target]time.Duration{}
					for t, s := range inflight {
						snap[t] = time.Since(s)
					}
					mu.Unlock()
					if len(snap) > 0 {
						o.OnWaiting(snap)
					}
				}
			}
		}()
	}

	sem := make(chan struct{}, o.Concurrency)
	var wg sync.WaitGroup
	for _, t := range uniq {
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			mu.Lock()
			inflight[t] = time.Now()
			mu.Unlock()

			p := provs.Providers[t.Provider]
			totalStarted := time.Now()
			st, lat, err := timeoutRetry(func() (int, time.Duration, error) {
				return One(p, t.Model, o.Timeout)
			})
			if err == nil && st == http.StatusOK && o.SlowAfter > 0 && lat > o.SlowAfter {
				err = i18n.E("minimal request took {lat}, over the interactive threshold {threshold}",
					i18n.A{"lat": lat.Round(time.Millisecond), "threshold": o.SlowAfter})
			}

			if err == nil && st < 400 {
				// If dialect/quirks were already learned, restore from cache instead of spending
				// tokens again.
				if !cache.apply(t) {
					// The **return values** of these two probes used to be dropped: a new quirk
					// CheckQuirks learned was logged only on the passive path (a forward hitting
					// 400), so whoever ran probe did not know what they had just found. Now both
					// are spoken via OnNote.
					if learned := CheckQuirks(t.Provider, p, t.Model, o.Timeout); len(learned) > 0 {
						o.note("%s", i18n.T("{target} learned upstream quirks: {quirks}",
							i18n.A{"target": t, "quirks": strings.Join(learned, ", ")}))
					}
					CheckDialects(t.Provider, p, t.Model, o.Timeout)
					cache.capture(t)
				}
			}
			totalLat := time.Since(totalStarted)

			mu.Lock()
			delete(inflight, t)
			got[t] = outcome{st, lat, totalLat, err}
			mu.Unlock()

			if o.OnDone != nil {
				o.OnDone(t, st, lat, totalLat, err)
			}
		}(t)
	}
	wg.Wait()
	close(stopTick)
	// Persisting does not change this conclusion, but decides whether the next round spends
	// tokens again. The five steps in save() (Marshal/MkdirAll/WriteFile/Chmod/Rename) each
	// report errors, and the call site used to `_ =` the result - so none of those five
	// checks ever applied.
	if err := cache.save(); err != nil {
		o.note("%s", i18n.T("capability cache could not be saved; the next probe spends tokens again: {err}",
			i18n.A{"err": err}))
	}

	// Write back
	seen := map[Target]bool{}
	for i := range results {
		r := &results[i]
		if r.Provider == "" || r.Err != "" {
			continue
		}
		t := Target{Provider: r.Provider, Model: r.Model}
		o2 := got[t]
		r.Status, r.Latency, r.Total = o2.status, o2.lat, o2.totalLat
		r.OK = o2.err == nil && o2.status == 200
		if o2.err != nil {
			r.Err = o2.err.Error()
		}
		if seen[t] {
			r.Cached = true
		}
		seen[t] = true
		// Dialect capabilities: those found in the registry (one provider/model is probed
		// once and shared across rows)
		if ok, _ := dialect.Supports(r.Provider, r.Model, dialect.CapOpenAI); ok {
			r.Dialects = "openai"
		}
		if ok, _ := dialect.Supports(r.Provider, r.Model, dialect.CapAnthropic); ok {
			if r.Dialects != "" {
				r.Dialects += "+"
			}
			r.Dialects += "anthropic"
		}
		if ok, known := dialect.Supports(r.Provider, r.Model, dialect.CapCountTokens); known {
			ct := ok
			r.CountTokens = &ct
		}
	}

	// Rows are already ordered by (profile, tier order); results keep the same order.
	return results, nil
}

func timeoutRetry(attempt func() (int, time.Duration, error)) (int, time.Duration, error) {
	status, latency, err := attempt()
	if networkErr, ok := err.(net.Error); !ok || !networkErr.Timeout() {
		return status, latency, err
	}
	// The primary latency is the final probe request itself; a first timeout belongs to the
	// command's total time, and must not be stacked onto a successful retry to double the
	// health score out of thin air.
	return attempt()
}

// Light gives one probe result its lamp.
func Light(ok bool, lat time.Duration) string {
	switch {
	case ok && lat < 3*time.Second:
		return "🟢"
	case ok:
		return "🟡"
	}
	return "🔴"
}

// One sends one minimal request to a (provider, model).
func One(p domain.Provider, model string, timeout time.Duration) (int, time.Duration, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"model":      model,
		"max_tokens": 4,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	})
	suffix := "/chat/completions"
	if p.Protocol == "anthropic" {
		suffix = "/messages"
	}
	// Pick base by dialect: an upstream splitting the two dialects (provider.anthropic_url)
	// only works on the right base, and probing the wrong base yields a conclusion unrelated
	// to real traffic.
	url := p.URL(suffix)

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.Protocol == "anthropic" {
		req.Header.Set("x-api-key", p.Key())
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+p.Key())
	}

	start := time.Now()
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	lat := time.Since(start)
	if err != nil {
		return 0, lat, err
	}
	defer resp.Body.Close()
	raw, _ := ioutil.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, lat, fmt.Errorf("%s", extractErr(raw))
	}
	return resp.StatusCode, lat, nil
}

func extractErr(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// Summary aggregates per profile, to advise on "which one to switch to".
type Summary struct {
	Profile string
	OK      int
	Bad     int
	AvgMs   int64
	Grade   string
}

func Summarize(rs []Result) []Summary {
	m := map[string]*Summary{}
	seen := map[string]bool{}
	var order []string
	for _, r := range rs {
		s, ok := m[r.Profile]
		if !ok {
			s = &Summary{Profile: r.Profile}
			m[r.Profile] = s
			order = append(order, r.Profile)
		}
		target := r.Profile + "\x00" + r.Provider + "\x00" + r.Model
		if seen[target] {
			continue
		}
		seen[target] = true
		if r.OK {
			s.OK++
			s.AvgMs += r.Latency.Milliseconds()
			if r.Latency >= 3*time.Second {
				s.Grade = "usable"
			} else if s.Grade == "" {
				s.Grade = "fluent"
			}
		} else {
			s.Bad++
			s.Grade = "unavailable"
		}
	}
	var out []Summary
	for _, n := range order {
		s := m[n]
		if s.OK > 0 {
			s.AvgMs /= int64(s.OK)
		}
		out = append(out, *s)
	}
	return out
}

func UniqueCount(rs []Result) int {
	seen := map[string]bool{}
	for _, r := range rs {
		if r.Provider != "" && r.Model != "" {
			seen[r.Provider+"\x00"+r.Model] = true
		}
	}
	return len(seen)
}

func FailedCount(rs []Result) int {
	seen := map[string]bool{}
	failed := 0
	for _, r := range rs {
		if r.Provider == "" || r.Model == "" {
			continue
		}
		key := r.Provider + "\x00" + r.Model
		if seen[key] {
			continue
		}
		seen[key] = true
		if !r.OK {
			failed++
		}
	}
	return failed
}

// CheckDialects probes "which dialects this (provider, model) understands" - including the
// anthropic-private count_tokens (Claude Code's context meter relies on it, and locally it
// only has a rough estimate). Call it only after the main probe passes. Results go into the
// dialect registry.
//
// Note: this function runs in the `newgate probe` process and what it learns dies with the
// process; the daemon learns it on its own when it first sees a count_tokens (a lazy probe at
// the gate layer), and the final state matches - see the dialect package comment.
func CheckDialects(provName string, p domain.Provider, model string, timeout time.Duration) {
	declared, other := dialect.CapOpenAI, dialect.CapAnthropic
	if p.Protocol == "anthropic" {
		declared, other = dialect.CapAnthropic, dialect.CapOpenAI
	}
	dialect.Mark(provName, model, declared) // the declared protocol is a config fact, no probe needed

	learnDialect(provName, p, model, other, timeout)

	// count_tokens is a dialect-of-anthropic endpoint: if /messages does not work, no point
	// trying
	if anthOK, _ := dialect.Supports(provName, model, dialect.CapAnthropic); !anthOK {
		dialect.MarkUnsupported(provName, model, dialect.CapCountTokens)
		return
	}
	learnDialect(provName, p, model, dialect.CapCountTokens, timeout)
}

// learnDialect sends one minimal request and records "supported / explicitly unsupported".
// Connection failures and 401/429 are **not learned** - those mean "not now", not "absent",
// and a wrong guess makes gate abandon an endpoint that actually exists, for good.
func learnDialect(provName string, p domain.Provider, model string, c dialect.Cap, timeout time.Duration) {
	st, err := oneDialect(p, model, c, timeout)
	switch {
	case err == nil && st < 400:
		dialect.Mark(provName, model, c)
	case err == nil && (st == 404 || st == 405):
		dialect.MarkUnsupported(provName, model, c)
	}
}

// oneDialect sends one minimal request to probe a dialect's endpoint. auth follows the
// provider's declared protocol - same as setAuth in gate, so what is probed is the shot gate
// will send later.
func oneDialect(p domain.Provider, model string, c dialect.Cap, timeout time.Duration) (int, error) {
	suffix := "/chat/completions"
	switch c {
	case dialect.CapAnthropic:
		suffix = "/messages"
	case dialect.CapCountTokens:
		suffix = "/messages/count_tokens"
	}
	payload := map[string]interface{}{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}
	if c != dialect.CapCountTokens {
		payload["max_tokens"] = 4
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	// URL construction goes through the provider's own: an upstream splitting the two dialects
	// (anthropic_url) relies on it to pick the right base - the probed path must be exactly
	// what gate will send.
	req, err := http.NewRequest("POST", p.URL(suffix),
		bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.Protocol == "anthropic" {
		req.Header.Set("x-api-key", p.Key())
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+p.Key())
	}

	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = ioutil.ReadAll(resp.Body) // drain to reuse the connection; the error body is not needed
	// A 4xx is not an error: 404/405 is exactly the answer "no such endpoint", and the
	// caller learns from the status code
	return resp.StatusCode, nil
}

// CheckQuirks actively sends one "request with tools" to learn upstream quirks in advance.
//
// Why this shot must be separate: an ordinary probe sends "hi", **without tools**. And the
// glm-5.3 400 appears precisely only with tools - the aggregator injects "disable thinking"
// for us only when it sees tools. So `newgate probe` shows all green while real traffic 400s
// on every request; a probe more optimistic than reality is the worst kind of probe.
//
// It is sent only after an ordinary probe passes, and only once per (provider, model): one
// more shot is one more quota unit and one more chance to hit a rate limit.
//
// It returns the quirks learned (plain language). nil if nothing was learned - including the
// case where the request itself failed: a failed probe does not mean the model is quirky, and
// we must not write guesses into the registry.
func CheckQuirks(provName string, p domain.Provider, model string, timeout time.Duration) []string {
	payload := map[string]interface{}{
		"model":      model,
		"max_tokens": 4,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"tools": []map[string]interface{}{{
			"type": "function",
			"function": map[string]interface{}{
				"name":        "newgate_probe",
				"description": "probe",
				"parameters": map[string]interface{}{
					"type": "object", "properties": map[string]interface{}{}, "required": []string{},
				},
			},
		}},
	}
	suffix := "/chat/completions"
	if p.Protocol == "anthropic" {
		// Anthropic-dialect tools are flat, with no function wrapper
		payload["tools"] = []map[string]interface{}{{
			"name": "newgate_probe", "description": "probe",
			"input_schema": map[string]interface{}{
				"type": "object", "properties": map[string]interface{}{}, "required": []string{},
			},
		}}
		suffix = "/messages"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}

	// URL construction always goes through the provider's own copy: for upstreams whose two
	// dialects split (anthropic_url) that is what picks the right base -- the probe path
	// must be exactly the one the gate will send later.
	req, err := http.NewRequest("POST", p.URL(suffix),
		bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	if p.Protocol == "anthropic" {
		req.Header.Set("x-api-key", p.Key())
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+p.Key())
	}

	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	raw, _ := ioutil.ReadAll(resp.Body) // error body is small
	return quirk.Default.Learn(provName, model, resp.StatusCode, raw)
}
