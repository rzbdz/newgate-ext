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

// Result 同时保存主探活结论和附加方言能力；
// Latency 与 Total 分开，避免附加检查污染路由使用的首要健康评分。
type Result struct {
	Profile  string        `json:"profile"`
	Role     string        `json:"role"`
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	OK       bool          `json:"ok"`
	Status   int           `json:"status"`
	Latency  time.Duration `json:"latency_ms"`       // 主探活耗时，健康评分只用它
	Total    time.Duration `json:"total_latency_ms"` // 含方言/quirk 附加探测
	Err      string        `json:"error,omitempty"`
	Cached   bool          `json:"-"` // 同一个 provider/model 只真打一次
	// Dialects 探明支持的方言（"openai+anthropic"），空 = 主探活没过、没探。
	Dialects string `json:"dialects,omitempty"`
	// CountTokens anthropic 私有端点 /messages/count_tokens 是否可用。
	// nil = 没探到（主探活失败或连接错误）。
	CountTokens *bool `json:"count_tokens,omitempty"`
}

// Light 把探活结论压缩为终端状态灯，不参与实际路由决策。
func (r Result) Light() string {
	switch {
	case r.OK && r.Latency < 3*time.Second:
		return "🟢"
	case r.OK:
		return "🟡" // 通但慢
	default:
		return "🔴"
	}
}

// Target 是去重探活的最小身份；profile/role 只是引用它的配置视图。
//
// 类型本体住在 config/targets：`newgate verify` 也要同一份「profile 解析成
// 哪些上游」的名单，而它不该 import 探针的实现包（见那边的包注释）。
type Target = targets.Target

// Options 控制一次探测。回调让调用方能实时渲染进展。
type Options struct {
	Only        string
	Timeout     time.Duration
	SlowAfter   time.Duration
	Concurrency int

	// OnlyTarget 只打**这一条** binding（provider/model），空 = 按 Only/全部 profile 走。
	//
	// 与 Only 的区别是「按什么找」：Only 是一个 profile 名（那下面是整条链的
	// 几个候选），而这个是某一条具体的 binding。它的用例是「这一条现在到底通不通」
	// ——健康表上的一行、一次手动的单点复验。走 profile 那条路给不出这个答案：
	// 同一个 provider/model 可能挂在好几个 profile 下，而用户问的是那一条。
	//
	// 设了它就**不看 profile**：目标已经点名了，再去遍历配置只会让「点一下这条」
	// 顺带打一串别的。
	OnlyTarget *Target

	// OnPlan 在开始探测前调用一次，告知总共要打几个目标。
	OnPlan func(targets []Target)
	// OnDone 每个目标一完成就立刻调用（完成顺序，非固定顺序）。
	OnDone func(t Target, status int, lat, totalLat time.Duration, err error)
	// OnWaiting 定期告知还卡在哪些目标上，以及各自已等了多久。
	OnWaiting func(inflight map[Target]time.Duration)
	// WaitTick OnWaiting 的间隔，0 表示不启用。
	WaitTick time.Duration

	// OnNote 报告一次「探到了，但不算失败」的事：这次探活了什么新毛病、缓存
	// 读不出来、缓存写不回去。
	//
	// 为什么单独开一个口子而不是并进 OnDone 的 err：这些事都不改变本次结论
	// （这一发是通的），但它们决定**下一次**的行为——缓存读不出来，下次还要
	// 再花一轮 token；学到的新毛病没说出来，敲 probe 的人不知道自己刚发现了
	// 什么（被动路径是打日志的，主动路径以前什么都不说）。
	OnNote func(string)
}

// note 报告一句不改变结论的话。没装 OnNote 就丢弃——它只是展示，不是结论。
func (o *Options) note(format string, args ...interface{}) {
	if o.OnNote != nil {
		o.OnNote(fmt.Sprintf(format, args...))
	}
}

// Run 把指定 profile（空 = 全部）的每个档位都打一遍。
// 同一个 (provider, model) 只真打一次，结果复用——省时间也省额度。
// 也会附带打一发 CheckQuirks 探上游毛病。
func Run(o Options) ([]Result, error) {
	if o.Concurrency <= 0 {
		o.Concurrency = 8
	}
	if o.Timeout <= 0 {
		o.Timeout = 120 * time.Second
	}
	cache, cacheErr := loadCapabilityCache()
	if cacheErr != nil {
		// 读不出来 = 这一轮要重新探（花 token）。继续跑，但说出来。
		o.note("%s", i18n.T("capability cache could not be read; every target is probed again this round: {err}",
			i18n.A{"err": cacheErr}))
	}

	// 配置视图与去重目标都由 config/targets 给：`newgate verify` 用的是同一份
	// （见那个包的注释）。这里只把它的 Row 翻译成 Result 的字段。
	rows, uniq, err := targets.Enumerate(targets.Options{Only: o.Only, OnlyTarget: o.OnlyTarget})
	if err != nil {
		return nil, err
	}
	// targets 只回答「哪些上游」；发请求还要 provider 的定义（base、key）。
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

	// 定期汇报还卡在谁身上——claude 家族动辄 40s+，没这个用户会以为死了
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
				// 方言/quirk 已学过就直接恢复缓存，不再重复花 token。
				if !cache.apply(t) {
					// 这两条探测的**返回值**以前被丢掉：CheckQuirks 学到的新毛病
					// 只在被动路径（转发撞 400）才打日志，主动敲 probe 的人反而
					// 不知道自己刚发现了什么。现在都经 OnNote 说出来。
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
	// 落盘不改变本次结论，但决定下一轮要不要重新花 token。save() 里五步
	// （Marshal/MkdirAll/WriteFile/Chmod/Rename）每一步都在报错，以前调用点
	// `_ =` 掉了——那五步检查一次都没用上。
	if err := cache.save(); err != nil {
		o.note("%s", i18n.T("capability cache could not be saved; the next probe spends tokens again: {err}",
			i18n.A{"err": err}))
	}

	// 回填
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
		// 方言能力：注册表里探明的（同一个 provider/model 只探一次，行间共享）
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

	// Rows 已经按（profile, 档位顺序）排好；结果沿用同一顺序。
	return results, nil
}

func timeoutRetry(attempt func() (int, time.Duration, error)) (int, time.Duration, error) {
	status, latency, err := attempt()
	if networkErr, ok := err.(net.Error); !ok || !networkErr.Timeout() {
		return status, latency, err
	}
	// 主延迟表示最终这次探测请求本身；第一次超时属于命令总耗时，
	// 不能叠到成功重试上把健康评分凭空翻倍。
	return attempt()
}

// Light 给一次探测结果配灯。
func Light(ok bool, lat time.Duration) string {
	switch {
	case ok && lat < 3*time.Second:
		return "🟢"
	case ok:
		return "🟡"
	}
	return "🔴"
}

// One 对一个 (provider, model) 打一次最小请求。
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
	// 按方言挑 base：两种方言分家的上游（provider.anthropic_url）只有走对
	// base 才通，探错 base 会得到一个和真实流量无关的结论。
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

// Summary 按 profile 汇总，用于给出「该切哪个」的建议。
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

// CheckDialects 探「这个 (provider, model) 听得懂哪些方言」——包括
// anthropic 私有的 count_tokens（Claude Code 的水位条靠它，本地只有
// 粗估）。主探活通过后才该调。结果记进 dialect 注册表。
//
// 注意：本函数在 `newgate probe` 的进程里跑，学到的随进程消失；daemon
// 会在自己遇到第一个 count_tokens 时补学（gate 层面的 lazy probe），
// 最终状态一致——见 dialect 包注释。
func CheckDialects(provName string, p domain.Provider, model string, timeout time.Duration) {
	declared, other := dialect.CapOpenAI, dialect.CapAnthropic
	if p.Protocol == "anthropic" {
		declared, other = dialect.CapAnthropic, dialect.CapOpenAI
	}
	dialect.Mark(provName, model, declared) // 声明的协议是配置事实，不用探

	learnDialect(provName, p, model, other, timeout)

	// count_tokens 是 anthropic 方言的端点：/messages 都不通就不用试了
	if anthOK, _ := dialect.Supports(provName, model, dialect.CapAnthropic); !anthOK {
		dialect.MarkUnsupported(provName, model, dialect.CapCountTokens)
		return
	}
	learnDialect(provName, p, model, dialect.CapCountTokens, timeout)
}

// learnDialect 打一发最小请求，按结果记「支持/明确不支持」。
// 连接失败和 401/429 这类**不学**——那是「现在不行」，不是「没有」，
// 猜错了会让 gate 永久放弃一个本来存在的端点。
func learnDialect(provName string, p domain.Provider, model string, c dialect.Cap, timeout time.Duration) {
	st, err := oneDialect(p, model, c, timeout)
	switch {
	case err == nil && st < 400:
		dialect.Mark(provName, model, c)
	case err == nil && (st == 404 || st == 405):
		dialect.MarkUnsupported(provName, model, c)
	}
}

// oneDialect 打一发最小请求探某个方言的端点。auth 跟 provider 声明的
// protocol 走——与 gate 的 setAuth 一致，探的就是 gate 将来会发的那条。
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

	// URL 构造统一走 provider 自己那份：两种方言分家的上游（anthropic_url）
	// 靠它选对 base——探的路径必须和 gate 将来发的一模一样。
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
	_, _ = ioutil.ReadAll(resp.Body) // 排空以复用连接；错误体不需要
	// 4xx 不算错误：404/405 正是「没有这个端点」的答案，调用方按状态码学
	return resp.StatusCode, nil
}

// CheckQuirks 主动探一发「带 tools 的请求」，把上游的毛病提前学出来。
//
// 为什么必须单独探这一发：普通探活发的是一句 "hi"，**不带 tools**。而
// glm-5.3 那个 400 恰恰只在带 tools 时出现——聚合器看见 tools 才会替我们
// 塞「关闭思考」。于是 `newgate probe` 一片全绿，真实流量却每条都 400，
// 探活比现实乐观是最坏的一种探活。
//
// 只在普通探活通过后再打，且一个 (provider, model) 只打一次：多打一发就多
// 一次额度和一次撞限流的机会。
//
// 返回学到的毛病（人话）。什么都没学到就返回 nil——包括请求本身失败的情况：
// 探测失败不代表模型有毛病，不能凭猜往注册表里写。
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
		// Anthropic 方言的 tools 是平铺的，没有 function 包一层
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

	// URL 构造统一走 provider 自己那份：两种方言分家的上游（anthropic_url）
	// 靠它选对 base——探的路径必须和 gate 将来发的一模一样。
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
