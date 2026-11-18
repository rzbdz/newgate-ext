package forward

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rzbdz/newgate-ext/lib/logx"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlpath"
	"github.com/rzbdz/newgate-ext/modules/gateway/dialect"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate-ext/modules/gateway/metrics"
	"github.com/rzbdz/newgate-ext/modules/gateway/policy"
	"github.com/rzbdz/newgate-ext/modules/gateway/probe"
	"github.com/rzbdz/newgate-ext/modules/gateway/protocol"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
	"github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
	schema "github.com/rzbdz/newgate-ext/modules/gateway/rewrite/schema"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate-ext/modules/gateway/thinkcache"
	"github.com/rzbdz/newgate-ext/modules/runtime/daemon"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Server 拥有代理数据面的 listener、热配置快照和优雅交接状态。
// 它是运行资源而不是组件端口；生命周期由上层 daemon/CLI 明确控制。
type Server struct {
	Port int
	// Host 是监听地址；空串 = 回环（见 domain.State.Host 那段注释：放开它是个
	// 明确的决定，默认不动）。
	Host   string
	Logger *log.Logger
	// Watch 配置快照的持有者。热路径只做一次原子指针读，不碰文件系统。
	// 改了配置**不需要重启**：watcher 换页后，新进来的请求就用新配置，
	// 正在跑的会话不受影响（各 agent 读各自的绑定）。
	Watch *store.Watcher

	// Filters 是注入进来的数据面策略贡献者（准入、结局裁决、控制面自报、
	// 停机落盘，见 modules/gateway/policy）。
	//
	// 数据面**不认识任何一家策略**：它只知道自己有这四个决策点，谁回答、
	// 按什么规则回答是别人的事。空账本 = 最小系统（全部候选可用、按机制
	// 换站、不记账）。
	Filters *policy.Registry

	srv      *http.Server
	ln       net.Listener // 优雅交接要把它作为 fd 移交新进程
	requests uint64
	failures uint64
	started  time.Time

	// mux 是数据面自己的 handler（7 条 pattern，catch-all 就是转发）；
	// root 非 nil 时用它当这个端口的根 handler。
	//
	// 这个接缝存在的理由：**端口上可能不止一个服务**（web 界面这类服务想共用
	// 同一个端口）。谁来决定「这个请求归谁」，不该由数据面回答——所以这里只留
	// 一个空位，由守护进程入口把根 handler 装进来。数据面因此完全不认识那张
	// 挂载表，也不认识任何一位房客（这条由 direction_test.go 钉住）。
	//
	// 默认（root == nil）就是数据面自己服务端口，也就是共享端口这件事出现之前
	// 的样子：单机测试与 testing/system 的 harness 走的都是这条路。
	mux  http.Handler
	root http.Handler

	stopOnce sync.Once
	stopCh   chan struct{}

	// pendingQuirks 是「转发路上撞出来、还没落盘」的上游毛病（见 probe 包的
	// PendingQuirks）。字段在 Server 上而不是 probe 包里：落盘的时刻是**停机**
	// （见 Shutdown），而数据面才是那个知道「这台机器要走了」的人。
	//
	// 为什么不是「撞到就立刻落盘」：那是在每一次上游 4xx 上写一次盘，而 4xx
	// 是热路径上完全正常的一种结局（形状 400、参数错、配额），代价与收益不成
	// 比例。为什么不是「原样留着不管」：那正是用户 2026-09-22 报的那个必现窗口
	// ——重启后内存表是空的，第一发 Codex → GLM 重新撞一次 400。
	pendingQuirks probe.PendingQuirks

	// 优雅交接的排空状态（交接后的**旧**进程用）：
	//   draining=1 之后，本进程的 pid/lock 已改写到新进程名下——任何
	//   退出路径都不许再清它们；主循环（Serve 返回处）要等在途请求
	//   流完才能退，否则 Shutdown 一关 listener 主函数就 return，
	//   进程当场消失，排空等于没排。
	drainOnce sync.Once
	drainCh   chan struct{}
	drainFlag int32
}

// New 构造尚未监听的 Server，使配置、日志和策略账本在启动副作用前就完整可见。
//
// host 为空 = 只绑回环（默认）。绑定地址是启动参数而不是热配置：换监听地址要
// 重新 bind，那不是热路径能做的事（与 Port 同级）。
func New(host string, port int, lg *log.Logger, w *store.Watcher, filters *policy.Registry) *Server {
	if filters == nil {
		filters = policy.New()
	}
	return &Server{Host: host, Port: port, Logger: lg, Watch: w, Filters: filters,
		stopCh: make(chan struct{}), drainCh: make(chan struct{})}
}

// RequestStop 外部请求停机（控制端点用）。幂等，重复调用无害。
// stopCh 为 nil（直接拿 &Server{} 字面量构造的测试桩）时静默跳过。
func (s *Server) RequestStop() {
	s.stopOnce.Do(func() {
		if s.stopCh != nil {
			close(s.stopCh)
		}
	})
}

// StopRequested 收听停机请求。收到即代表有人（通常是别的用户的
// `newgate stop`）让这个进程退出。
func (s *Server) StopRequested() <-chan struct{} { return s.stopCh }

// Draining 本进程是否处于优雅交接的排空期（socket 已移交新进程）。
// 排空期的退出路径不能清 pid/lock——那是新进程的了。
func (s *Server) Draining() bool { return atomic.LoadInt32(&s.drainFlag) == 1 }

// Drained 在途请求排空完成的信号。只在 Draining 时有意义。
func (s *Server) Drained() <-chan struct{} { return s.drainCh }

// snap 当前配置快照。watcher 缺失时退化为直接读盘（测试路径）。
func (s *Server) snap() *store.Snapshot {
	if s.Watch != nil {
		return s.Watch.Current()
	}
	sn, err := store.Load()
	if err != nil {
		return nil
	}
	return sn
}

func (s *Server) logf(format string, a ...interface{}) {
	if s.Logger != nil {
		s.Logger.Printf(format, a...)
	}
}

// outcome 是数据面回报上游结局的唯一入口：一次裁决 + 计数器 + 日志素材。
// 四个调用点（连接失败、状态码、流中断、成功）都走它，免得某个点漏掉指标。
//
// 它只做**机制**：把「这一次到底怎么了」原样交给注册进来的策略，拿到判决后
// 执行。至于「该不该记这本账」「留什么痕」全是策略的事——数据面不认识它们，
// 包括计数器的名字。
func (s *Server) outcome(o policy.Outcome) policy.Verdict {
	v := s.Filters.Judge(o)
	for _, key := range v.Metrics {
		metrics.Default.Inc(key)
	}
	return v
}

// advance 内核的换站机制：响应还没开始写、链上还有下一站。
//
// 这是唯一的默认方向，且**策略只能叫停**（Verdict.Stop）——不存在策略要求
// 前进而机制要停的情形（核对过旧的失败分类表全表：除「其它 4xx」那一处政策
// 之外，换站与否恒等于这个式子）。所以判决与机制的合成就是一次 OR。
func advance(o policy.Outcome, v policy.Verdict) bool {
	return !o.ResponseStarted && !o.IsLast && !v.Stop
}

// verifyProbeAttempts / verifyProbeTimeout 是「上闸前诊断」的形状：连续打
// 最多两次最小探活，**任一次 200 就算这条 binding 还活着**。
//
// 为什么是「任一成功」而不是「两次都成功」：要回答的问题是「它到底还能不能
// 通」，一次成功就是确凿的能通；而一次失败可能只是排队/抖动。用户的原话是
// 「就算间歇性能通也算」——这条路径要防的正是「明明能通却被摘牌」。
//
// 为什么超时刻意短：这是**替用户等**的时间。真实请求已经失败，用户正等
// fallback；诊断再慢一点，就是拿一个已经不爽的请求去换一个更准的结论。
// 8s 够打一个 max_tokens=4 的最小请求（正常上游是百毫秒级）。
const (
	verifyProbeAttempts = 2
	verifyProbeTimeout  = 8 * time.Second
)

// probe 是数据面借给策略的「主动探活」能力（policy.Env.Probe）。
//
// 现场（2026-09-17 实测，日志与 health.json 都在）：smt-deepseek 被摘了好几次，
// 每次 reason 都是「真实流量连续失败」，而每一次手动 `newgate probe` 都是
// fluent——因为被动路径失败的是**首字节超时**（分类器那条链把 126KB 的 system
// 塞进 12s 的紧预算），而 probe 发最小请求，压根碰不到那个边界。两种证据冲突
// 时，主动、可控、可重复的那一个更硬；而摘牌的代价不对称（用户被悄悄换给别的
// 模型、要等 60s 起的冷却），所以上闸前必须再要一次主动证据。
//
// 这个能力归数据面：它要读当前配置快照（provider 的 base、key、方言）并复用
// 数据面的探活实现。策略只是**借**它——上一版是策略反过来要求数据面注入一个
// 回调（`SetVerifier`），方向是反的。
//
// fail-closed：拿不到 provider 配置、key 为空、探活报错，一律返回 false
// ——诊断**不能**成为坏 binding 的免死金牌，它只该拦住误判。
func (s *Server) probe(provider, model string) bool {
	snap := s.snap()
	if snap == nil || snap.Providers == nil {
		return false
	}
	p, ok := snap.Providers.Providers[provider]
	if !ok || p.Key() == "" {
		return false
	}
	for i := 0; i < verifyProbeAttempts; i++ {
		status, latency, err := probe.One(p, model, verifyProbeTimeout)
		s.logf("[probe] %s", i18n.T(
			"pre-gate diagnosis {provider}/{model} attempt {attempt}/{total}: HTTP {status} {err} ({latency})",
			i18n.A{"provider": provider, "model": model, "attempt": i + 1,
				"total": verifyProbeAttempts, "status": status, "err": err,
				"latency": latency.Round(time.Millisecond)}))
		if err == nil && status == 200 {
			return true
		}
	}
	return false
}

// serverEnv 是数据面交给策略的运行期能力（policy.Env）。
//
// 时刻很重要：它由 Server.Start 在**服务起来的时候**装上，不是装配期——因为
// 它给的正是「此刻活着的这件东西」（日志、当前配置快照下的探活）。见
// docs/02-component-framework.md 的三段法则。
type serverEnv struct{ s *Server }

// Logf 策略的日志出口。消息**整条由策略给**（数据面不替它加前缀）：谁的知识
// 谁起名，数据面不该出现「健康表」这类别人的词。
func (e serverEnv) Logf(format string, args ...any) { e.s.logf(format, args...) }

func (e serverEnv) Probe(provider, model string) bool { return e.s.probe(provider, model) }

// Handler 造出数据面在这个端口上的 handler。
//
// 想在同一个端口上再挂服务的人（守护进程入口）**先问它要一份**，把它当作兜底
// 包进自己的路由，再用 SetRoot 装回来。幂等：反复调用拿到同一份（mux 建一次
// 就存下来）——兜底与端口服务的必须是同一个东西。
func (s *Server) Handler() http.Handler {
	if s.mux == nil {
		mux := http.NewServeMux()
		mux.HandleFunc(controlpath.Status, s.handleStatus)
		mux.HandleFunc(controlpath.Metrics, s.handleMetrics)
		mux.HandleFunc(controlpath.Health, s.handleHealth)
		mux.HandleFunc(controlpath.Stop, s.handleControlStop)
		mux.HandleFunc(controlpath.Upgrade, s.handleControlUpgrade)
		mux.HandleFunc("/v1/models", s.handleModels)
		// catch-all：数据面。走到这里的路径要么是数据面自己的（/v1、/a），要么
		// 是别人明确交给它的（端口上没人认领的那些）。
		mux.HandleFunc("/", s.handleProxy)
		s.mux = mux
	}
	return s.mux
}

// SetRoot 换掉这个端口的根 handler。**必须在 Start 之前**调（Start 之后
// http.Server 已经拿着旧的那份了，改了不会生效，而且没人会告诉你）。
func (s *Server) SetRoot(h http.Handler) { s.root = h }

// rootHandler 是 Start 真正交给 http.Server 的那一个。
//
// 单独一个函数只为一件事：让「端口交给谁」这件事**可测**——测试调它、Start 调它，
// 是同一条路径（不然测试只能自己去起监听、连端口，而那样测的是别的东西）。
func (s *Server) rootHandler() http.Handler {
	if s.root != nil {
		return s.root
	}
	return s.Handler()
}

func (s *Server) Start() error {
	// 策略要的运行期能力在这里交付（探活的形状见 probe 的说明）。
	s.Filters.BindEnv(serverEnv{s: s})
	// 探过的东西（方言能力、上游毛病）从磁盘装回来。读不出来就说一句：
	// 否则现象是「重启之后每个上游都要重新撞一次 404/400 才学回来」，
	// 而原因没人知道。
	if err := probe.LoadCachedCapabilities(); err != nil {
		s.logf("[probe] %s", i18n.T("capability cache load failed (will re-probe this run): {err}", i18n.A{"err": err}))
	}
	handler := s.rootHandler()

	ln, err := s.listen()
	if err != nil {
		return err
	}
	s.started = time.Now()
	s.srv = &http.Server{Handler: handler}
	s.ln = ln
	s.signalReady() // 交接进来的进程：告诉父进程「socket 已接上」
	s.logf("[proxy] %s", i18n.T("listening on {host}:{port}", i18n.A{"host": s.bindHost(), "port": s.Port}))
	return s.srv.Serve(ln)
}

// listen 拿监听 socket：父进程交接来的 fd 优先（优雅升级路径），否则
// 自己 listen。继承时 fd 号从 NEWGATE_LISTENER_FD 读，读完立刻 unset——
// 环境变量不能传染给这个进程之后 spawn 的任何东西（比如它自己的升级
// 子进程、或懒启动路径的普通 Spawn），否则 fd3 会被当成 socket 用。
func (s *Server) listen() (net.Listener, error) {
	if fdStr := os.Getenv("NEWGATE_LISTENER_FD"); fdStr != "" {
		os.Unsetenv("NEWGATE_LISTENER_FD")
		fd, err := strconv.Atoi(fdStr)
		if err != nil {
			return nil, i18n.Ef(err, "NEWGATE_LISTENER_FD={value} is not an fd number", i18n.A{"value": strconv.Quote(fdStr)})
		}
		f := os.NewFile(uintptr(fd), "inherited-listener")
		ln, err := net.FileListener(f)
		if err != nil {
			return nil, i18n.Ef(err, "inheriting listener fd {fd} failed", i18n.A{"fd": fd})
		}
		if _, ok := ln.(*net.TCPListener); !ok {
			ln.Close()
			return nil, i18n.E("inherited fd {fd} is not a TCP listener", i18n.A{"fd": fd})
		}
		// 端口号以真身为准：继承路径下 s.Port 参数可能只是父进程的复述
		if a, ok := ln.Addr().(*net.TCPAddr); ok {
			s.Port = a.Port
		}
		return ln, nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", s.bindHost(), s.Port))
	if err != nil {
		return nil, i18n.Ef(err, "listening on {host}:{port} failed",
			i18n.A{"host": s.bindHost(), "port": s.Port})
	}
	return ln, nil
}

// bindHost 是这次监听实际用的地址：没配就是回环。
func (s *Server) bindHost() string {
	if s.Host == "" {
		return "127.0.0.1"
	}
	return s.Host
}

// signalReady 优雅交接的另一半握手：进入 accept 循环前往父进程给的
// 管道写一个字节。没有交接（正常 Spawn 启动）时是空操作。
func (s *Server) signalReady() {
	fdStr := os.Getenv("NEWGATE_READY_FD")
	if fdStr == "" {
		return
	}
	os.Unsetenv("NEWGATE_READY_FD")
	if fd, err := strconv.Atoi(fdStr); err == nil {
		if f := os.NewFile(uintptr(fd), "ready-pipe"); f != nil {
			_, _ = f.Write([]byte{1})
			_ = f.Close()
		}
	}
}

func (s *Server) Shutdown() {
	// 有账没落盘的策略在这里被叫一次（顺序在关 listener 之前：关掉之后进程
	// 可能就退了，那笔账就丢了）。落盘失败按「不静默」报出来，但不阻断停机。
	s.Filters.Flush(s.logf)
	// 转发路上学到的上游毛病同理（见 flushPendingQuirks）。
	s.flushPendingQuirks()
	if s.srv != nil {
		_ = s.srv.Close()
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	snap := s.snap()
	if snap == nil {
		s.fail(w, 400, i18n.T("cannot read config — run `newgate doctor`", nil))
		return
	}
	st := snap.State
	tc := thinkcache.Default.Stats()
	doc := map[string]interface{}{
		"ok":              true,
		"default_profile": st.DefaultProfile,
		"active":          st.Active,
		"port":            s.Port,
		"requests":        atomic.LoadUint64(&s.requests),
		"failures":        atomic.LoadUint64(&s.failures),
		"uptime_s":        int(time.Since(s.started).Seconds()),
		"tiers":           domain.Roles,
		// 给 `newgate restart` 探测用：支持优雅交接（socket 移交）。
		// 旧版 daemon 没这个字段——restart 看到缺失就退回 stop+start，
		// 绝不盲发 /__newgate/upgrade（那会被 catch-all 转发到上游）。
		"handoff": true,
		// 推理内容缓存：只报计数，绝不报内容
		"thinkcache": map[string]interface{}{
			"entries":    tc.Entries,
			"bytes":      tc.Bytes,
			"max_bytes":  tc.MaxBytes,
			"hits":       tc.Hits,
			"misses":     tc.Misses,
			"puts":       tc.Puts,
			"evictions":  tc.Evictions,
			"unparsable": tc.Unparsable,
		},
	}
	// 策略自己的状态节并进**顶层**（不是塞进一个 extra 对象）：老版本 CLI 读的
	// 就是顶层那些键，挪窝会让它在优雅交接的升级窗口里看见空表。键撞车在注册期
	// 就报错（见 policy.Registry.validate），所以这里不会互相覆盖。
	for key, value := range s.Filters.Doc() {
		doc[key] = value
	}
	writeJSON(w, 200, doc)
}

type probeHealthObservation struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Status    int    `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
	Context   int    `json:"context_bytes"`
	Error     string `json:"error,omitempty"`
}

// handleHealth 接收 `newgate probe` 的主动探活结论，交给注册进来的策略。
//
// 数据面只负责**收发与鉴权**：它把「探到了什么」翻译成中性的观察值，至于这条
// 结论意味着什么、要不要摘牌，全是策略的事（`policy.Controller.ObserveProbes`）。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, 405, map[string]interface{}{"ok": false, "error": i18n.T("only POST is accepted", nil)})
		return
	}
	snap := s.snap()
	if snap == nil || snap.State.ControlToken == "" ||
		r.Header.Get("Authorization") != "Bearer "+snap.State.ControlToken {
		writeJSON(w, 403, map[string]interface{}{"ok": false, "error": i18n.T("control token mismatch", nil)})
		return
	}
	var req struct {
		Observations []probeHealthObservation `json:"observations"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 64*1024))
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": i18n.T("invalid request JSON", nil)})
		return
	}
	threshold := snap.State.Timeouts.ClassifierFirstByte()
	obs := make([]policy.ProbeObservation, 0, len(req.Observations))
	for _, o := range req.Observations {
		if o.Provider == "" || o.Model == "" {
			continue
		}
		obs = append(obs, policy.ProbeObservation{
			Provider: o.Provider, Model: o.Model, Status: o.Status,
			Latency:      time.Duration(o.LatencyMs) * time.Millisecond,
			ContextBytes: o.Context, Error: o.Error, SlowAfter: threshold,
		})
	}
	opened := 0
	for _, ack := range s.Filters.ObserveProbes(obs) {
		if ack.Opened {
			opened++
		}
		if ack.Note != "" {
			s.logf("%s", ack.Note)
		}
	}
	doc := map[string]interface{}{"ok": true, "opened": opened}
	for key, value := range s.Filters.Doc() {
		doc[key] = value
	}
	writeJSON(w, 200, doc)
}

// handleMetrics 网关计数器（只读，只听 127.0.0.1）。`newgate metrics` 的
// 数据源；计数随 daemon 重启归零。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{
		"uptime_s": int(time.Since(s.started).Seconds()),
		"metrics":  metrics.Default.Snapshot(),
	})
}

// handleControlStop 控制端点：给「读得到共享配置、却发不出信号」的用户
// 停机用（daemon.Stop 在 EPERM 时的兜底路径）。
//
// 鉴权：state.json 里的 ControlToken（Bearer）。端点只听 127.0.0.1，
// 且令牌与上游 key 同文件同级保密——能拿到它的进程早已越过了这层防护。
func (s *Server) handleControlStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, 405, map[string]interface{}{"ok": false, "error": i18n.T("only POST is accepted", nil)})
		return
	}
	tok := ""
	if snap := s.snap(); snap != nil {
		tok = snap.State.ControlToken
	}
	// 常数时间比较：不让本地进程靠响应耗时逐位猜令牌
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if tok == "" || subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
		writeJSON(w, 403, map[string]interface{}{"ok": false, "error": i18n.T("control token mismatch", nil)})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "bye": true})
	s.logf("[proxy] %s", i18n.T("control stop requested by {addr}, exiting", i18n.A{"addr": r.RemoteAddr}))
	// 先让 200 刷出缓冲区，再触发退出
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.RequestStop()
	}()
}

// handleControlUpgrade 优雅升级（nginx upgrade 的 Go 版）：把监听 socket
// 移交给新二进制，新进程接上后旧进程排空在途请求再退。
//
// 为什么要有它：开发 newgate 的会话本身就穿行在代理里，stop/start 的
// 断流窗口会把正在用代理的 Claude Code 直接打断。交接路径下：
//   - 在途请求（含 SSE 长流）由本进程**流完为止**，不掐断；
//   - 新连接立刻由新进程 accept，客户端无感知；
//   - 不摘 shim、不动接管状态——只是换了个进程继续服务。
//
// 鉴权与 /__newgate/stop 同源（ControlToken）。
func (s *Server) handleControlUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, 405, map[string]interface{}{"ok": false, "error": i18n.T("only POST is accepted", nil)})
		return
	}
	tok := ""
	if snap := s.snap(); snap != nil {
		tok = snap.State.ControlToken
	}
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if tok == "" || subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
		writeJSON(w, 403, map[string]interface{}{"ok": false, "error": i18n.T("control token mismatch", nil)})
		return
	}
	if s.ln == nil {
		writeJSON(w, 503, map[string]interface{}{"ok": false, "error": i18n.T("listener handle not ready yet", nil)})
		return
	}
	info, err := daemon.SpawnHandoff(s.Port, s.ln)
	if err != nil {
		// 交棒失败：本进程继续独占 socket，服务没受任何影响
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	// pid/lock 立刻改到新进程名下——排空期间 status 也要指向未来
	if err := daemon.AdoptRuntime(info); err != nil {
		_ = syscall.Kill(info.PID, syscall.SIGKILL)
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	s.logf("[proxy] %s", i18n.T("graceful handoff: socket handed to new process pid {pid}, draining in-flight requests", i18n.A{"pid": info.PID}))
	// 学到的东西**先落盘再交棒**，顺序不能反：swap 之后这个进程只剩「把在途
	// 请求流完」这一件事，而此刻它手里那一笔（换版之前那几个小时里撞出来的上游
	// 毛病）只有它知道——新进程的 Start 已经跑完了，它读盘时看到的是**旧的**
	// 那一份。落盘放后面的话，这一笔要等到「下一个新进程起来」才生效，也就是
	// 「换版后的第一发」仍然撞 400——而那正是用户 2026-09-22 报的那一次。
	// （2026-09-23 实测踩过：先 swap 的版本，B 进程第一发仍是 400。）
	s.flushPendingQuirks()
	writeJSON(w, 200, map[string]interface{}{"ok": true, "new_pid": info.PID})
	// 排空：等在途请求（含 SSE 流）自然结束，上限 10 分钟，然后退。
	// 两个关键点：
	//   - 用 Shutdown（等在途），绝不用 srv.Close()（硬掐）；
	//   - 主循环会在 Serve 返回处等 Drained()——否则 listener 一关主函数
	//     就 return，进程当场消失，这里等再久也白等。
	// 退出一律 os.Exit：跳过 Serve 里 defer 的 RemoveLock（pid/lock 已是
	// 新进程的，不能删）。
	go func() {
		atomic.StoreInt32(&s.drainFlag, 1)
		time.Sleep(100 * time.Millisecond) // 让 200 先刷出去
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if s.srv != nil {
			_ = s.srv.Shutdown(ctx)
		}
		s.drainOnce.Do(func() { close(s.drainCh) })
		// 排空完了才落盘：排空期里在途请求可能又学到新的毛病，而那几发正是
		// 交接前后最可能撞上的（见 flushPendingQuirks 的说明——换版路径上
		// 只有这一处会叫它）。
		s.flushPendingQuirks()
		s.logf("[proxy] %s", i18n.T("drain complete, exiting (socket continues under pid {pid})", i18n.A{"pid": info.PID}))
		os.Exit(0)
	}()
}

// handleModels 让 opencode 能发现我们暴露的语义档位。
//
// 模块贡献的动态角色键（omo-sisyphus / cat-deep）也列出来：它们同样是客户端
// 可以点名的 model id，藏着不列只会让 opencode 报「模型不存在」。
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	var data []map[string]interface{}
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] {
			return // 内置别名（normal→mid）也在角色表里，会与档位重名
		}
		seen[id] = true
		data = append(data, map[string]interface{}{
			"id": id, "object": "model", "owned_by": "newgate",
		})
	}
	// 客户端模块可以替自己的 URL 答这份清单（special.ModelLister 的注释说了为什么）。
	// 没人答就是缺省：档位 + 动态角色键，与客户端无关。
	tgt := parseTarget(r.URL.Path)
	var st *domain.State
	if snap := s.snap(); snap != nil {
		st = snap.State
	}
	if ids, _, ok := special.ListModels(tgt.TaskCreate, tgt.Profile, st); ok {
		for _, id := range ids {
			add(id)
		}
		writeJSON(w, 200, map[string]interface{}{"object": "list", "data": data})
		return
	}
	for _, tier := range domain.Roles {
		add(tier)
	}
	for _, extra := range domain.ExtraRoles() {
		add(extra.Key)
	}
	writeJSON(w, 200, map[string]interface{}{"object": "list", "data": data})
}

// Target 从 URL 路径里解析出的路由意图。
//
// 路径文法：
//
//	/a/<agent>[/p/<profile>]/v1/...   带 agent 身份
//	/v1/...                           兼容：用全局默认 profile
//
// 为什么用路径而不是 header：claude / opencode 都不允许我们往它们的请求里
// 塞自定义 header，但 baseURL 是 bootstrap 时我们自己写进去的。路径方案
// 完全无状态——不需要注册会话、不需要令牌生命周期，代理重启也不受影响。
type Target struct {
	TaskCreate string // 空 = 未指定
	Profile    string // 非空 = 本次调用显式覆盖，不改全局状态
	Suffix     string // 转发给上游的路径后缀，形如 /chat/completions
}

func parseTarget(p string) Target {
	var t Target
	for {
		switch {
		case strings.HasPrefix(p, "/a/"):
			rest := p[3:]
			if i := strings.IndexByte(rest, '/'); i < 0 {
				t.TaskCreate, p = rest, ""
			} else {
				t.TaskCreate, p = rest[:i], rest[i:]
			}
		case strings.HasPrefix(p, "/p/"):
			rest := p[3:]
			if i := strings.IndexByte(rest, '/'); i < 0 {
				t.Profile, p = rest, ""
			} else {
				t.Profile, p = rest[:i], rest[i:]
			}
		default:
			t.Suffix = strings.TrimPrefix(p, "/v1")
			return t
		}
	}
}

// pinnedProfiles 把 profile 集合收敛到被点名的那一份（没点名 = 原样返回）。
//
// 点名（URL 里的 `/p/<name>`）的语义是「这次就用它」。那件事原先只在解析之后
// 靠切片截断（steps[:1]）兑现，而它有一个缝：**被点名的 profile 一个可用候选
// 都没有**时（唯一 binding 被熔断、被禁用），链头位置站的已经是别人的
// profile，截断留下的恰是「静默换到别人」——点名最想禁止的东西。2026-11-04
// 现场：ds 的唯一条 binding 熔断，`--profile=ds` 的请求要么 404（反查缺陷），
// 要么在反查修好之后悄悄跑到别的 profile 上。
//
// 所以收敛放在**建链之前**：链里根本没有第二份 profile 的候选，后面的截断
// 只负责「同一份 profile 内部也不换人」。被点名的那份不存在时返回空集合——
// 由调用方报「这份 profile 没有可用候选」，而不是拿全局链顶上。
func pinnedProfiles(profiles []*domain.Profile, name string) []*domain.Profile {
	for _, p := range profiles {
		if p.Name == name {
			// 新切片：快照里的那份是共享的，不能因为调用方 append 而被改到。
			return []*domain.Profile{p}
		}
	}
	return nil
}

// testChain 仅供测试注入，绕过磁盘配置与真实上游。生产路径永远是 nil。
// 有它才能保证单测不出网——docs/10-testing-security.md「测试不出网，出网即失败」。
var testChain func(tier string) []resolve.Step

// handleCountTokens 本地应答 count_tokens（粗估兜底）。
//
// Claude Code 周期性地问 token 数（上下文水位条、自动压缩阈值都靠它），
// 但不是所有上游都有这个端点——聚合器实测（2026-09，api.rvcompute.com）
// /messages 200 而 /messages/count_tokens 404。所以先试转发拿真值
// （forwardCountTokens，按 provider 学），接不住再走到这里：按请求体
// 字节数 / 4 粗估（英文 ≈ 4 字符/token；中文 UTF-8 ≈ 3 字节/字、
// 1 字/token，估出来偏大——对水位条来说宁可早压缩，无害）。
//
// 逐轮的真实计数走 /messages 响应里的 usage，不经过这里。
func (s *Server) handleCountTokens(w http.ResponseWriter, reqID uint64, body []byte) {
	n := len(body) / 4
	s.logf("[proxy] %s", i18n.T("#{req} count_tokens ({bytes} bytes) -> local rough estimate {n} tokens", i18n.A{"req": reqID, "bytes": len(body), "n": n}))
	writeJSON(w, 200, map[string]interface{}{"input_tokens": n})
}

// forwardCountTokens 把 count_tokens 转发给上游拿真值。接住了返回 true。
//
// 为什么值得：本地只有粗估，上游的 tokenizer 才是真值——水位条和自动
// 压缩阈值都靠它。但它是 anthropic 方言的私有端点，openai 方言上游和
// 很多聚合器没有，所以按 (provider, model) 学，gate 层面 lazy probe：
//
//	没探过 → 试发一发（这本身就是 probe）；404/405 = 明确没有，记下，
//	          此后退回本地粗估不再白跑；2xx = 有，记下，此后一直拿真值
//	连接失败 / 429 / 401 → 不学（「现在不行」≠「没有」），本次退回本地
//
// 模型注入：count_tokens 请求不带 model 字段，补**主力档**链头——
// Claude Code 的主循环跑在 opus 槽（四档化后 opus 槽 = normal 档，
// 2026-09-16；之前是 heavy），数出来的才是将要处理这段对话的 tokenizer。不走链、
// 不碰熔断器：数 token 失败不算上游病。
func (s *Server) forwardCountTokens(w http.ResponseWriter, r *http.Request,
	body []byte, tgt Target, reqID uint64) bool {

	head, ok := s.mainLoopHead(tgt)
	if !ok {
		return false
	}
	if ctOK, known := dialect.Supports(head.Binding.Provider, head.Binding.Model, dialect.CapCountTokens); known && !ctOK {
		return false // 探过了：这个上游没有 count_tokens
	}

	// 补 model 字段（请求通常不带）；带了就尊重客户端的
	var out []byte
	if m, has := rewrite.TopLevelString(body, "model"); has && m != "" {
		out = body
	} else {
		nb, err := rewrite.InsertTopLevelRaw(body, "model", []byte(strconv.Quote(head.Binding.Model)))
		if err != nil {
			return false
		}
		out = nb
	}

	target := head.Provider.URL("/messages/count_tokens")
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(out))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	setAuth(req.Header, head.Provider)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		if r.Context().Err() == nil {
			s.logf("[proxy] %s", i18n.T("#{req} count_tokens forward failed ({err}); falling back to local rough estimate", i18n.A{"req": reqID, "err": err}))
		}
		return false
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 404 || resp.StatusCode == 405:
		if dialect.MarkUnsupported(head.Binding.Provider, head.Binding.Model, dialect.CapCountTokens) {
			s.logf("[proxy] %s", i18n.T(
				"#{req} learned {name} has no count_tokens endpoint (upstream {code}); falling back to local rough estimate, no retry",
				i18n.A{"req": reqID, "name": head.Binding, "code": resp.StatusCode}))
		}
		metrics.Default.Inc("count_tokens.probe_404")
		return false
	case resp.StatusCode >= 400:
		s.logf("[proxy] %s", i18n.T("#{req} count_tokens upstream {code}; falling back to local rough estimate", i18n.A{"req": reqID, "code": resp.StatusCode}))
		return false
	}
	dialect.Mark(head.Binding.Provider, head.Binding.Model, dialect.CapCountTokens)
	metrics.Default.Inc("count_tokens.forwarded")

	rb, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Newgate-Route", "count_tokens -> "+head.Binding.String())
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(rb)
	s.logf("[proxy] %s", i18n.T("#{req} count_tokens -> {name} upstream true value: {value}", i18n.A{"req": reqID, "name": head.Binding, "value": trim(string(rb))}))
	return true
}

// mainLoopHead 主循环档（normal）的链头（含完整 provider 记录）。
// count_tokens 不带 model，转发时按它补。用 PrimaryBinding（忽略熔断器）：
// 数 token 用配置里排第一的就行，不值得为它触发 fallback 语义。
func (s *Server) mainLoopHead(tgt Target) (resolve.Step, bool) {
	if testChain != nil {
		if steps := testChain("normal"); len(steps) > 0 {
			return steps[0], true
		}
		return resolve.Step{}, false
	}
	snap := s.snap()
	if snap == nil {
		return resolve.Step{}, false
	}
	active := tgt.Profile
	if active == "" {
		active = snap.State.ActiveFor(tgt.TaskCreate)
	}
	profiles := snap.Profiles
	if tgt.Profile != "" {
		profiles = pinnedProfiles(snap.Profiles, tgt.Profile)
	}
	b, ok := resolve.PrimaryBinding("normal", profiles, snap.Providers, active)
	if !ok {
		return resolve.Step{}, false
	}
	p, ok := snap.Providers.Providers[b.Provider]
	if !ok {
		return resolve.Step{}, false
	}
	return resolve.Step{Profile: active, Binding: b, Provider: p}, true
}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	atomic.AddUint64(&s.requests, 1)
	metrics.Default.Inc("requests.total")

	// /api/hello：Claude Code 的连通性探针（HEAD/GET，无 body）。探的是
	// 「API 基地址活着吗」——我们就是它的 API，本地应 200；掉进下面的
	// model 解析只会刷一串 400 日志（2026-09-09 实抓）。
	if strings.HasSuffix(r.URL.Path, "/api/hello") {
		w.WriteHeader(200)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		s.fail(w, 400, i18n.Ef(err, "cannot read request body: {err}", nil).Error())
		return
	}
	_ = r.Body.Close()

	tgt := parseTarget(r.URL.Path)

	// 带身份前缀的 `/a/<agent>[/p/<profile>]/v1/models` 与裸的 `/v1/models` 是
	// 同一件事：列出可点名的档位。mux 只把裸路径挂给了 handleModels，带前缀的
	// 会掉到这里、因为没有 model 字段被 400。2026-10-29 opencode 的 baseURL 补上
	// `/a/opencode`（之前漏了，网关认不出它、按全局缺省选链）之后，它的 /models
	// 正好走这条路。列表与客户端无关，所以直接交给同一个处理函数。
	if tgt.Suffix == "/models" && r.Method == http.MethodGet {
		s.handleModels(w, r)
		return
	}

	// 没有 body 的 GET / HEAD 不是模型调用：本地答 404，**不记成失败**。
	//
	// 2026-11-04 接 hermes 时实抓：它对本机地址的 base_url 会探一圈「这是哪种本地
	// 推理服务」（`/api/v1/models`、`/api/tags`、`/v1/props`、`/props`、`/version`，
	// 见 hermes 的 model_metadata.detect_local_server_type），每建一个会话一轮。这些
	// 以前掉进下面的 model 解析，每一发记一条「错误 400：请求体顶层没有 model」、
	// 失败计数 +1——status 里的 err 数于是跟着 hermes 的会话数涨，看起来像数据面在
	// 出错。它们问的是「你有没有这个端点」，诚实的回答就是 404：探测方据此判断
	// 「不是 ollama / lm-studio」，与它对一个普通 OpenAI 兼容服务的预期一致。
	//
	// 只认「GET/HEAD 且 body 为空」：带 body 的请求仍走下面那条路——缺 model 的
	// POST 是真的形状不对，那一条 400 + 计数是该有的。
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && len(body) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{
			"error": map[string]interface{}{
				"type":    "not_found",
				"message": "[newgate] " + i18n.T("{method} {path} is not an endpoint newgate serves", i18n.A{"method": r.Method, "path": r.URL.Path}),
			},
		})
		return
	}

	// count_tokens：Anthropic 协议的私有端点，Claude Code 拿它算上下文水位。
	// 必须在 model 检查**之前**拦——有客户端发这个请求时不带 model 字段。
	// 上游有这个端点就转发拿真值（按 provider 学，见 forwardCountTokens），
	// 没有（聚合器 404）退回本地粗估。
	if strings.HasSuffix(r.URL.Path, "/count_tokens") {
		reqID := atomic.LoadUint64(&s.requests)
		metrics.Default.Inc("count_tokens.total")
		if s.forwardCountTokens(w, r, body, tgt, reqID) {
			return
		}
		metrics.Default.Inc("count_tokens.local")
		s.handleCountTokens(w, reqID, body)
		return
	}

	// 不做任何 JSON 往返。只在原始字节里读出 model，后面也只替换那一段。
	inModel, ok := rewrite.TopLevelString(body, "model")
	if !ok || inModel == "" {
		// 带上方法与路径：这一类「形状不认识」的请求，没有路径就没法排查
		// 是哪个客户端、哪个端点发来的。
		s.fail(w, 400, i18n.T("request body has no top-level model string field ({method} {path})",
			i18n.A{"method": r.Method, "path": r.URL.Path}))
		return
	}
	norm := protocol.NormalizeRole(inModel)
	snap := s.snap()
	if snap == nil {
		s.fail(w, 400, i18n.T("cannot read config — run `newgate doctor`", nil))
		return
	}
	st := snap.State
	stream0 := rewrite.TopLevelBool(body, "stream")
	reqID := atomic.LoadUint64(&s.requests)
	// 进来就记——否则「请求没到」和「到了在等上游」在日志里长得一样。
	// req= 是客户端请求体字节数：跟上游日志对账（300k compact 这种大输入）时
	// 靠它定位「同一发请求」，没有它就只剩 reqID 一个数，跨系统对不上。
	s.logf("[proxy] %s", i18n.T("#{req} <- {model} stream={stream} req={bytes} bytes  start", i18n.A{"req": reqID, "model": inModel, "stream": stream0, "bytes": len(body)}))
	if gatewaystate.DebugActive(st) {
		s.logf("[proxy] %s", i18n.T(
			"#{req} client request {method} {path}{headers}\n    body({bytes} bytes): {body}",
			i18n.A{"req": reqID, "method": r.Method, "path": r.URL.Path,
				"headers": headerDump(r.Header), "bytes": len(body),
				"body": truncate(string(redact(body)), 4000)}))
	}

	// ---- per-agent 路由 + fallback 链 ----
	// 每个 agent 有自己的链头；URL 里的 /p/ 是本次调用的覆盖
	active := tgt.Profile
	if active == "" {
		active = st.ActiveFor(tgt.TaskCreate)
	}
	// 点名（/p/<name>）在建链前就把 profile 集合收敛到那一份，见 pinnedProfiles。
	profiles := snap.Profiles
	if tgt.Profile != "" {
		profiles = pinnedProfiles(snap.Profiles, tgt.Profile)
	}
	suffix := tgt.Suffix
	stream := stream0

	var steps []resolve.Step
	var skips []resolve.Skip
	tier := norm
	toolOrigin, hasToolOrigin := thinkcache.Default.ContinuationOrigin(body)
	// special 路由插件在常规解析前贡献结构化决策。热路径不知道具体插件名，
	// 也不解释它为什么改道；档位、覆盖链头、超时和可观测性都由插件声明。
	req := &special.Request{InModel: inModel, Tier: norm, Stream: stream0, Agent: tgt.TaskCreate}
	// special 短路器（special.Responder）：有的插件能**直接替上游回答**，一个
	// 字节都不发——比如裸奔（modules/claudecode/classifier-naked.go）把 Bash
	// 分类器短路成批准。先于构链问：短路成功就没有「链」这回事了。热路径不
	// 认识具体插件，只负责执行判决、留痕。
	if resp, pluginName, short := special.Respond(body, req, st); short {
		s.writeShortCircuit(w, r, reqID, pluginName, resp,
			special.RespondNote(pluginName, st), active)
		return
	}

	route, routed := special.Route(body, req, st)
	routeTier := ""
	if routed {
		routeTier = route.Tier
	}
	if routeTier != "" {
		opts := resolve.Opts{
			Active:    active,
			Available: s.Filters.Admit,
			Rank:      func(provider, model string) int { return s.Filters.Rank(provider, model, len(body)) },
			MaxSteps:  st.Chain.Attempts(),
		}
		var rs []resolve.Step
		applied := false
		switch {
		case testChain != nil:
			rs = testChain(routeTier)
		case route.Head != nil:
			rs, skips, applied = resolve.OverrideChain("special:"+route.Plugin,
				*route.Head, routeTier,
				profiles, snap.Providers, opts)
			if !applied && route.OverrideFailNote != "" {
				s.logf("[proxy] #%d special_treatment %s: %s",
					reqID, route.Plugin, route.OverrideFailNote)
			}
		default:
			rs, skips = resolve.BuildChain(routeTier, profiles, snap.Providers, opts)
		}
		if len(rs) > 0 {
			steps, tier = rs, routeTier
			note := route.Note
			if applied && route.OverrideNote != "" {
				note = route.OverrideNote
			}
			if note != "" {
				s.logf("[proxy] #%d special_treatment %s: %s",
					reqID, route.Plugin, note)
			}
			if key := route.MetricKey(); key != "" {
				metrics.Default.Inc(key)
			}
		} else {
			// 插件要求的链为空时回落到常规解析：路由插件不能切断请求。
			routeTier = ""
			routed = false
		}
	}
	if routeTier == "" {
		if testChain != nil {
			steps = testChain(norm)
		} else {
			// 档位名走档位链；具体模型名反解回它所属档位，并把点名的模型放最前
			// （docs/04-configuration.md）——这支持「工具界面显示真实模型名」。
			steps, skips, tier = resolve.ResolveRequest(norm, active, profiles, snap.Providers, resolve.Opts{
				Active:    active,
				Available: s.Filters.Admit,
				Rank:      func(provider, model string) int { return s.Filters.Rank(provider, model, len(body)) },
				MaxSteps:  st.Chain.Attempts(),
			})
		}
	}
	// 短路：这次调用只走链头，不许换人（2026-09-28）。
	//
	// 两个来由，合在同一处截断——它们要的**结果**完全一样（只发第一步、不
	// 换人、不等超时预算），分成两处只会让下面那三条推论要证明两遍：
	//
	//   - `--profile=xx`（tgt.Profile 非空）：用户点名了一个 profile，意思是
	//     「就用这个」。此前它只把 profile 钉成链头，链尾照样按 profile 优先级
	//     接上别人——用户点名的那个挂了就**静默换到别的 profile**，配置、界面
	//     显示的真名、实际跑的上游三者对不上。判据零新增状态：launch 只在
	//     `--profile=xx` 时把 `/p/<name>` 写进 base URL，parseTarget 读出来
	//     就是它，所以「有 /p/ 段」⟺「用户点了名」。
	//
	//     2026-11-04 起 profile 集合在建链前就已经收敛到被点名的那一份
	//     （见 pinnedProfiles），所以这里的截断只管「同一份 profile 内部也不
	//     换人」。两者缺一不可：只收敛不截断 = 点名 profile 内部的 fallback
	//     仍会换模型；只截断不收敛 = 点名 profile 全灭时截到的是别人的链头。
	//
	//   - `newgate fallback off`（全局开关）：全局关掉所有 fallback 链。
	//
	// 位置在这一处不是随意的，三条：
	//
	//  1. **在 882-942 那段路由/取链之后**：`testChain != nil` 的两个分支也
	//     在这段里，放前面会被测试注入的链绕过——那些测试就测不到这条了。
	//     截断放在**唯一**的出口，所有来路（路由链、覆盖链、档位链、注入链）
	//     都必然经过它。
	//  2. **在下面 deadline 那条之前**：budget 那条判断（`i > 0`）只在链上
	//     还有下一步时才成立，链只剩一步时 i 恒为 0——于是「无视所有 timeout」
	//     不是另写一条分支，而是**推出来的**。`st.Chain.Budget()` 与
	//     `st.Timeouts` 一个字都不用动（后者是每次尝试的传输超时，本来就该
	//     照常生效，见下面 `tr.ResponseHeaderTimeout`）。
	//  3. **绝不往 st 里写**：`st := snap.State`（841）是指向 watcher 那份共享
	//     快照的**指针**。在这里 `st.Chain.MaxAttempts = 1` 会改掉所有并发请求、
	//     所有 agent 的活配置——数据竞争 + 跨请求污染。截断只发生在本地切片上。
	//
	// 还要说明为什么不能用 `Opts.MaxSteps = 1` 代替：那条路在
	// `OverrideChain`（路由插件给了覆盖绑定时）下会先按 1 截断 tier 链、再把
	// 覆盖步**插到最前**，结果是 2 步、fallback 照打。切片截断没有这个缝。
	//
	// 末尾必须留痕（不静默）：被砍掉的那几步连同原因写进日志，用户能回答
	// 「为什么这次没换人」。
	//
	// **`why` 为空就是没砍**：这个变量不是排版用的中间量，它是「砍不砍」的
	// 唯一判据。写成 `switch { case …: log }` 再把 `steps = steps[:1]` 摆在
	// switch 外面，读起来像是「截断」，实际是**每一发多站请求都只走链头**——
	// 2026-09-28 就是这么写错了一版：下面三条「没换人」的用例照样全绿（它们
	// 要的结果正是截断），只有 TestFallbackOnByDefaultStillSwaps 那条对照红
	// 了。它存在的理由就是这个：三个绿不能证明开关接上了，只能证明截断发生了。
	if len(steps) > 1 {
		var why string
		switch {
		case tgt.Profile != "":
			why = i18n.N(
				"#{req} profile {profile} is pinned, dropping {n} fallback step: {dropped}",
				"#{req} profile {profile} is pinned, dropping {n} fallback steps: {dropped}",
				len(steps)-1, i18n.A{"req": reqID, "profile": tgt.Profile, "n": len(steps) - 1,
					"dropped": formatSteps(steps[1:])})
		case gatewaystate.FallbackOff(st):
			why = i18n.N(
				"#{req} fallback is off, dropping {n} chain step: {dropped}",
				"#{req} fallback is off, dropping {n} chain steps: {dropped}",
				len(steps)-1, i18n.A{"req": reqID, "n": len(steps) - 1,
					"dropped": formatSteps(steps[1:])})
		}
		if why != "" {
			s.logf("[proxy] %s", why)
			steps = steps[:1]
		}
	}
	if len(steps) == 0 {
		s.logf("[proxy] %s", i18n.T("#{req} no usable candidate; skipped: {skips}", i18n.A{"req": reqID, "skips": fmtSkips(skips)}))
		if tier == "" {
			s.fail(w, 404, i18n.T(
				"model {model} is neither a known tier ({tiers}) nor bound in any profile; run `newgate tier` for available bindings",
				i18n.A{"model": norm, "tiers": strings.Join(domain.Roles, ", ")}))
		} else if norm != tier {
			// 名字认出来了（它绑在某个档位上），但那条档位链此刻一个可用候选
			// 都没有。旧代码在这里说「不在任何 profile 的绑定里」——2026-11-04
			// 现场：唯一绑定了 deepseek-flash 的 ds 被熔断，用户看到这句话后去
			// 查配置，配置是对的，真正的原因（circuit open）在上一行的跳过原因里。
			s.fail(w, 404, i18n.T(
				"model {model} is bound to tier {tier}, but that tier has no usable candidate under profile {profile}; run `newgate tier {tier}` to see why each candidate was skipped",
				i18n.A{"model": norm, "tier": tier, "profile": active}))
		} else {
			s.fail(w, 404, i18n.T(
				"tier {tier} has no usable candidate under profile {profile} (tiers: {tiers}); run `newgate tier {tier}` to see why each candidate was skipped",
				i18n.A{"tier": tier, "profile": active, "tiers": strings.Join(domain.Roles, ", ")}))
		}
		return
	}
	deadline := start.Add(time.Duration(st.Chain.Budget()) * time.Millisecond)

	var lastMsg string
	var lastCode int
	var trail []string // 给 X-Newgate-Chain
	for i, a := range steps {
		isLast := i == len(steps)-1
		// 这一发的响应改写者（见下面 special.ClaimEgress）。每一站都重置：
		// 换了一个上游就是另一份请求形状，上一站认下的改写不适用这一站。
		var egress special.Egress
		if i > 0 && time.Now().After(deadline) {
			metrics.Default.Inc("chain.budget_exhausted")
			s.logf("[proxy] %s", i18n.T("#{req} chain budget {budget} ms exhausted, stopping at step {step}",
				i18n.A{"req": reqID, "budget": st.Chain.Budget(), "step": i}))
			trail = append(trail, "budget-exhausted")
			isLast = true
		}
		// 纯字节手术：只替换顶层 model 的值，其余每个字节原样保留
		newBody, merr := rewrite.ReplaceTopLevelString(body, "model", a.Binding.Model)
		if merr != nil {
			s.fail(w, 500, i18n.Ef(merr, "rewriting model failed: {err}", nil).Error())
			return
		}
		if hasToolOrigin {
			candidate := &special.Request{
				InModel: inModel, Tier: tier, Model: a.Binding.Model,
				Provider: a.Binding.Provider, BaseURL: a.Provider.Base(suffix),
				Protocol: a.Provider.Protocol, Path: suffix, Stream: stream,
				Agent: tgt.TaskCreate, State: st, Quirks: quirk.Default,
			}
			if res := special.RebaseToolLoop(newBody, toolOrigin.Provider, toolOrigin.Model,
				candidate, pluginOff(st)); len(res.Notes) > 0 {
				for _, note := range res.Notes {
					s.logf("[proxy] #%d special_treatment %s", reqID, note)
				}
				if res.Changed {
					newBody = res.Body
				}
				for _, event := range res.Events {
					if key := event.MetricKey(); key != "" {
						metrics.Default.Inc(key)
					}
				}
			}
		}

		// tool schema 修补：只在真有东西要补时才重写 tools 这一个值，
		// messages / system / cache_control 仍然逐字节不动。
		if gatewaystate.RepairEnabled(st) {
			if toolsRaw, ok := rewrite.TopLevelRaw(newBody, "tools"); ok {
				repaired, changes, rerr := schema.Repair(toolsRaw)
				switch {
				case rerr != nil:
					s.logf("[proxy] %s", i18n.T("#{req} tools repair skipped (parse failed, sending as-is): {err}", i18n.A{"req": reqID, "err": rerr}))
				case len(changes) > 0:
					if nb, serr := rewrite.ReplaceTopLevelRaw(newBody, "tools", repaired); serr == nil {
						newBody = nb
						s.logf("[proxy] %s", i18n.N(
							"#{req} set \"required\": [] on {n} tool to pass strict validators (semantically a no-op): {changes}",
							"#{req} set \"required\": [] on {n} tools to pass strict validators (semantically a no-op): {changes}",
							len(changes), i18n.A{"req": reqID, "changes": changes}))
					} else {
						s.logf("[proxy] %s", i18n.T("#{req} tools write-back failed, sending as-is: {err}", i18n.A{"req": reqID, "err": serr}))
					}
				}
			}
		}

		// special_treatment：每家上游的怪癖补丁（gateway/special）。
		// 与 schema 修补的分工——那边是所有严格校验器都需要的通用修补，
		// 这边是「只有某家上游才需要」的，由插件自己 Match 认领。
		// （分类器改走 light 链的路由决策不在这——见上面 special.Route。）
		if gatewaystate.SpecialEnabled(st) {
			patched := &special.Request{
				InModel:  inModel,
				Tier:     tier,
				Model:    a.Binding.Model,
				Provider: a.Binding.Provider,
				// 按这次请求实际会去的 base 报——两种方言分家的上游
				// （provider.anthropic_url）要让插件看到真实那一个。
				BaseURL:  a.Provider.Base(suffix),
				Protocol: a.Provider.Protocol,
				Path:     suffix,
				Stream:   stream,
				Agent:    tgt.TaskCreate,
				State:    st,
				Quirks:   quirk.Default,
			}
			res := special.Apply(newBody, patched, pluginOff(st))
			counted := map[string]bool{}
			for _, n := range res.Notes {
				s.logf("[proxy] #%d special_treatment %s", reqID, n)
			}
			for _, event := range res.Events {
				key := event.MetricKey()
				if key != "" && !counted[key] {
					counted[key] = true
					metrics.Default.Inc(key)
				}
			}
			if res.Changed {
				newBody = res.Body
			}
			// 响应侧：问一句这一发的响应要不要改（比如请求侧把 custom 工具降级成了
			// function，响应侧就得把那几个 function_call 改回 custom_tool_call）。
			// 问在**请求改完之后**：认领者要拿刚 Apply 过的结果决定改哪几条，所以
			// 它看到的必须是最终的那份请求。原文 `body` 一起交过去——响应侧要改
			// 哪几条，判据往往只在客户端发来的那一份里（见 special.Egressor）。
			egress = special.ClaimEgress(patched, body, pluginOff(st))
		}

		target := a.Provider.URL(suffix)
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		// 挂上客户端的 context：opencode 里按 ESC 取消时，上游请求也立刻中断，
		// 而不是让它跑完整个响应照样计费
		req, rerr := http.NewRequestWithContext(r.Context(), r.Method, target,
			bytes.NewReader(newBody))
		if rerr != nil {
			s.fail(w, 500, rerr.Error())
			return
		}
		s.dump(reqID, i, body, newBody)
		if gatewaystate.DebugActive(st) {
			s.logf("[proxy] %s", i18n.T(
				"#{req} sending upstream {target}\n    body({bytes} bytes, delta vs original {delta}): {body}",
				i18n.A{"req": reqID, "target": target, "bytes": len(newBody),
					"delta": len(newBody) - len(body),
					"body":  truncate(string(redact(newBody)), 4000)}))
		}
		copyHeaders(req.Header, r.Header)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Del("Content-Length")
		setAuth(req.Header, a.Provider)

		// 流式不能设总超时（长响应会被砍断），但必须限制首字节等待时间，
		// 否则上游装死就永久挂住。ResponseHeaderTimeout 正好只管到响应头。
		// 紧上限只给**分类器**（它挡在交互通路上，挂住 = 冻住会话，靠
		// system marker 精确认出）；其他请求——包括 /compact 这种 500KB+
		// 的非流式大输入——一律标准 150s：大 prefill 合法地慢，一刀切
		// 紧超时只会在链上连环掐死（2026-09-09 实抓教训）。全部从
		// state.json 的 timeouts 热加载，误杀率看 newgate metrics。
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.ResponseHeaderTimeout = st.Timeouts.FirstByte()
		if routed && route.FirstByteTimeout > 0 {
			tr.ResponseHeaderTimeout = route.FirstByteTimeout
		}
		client := &http.Client{Transport: tr, Timeout: func() time.Duration {
			if stream {
				return 0
			}
			return st.Timeouts.Total()
		}()}

		attemptStart := time.Now()
		resp, derr := client.Do(req)
		attemptTTFT := time.Since(attemptStart)
		// 路由串按**实际发出的** body model 报——special 插件可能已把
		// mid 切成 light（claude-bg 的分类器切档），拿链步的模型打日志
		// 会把人引去查错方向（2026-09-09：日志说 glm-5.3，dump 说 air）。
		sentModel := a.Binding.Model
		if m, has := rewrite.TopLevelString(newBody, "model"); has && m != "" {
			sentModel = m
		}
		routeStr := fmt.Sprintf("%s -> %s/%s", inModel, a.Binding.Provider, sentModel)

		if derr != nil {
			// 客户端自己走了（按 ESC、关窗口、客户端侧超时）不是上游的错。
			//
			// 不加这一闸的后果实测过：一次取消会被当成 smt-claude 连接失败 →
			// 记一次失败 → 沿链走到 smt-deepseek，用的还是那个已经死掉的
			// context，于是**每个候选都瞬间失败**，一次 ESC 就把整条链上所有
			// provider 的熔断器全打开（日志里三个 provider 一起「暂时摘掉」）。
			// 之后真正的请求反而没候选可用，回 502——用户看到的是「取消一下
			// 之后全挂了」，根本查不到源头。
			//
			// 所以：不记失败、不开熔断、不往下走链、也不写 502（对面已经没人了）。
			if cerr := r.Context().Err(); cerr != nil {
				metrics.Default.Inc("client.cancel")
				s.logf("[proxy] %s", i18n.T("#{req} {route} client cancelled during connect ({err}); stopping the whole chain",
					i18n.A{"req": reqID, "route": routeStr, "err": cerr}))
				return
			}
			if strings.Contains(derr.Error(), "timeout awaiting response headers") {
				if stream {
					metrics.Default.Inc("timeout.first_byte.stream")
				} else {
					metrics.Default.Inc("timeout.first_byte.non_stream")
				}
			}
			o := policy.Outcome{
				Provider: a.Binding.Provider, Model: a.Binding.Model,
				Binding: a.Binding.String(), Kind: policy.ConnectionFailed,
				IsLast: isLast, RequestBytes: len(body),
			}
			v := s.outcome(o)
			if v.Attribute {
				atomic.AddUint64(&s.failures, 1)
			}
			hint := ""
			if strings.Contains(derr.Error(), "timeout awaiting response headers") {
				waitLimit := st.Timeouts.FirstByte()
				if routed && route.FirstByteTimeout > 0 {
					waitLimit = route.FirstByteTimeout
				}
				hint = i18n.T("  (first byte exceeded {limit} — upstream stalling or queued)", i18n.A{"limit": waitLimit})
			}
			s.logf("[proxy] %s", i18n.T("#{req} {route} connect failed: {err}{note}{hint}", i18n.A{"req": reqID, "route": routeStr, "err": derr, "note": v.Note, "hint": hint}))
			lastMsg, lastCode = i18n.T("upstream {provider} connect failed: {err}", i18n.A{"provider": a.Binding.Provider, "err": derr}), 502
			trail = append(trail, fmt.Sprintf("%s(conn)", a.Binding))
			if advance(o, v) {
				metrics.Default.Inc("chain.step_failed")
				s.logf("[proxy] %s", i18n.T("advancing to next chain step: {step}", i18n.A{"step": steps[i+1]}))
				continue
			}
			s.fail(w, 502, lastMsg)
			return
		}

		// 定案：把这个响应交给客户端
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			// 上游说了话。先把错误原文整个读出来当证据（它是判据的输入，也是
			// 落盘的现场），再**一次性**把「沿不沿链走 / 记不记账 / 留什么痕」
			// 交给注册进来的策略。
			//
			// 2026-09-17 之前这里是两个分支各判一次：可转移的那条无条件记账，
			// 定案的那条先豁免形状错误。判据不一致的后果是同一发 reasoning
			// 回传 400 在链中间会摘牌、在链尾不会——链中间那发会把还能用的
			// deepseek 摘掉。现在只有一处判据，且它是纯函数，能被穷举测完。
			eb, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 256*1024))
			_ = resp.Body.Close()
			o := policy.Outcome{
				Provider: a.Binding.Provider, Model: a.Binding.Model,
				Binding: a.Binding.String(), Kind: policy.RejectedStatus,
				Status: resp.StatusCode, Body: eb,
				IsLast: isLast, FallbackOn400: st.Chain.FallbackOn400,
				RequestBytes: len(body),
			}
			v := s.outcome(o)
			if v.Attribute {
				atomic.AddUint64(&s.failures, 1)
			}
			s.learnQuirks(reqID, a.Binding.Provider, a.Binding.Model, resp.StatusCode, eb)
			if advance(o, v) {
				s.logf("[proxy] %s", i18n.T("{route} -> {status}{note}  upstream said: {body}",
					i18n.A{"route": routeStr, "status": resp.StatusCode, "note": v.Note,
						"body": trim(string(eb))}))
				// 沿链转移这一发也要留下认领证据。形状 400 换个校验更松的上游
				// 是唯一可能成功的路，所以「裁决为继续走」是它最常见的收场；
				// 不留痕就等于客户端最后拿到 200、日志里却看不出中间那家拒过
				// （2026-09-17 那次静默降级正是这个形态）。
				//
				// 这里**刻意不落**通用 err-* 证据（整包 body dump 会让每次
				// fallback 多几 MB 落盘/日志），所以 base 传空——专属日志照打，
				// 锚点是标记与判据名，不是文件。
				if ev := v.Evidence; ev != nil {
					s.recordShapeEvidence(reqID, a.Binding.String(), ev, "", body, newBody, eb,
						r.Header, resp.Header, routeStr)
				}
				s.logf("[proxy] %s", i18n.T("advancing to next chain step: {step}", i18n.A{"step": steps[i+1]}))
				metrics.Default.Inc("chain.step_failed")
				lastMsg, lastCode = trim(string(eb)), resp.StatusCode
				trail = append(trail, fmt.Sprintf("%s(%d)", a.Binding, resp.StatusCode))
				continue
			}
			base, evErr := s.saveErrEvidence(reqID, resp.StatusCode, body, newBody, eb,
				r.Header, resp.Header, routeStr)
			if evErr != nil {
				s.logf("[proxy] %s", i18n.T(
					"#{req} upstream {code}, writing evidence failed (the directory is creatable but files are not writable; check permissions/disk — CLAUDE.md §3.1 records this trap): {err}",
					i18n.A{"req": reqID, "code": resp.StatusCode, "err": evErr}))
			} else {
				s.logf("[proxy] %s", i18n.T("#{req} upstream {code}, full evidence saved to {base}.*", i18n.A{"req": reqID, "code": resp.StatusCode, "base": base}))
			}
			// 终局这一发走的是和沿链转移**同一份**留痕实现（见
			// recordShapeEvidence）：这里刚落了通用 err-* 证据，把它的基名
			// 一并交给专属日志当锚点。
			if ev := v.Evidence; ev != nil {
				s.recordShapeEvidence(reqID, a.Binding.String(), ev, base, body, newBody, eb,
					r.Header, resp.Header, routeStr)
			}
			s.logf("[proxy] %s", i18n.T("#{req} upstream raw: {body}", i18n.A{"req": reqID, "body": truncate(string(redact(eb)), 2000)}))
			s.logf("[proxy] %s", i18n.T("#{req} body we sent ({bytes} bytes): {body}", i18n.A{"req": reqID,
				"bytes": len(newBody), "body": truncate(string(redact(newBody)), 4000)}))

			for k, vs := range resp.Header {
				if respHopHeaders[http.CanonicalHeaderKey(k)] || http.CanonicalHeaderKey(k) == "Content-Length" {
					continue
				}
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.Header().Set("X-Newgate-Route", routeStr)
			w.Header().Set("X-Newgate-Profile", a.Profile)
			w.Header().Set("X-Newgate-Chain", chainHeader(trail, a))
			w.Header().Set("X-Newgate-Evidence", filepath.Base(base))
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(eb)
			return
		}
		ok := policy.Outcome{
			Provider: a.Binding.Provider, Model: a.Binding.Model,
			Binding: a.Binding.String(), Kind: policy.Succeeded,
			RequestBytes: len(newBody), TTFT: attemptTTFT,
		}
		s.outcome(ok)
		s.Filters.Observe(ok)
		if i > 0 {
			metrics.Default.Inc("chain.failover")
		}
		if gatewaystate.DebugActive(st) {
			s.logf("[proxy] %s", i18n.T("#{req} upstream response headers {code}{headers}", i18n.A{"req": reqID, "code": resp.StatusCode, "headers": headerDump(resp.Header)}))
		}
		s.logf("[proxy] %s", i18n.T(
			"#{req} {route}  {path}  {status}  first byte {ms} ms  stream={stream}  profile={profile}{failover}",
			i18n.A{"req": reqID, "route": routeStr, "path": suffix, "status": resp.StatusCode,
				"ms": time.Since(start).Milliseconds(), "stream": stream, "profile": a.Profile,
				"failover": map[bool]string{true: i18n.T("  (failover)", nil)}[i > 0]}))

		for k, vs := range resp.Header {
			if respHopHeaders[http.CanonicalHeaderKey(k)] {
				continue // Transfer-Encoding / Connection 由 Go 的 server 自己管
			}
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("X-Newgate-Route", routeStr)
		w.Header().Set("X-Newgate-Profile", a.Profile)
		w.Header().Set("X-Newgate-Chain", chainHeader(trail, a))
		if i > 0 {
			// 转移了必须明确告知，绝不静默（docs/05-gateway.md）
			// HTTP header 只能装 latin-1，中文会乱码——只放 ASCII，详情在日志里
			w.Header().Set("X-Newgate-Failover", fmt.Sprintf("%s -> %s (upstream %d; see: newgate logs)",
				steps[0].Profile, a.Profile, lastCode))
		}
		w.WriteHeader(resp.StatusCode)

		// 逐块转发。响应体一个字节都不改，也绝不缓冲整个响应——**除非**这一发
		// 的响应被人认领了（special.ClaimEgress，在发请求前问过）。
		//
		// 认领的那一发要按 SSE 事件边界切开再转发：拆帧只改变**写出时机**，不改
		// 变字节（每条事件原样交给认领者，它说不动就一字不动）。没人认领时走的是
		// 原来那条逐字节直通的快路——「没有补丁时转发就是转发」这条承诺，不因为
		// 多了一个扩展点而变贵。
		//
		// 旁路挂一个观测者，把上游吐出来的推理内容记进 thinkcache，供下一轮
		// 补回去（客户端会把它剥掉，见 modules/deepseek/st-reasoning.go）。
		// 它是**纯只读**的：拿到的是已经写给客户端的那一份字节，不参与转发，
		// 看错了最坏结果是这轮没缓存上。
		flusher, canFlush := w.(http.Flusher)
		ob := thinkcache.NewObserver()
		var nonStream []byte // 非流式：整个 body 才能解析，攒完再看（有上限）
		buf := make([]byte, 32*1024)
		var chunks int
		var bytesOut int64

		// 有人认领这一发的响应吗（special.ClaimEgress，在发请求前问过）。认领了
		// 才拆 SSE 事件；没有就是原来那条逐字节直通的快路。
		egressOn := stream && egress != nil
		// 出站改写必须留痕（「不静默」在这条路上的落点）：它改的是**上游回来的
		// 字节**，用户手里那份原文与上游给的对不上，只有日志能解释为什么。
		var egressEvents int
		var sp *sseSplitter
		writeOut := func(p []byte) error {
			_, err := w.Write(p)
			return err
		}
		if egressOn {
			sp = newSSESplitter(w, egress, func(orig, out []byte) { egressEvents++ })
			writeOut = sp.write
		}
		for {
			n, rderr := resp.Body.Read(buf)
			if n > 0 {
				if werr := writeOut(buf[:n]); werr != nil {
					metrics.Default.Inc("client.cancel")
					s.logf("[proxy] %s", i18n.T("#{req} client disconnected (forwarded {chunks} chunks / {bytes} bytes): {err}",
						i18n.A{"req": reqID, "chunks": chunks, "bytes": bytesOut, "err": werr}))
					return
				}
				if stream {
					// 观测者拿到上游给的**原字节**，不是改写后的：thinkcache 要
					// 回填的是上游那一轮的推理原文，补丁改过的形状帮不上忙，
					// 而且改坏的形状会让缓存整轮落空。
					ob.Write(buf[:n])
				} else if len(nonStream) < 8<<20 {
					nonStream = append(nonStream, buf[:n]...)
				}
				chunks++
				bytesOut += int64(n)
				if canFlush {
					flusher.Flush() // 每读到就吐，不等缓冲区满
				}
			}
			if rderr != nil {
				switch {
				case rderr == io.EOF:
					if sp != nil {
						// 尾巴：很多上游的最后一个事件不带结尾空行，不吐就丢字节。
						if ferr := sp.flush(); ferr != nil {
							metrics.Default.Inc("client.cancel")
							s.logf("[proxy] %s", i18n.T("#{req} client disconnected (forwarded {chunks} chunks / {bytes} bytes): {err}",
								i18n.A{"req": reqID, "chunks": chunks, "bytes": bytesOut, "err": ferr}))
							return
						}
					}
					if egressEvents > 0 {
						s.logf("[proxy] %s", i18n.N(
							"#{req} response rewritten on the way out ({n} event): upstream bytes differ from what the client got",
							"#{req} response rewritten on the way out ({n} events): upstream bytes differ from what the client got",
							egressEvents, i18n.A{"req": reqID, "n": egressEvents}))
					}
					if !stream && len(nonStream) > 0 {
						ob.ObserveBody(nonStream)
					}
					origin := thinkcache.Origin{
						Profile: a.Profile, Provider: a.Binding.Provider, Model: a.Binding.Model,
					}
					if nb, nk := ob.CommitWithOrigin(thinkcache.Default, origin); nb > 0 {
						// 只打字节数和 key 数，绝不打内容。key 是 tool:<id> 形式的
						// **身份**，不是内容——它是把「这一轮存了」和下一轮「这轮
						// 补上了原文 / 这轮没有原文可补」对起来的唯一线索
						// （见 st-reasoning.go 的 skipCause）。2026-09-18 加。
						s.logf("[proxy] %s", i18n.N(
							"#{req} recorded {bytes} bytes of reasoning and {n} key {keys} this round; replaying them to the client next round",
							"#{req} recorded {bytes} bytes of reasoning and {n} keys {keys} this round; replaying them to the client next round",
							nk, i18n.A{"req": reqID, "bytes": nb, "keys": keysForLog(ob.Keys())}))
					} else if ntc := ob.ToolCalls(); ntc > 0 {
						// 有 tool call 却一个字节推理都没有：下一轮这些历史消息
						// **一条都补不上**（插件不编占位符，见 st-reasoning.go 文件头），
						// 而这是**上游本来就没给**，不是我们弄丢的。两种情况的处置完全
						// 相反（前者只能接受，后者要查缓存），所以这一行必须存在——
						// 否则事后只看补丁那侧的日志，会把「上游没给」误判成缓存失效，
						// 然后去修一个没坏的东西。
						//
						// 连**请求里的 thinking 配置**一起打：DeepSeek 不认 Anthropic
						// 的 adaptive（Claude Code 的默认值），收到 adaptive 时一个思考
						// 块都不吐——请求侧完全合法、日志全绿，只有把这个值打出来才看
						// 得出「不是缓存坏了，是上游根本没思考」。
						s.logf("[proxy] %s", i18n.N(
							"#{req} upstream sent no reasoning this round ({n} tool call, request thinking={thinking}, tool id {keys}); wire layer: {wire}. None of these history messages can be replayed next round (no placeholders are fabricated)",
							"#{req} upstream sent no reasoning this round ({n} tool calls, request thinking={thinking}, tool id {keys}); wire layer: {wire}. None of these history messages can be replayed next round (no placeholders are fabricated)",
							ntc, i18n.A{"req": reqID, "thinking": thinkingTagOf(newBody),
								"keys": keysForLog(ob.Keys()), "wire": ob.Wire()}))
					}
					s.learnStreamFailure(reqID, a.Binding.Provider, a.Binding.Model, ob)
					if stream {
						s.logf("[proxy] %s", i18n.T("#{req} stream ended normally: {chunks} chunks / {bytes} bytes / {ms} ms total",
							i18n.A{"req": reqID, "chunks": chunks, "bytes": bytesOut,
								"ms": time.Since(start).Milliseconds()}))
					} else {
						s.logf("[proxy] %s", i18n.T("#{req} non-stream response ended: {bytes} bytes / {ms} ms total",
							i18n.A{"req": reqID, "bytes": bytesOut, "ms": time.Since(start).Milliseconds()}))
					}
				case r.Context().Err() != nil:
					metrics.Default.Inc("client.cancel")
					s.logf("[proxy] %s", i18n.T("#{req} client cancelled; upstream cut off (saves the remaining tokens)", i18n.A{"req": reqID}))
				default:
					// 响应已经开始往客户端写，中途上游断了。换不了站（写出去的
					// 东西收不回），但这是实打实的可用性问题——以前这里只打一行
					// 日志，一个每次流到一半就断的上游在健康账本上完全隐形。
					o := policy.Outcome{
						Provider: a.Binding.Provider, Model: a.Binding.Model,
						Binding: a.Binding.String(), Kind: policy.StreamCut,
						ResponseStarted: true, IsLast: isLast,
						RequestBytes: len(newBody), TTFT: attemptTTFT,
					}
					v := s.outcome(o)
					if v.Attribute {
						atomic.AddUint64(&s.failures, 1)
					}
					s.logf("[proxy] %s", i18n.T("#{req} upstream stream cut (forwarded {chunks} chunks / {bytes} bytes): {err}{note}",
						i18n.A{"req": reqID, "chunks": chunks, "bytes": bytesOut, "err": rderr, "note": v.Note}))
					// 断流之前也可能已经报过一句「我拒了这一发」（Responses 的
					// response.failed 之后就是 close）。这一句与断流本身是两件事：
					// 断流进健康账本（上面那个 policy.Outcome），**拒发的理由**
					// 进 quirk 表——后者才是下一发不再撞的东西，而它只有顺着
					// 观测者才看得见（见 learnStreamFailure）。
					s.learnStreamFailure(reqID, a.Binding.Provider, a.Binding.Model, ob)
				}
				return
			}
		}
	}
}

// learnStreamFailure 处置「上游在**事件流里**报的那次拒绝」。
//
// # 为什么这条路上必须有它
//
// Responses 方言的上游在 `stream: true` 时不走 HTTP 状态码：先 200 开流，再把
// 失败塞进事件流（`response.failed`）。于是数据面那条按状态码走的 learnQuirks
// （`status >= 400` 才学）在 Codex 这条路上**一次都不会触发**——用户看到的是
// 「必现、过一会儿自己好」：第一发撞，第二发起就好了？不，是**永远撞**，除非
// 进程里恰好有人 probe 过那一家。2026-09-22 报的那句
// 「stream disconnected before completion: 该模型始终思考…」正是这个形态。
//
// 观测者（thinkcache.Observer）是这条路上**唯一**读到那些字节的地方，所以
// 「上游拒了这一发」这个事实只能从它那里问（见 Observer.StreamFailure）。
//
// # 为什么把状态码写成 400
//
// `quirk.Table.Learn` 的判据是「4xx + 报错原文」：4xx 表达的是「上游拒了你的
// 请求形状」（与 5xx 的「上游自己挂了」相对），而这个失败**就是**这一族——只是
// 它没走状态码。真正重要的是原文，它是签名的匹配输入，一字不差地传下去。
//
// 学到的结果与撞上真 400 完全一样：`quirk.Default` 立刻标上，下一个请求
// `always-thinks` 的 Match 就绿了，请求体里那个上游不收的值被就地改掉
// （见 modules/thinking/st-always.go 的 fixResponsesEffort）；同时记进
// `pendingQuirks`，停机/换版时落盘，下一个进程的第一发不再撞。
func (s *Server) learnStreamFailure(reqID uint64, provider, model string, ob *thinkcache.Observer) {
	msg, failed := ob.StreamFailure()
	if !failed {
		return
	}
	s.logf("[proxy] %s", i18n.T("#{req} {provider}/{model} reported a failure inside the event stream (HTTP 200, so the status code says nothing): {msg}",
		i18n.A{"req": reqID, "provider": provider, "model": model, "msg": trim(msg)}))
	s.learnQuirks(reqID, provider, model, 400, []byte(msg))
}

// flushPendingQuirks 把「转发路上学到、还没落盘」的上游毛病写出去。
//
// 这一笔以前只活在内存里，重启就丢，于是「重启后第一发 Codex → GLM」必然重新
// 撞一次 400——用户 2026-09-22 报的那个「必现、过一会儿自己好」的形态
// （见 Server.pendingQuirks 与 probe.PendingQuirks）。
//
// **两条停机路径都要叫它**，这一点是这个函数单独存在的唯一理由：`Shutdown`
// （收到信号／控制端点）与**优雅交接**（把 socket 交给新进程）走的是两段代码，
// 而换版用的正是后者——只补前者的话，「换版之后第一发」这个最常发生的场景
// 一个字节都没存下来，而这正是用户报的那一次（见 handoff 那段）。
//
// 落盘失败只报不改行为：磁盘写不进去时，最坏的情况不过是从前那种「重启后重学
// 一次」，而不是停机被拦住（fail-open）。
func (s *Server) flushPendingQuirks() {
	if err := s.pendingQuirks.Flush(); err != nil {
		s.logf("[probe] %s", i18n.T("upstream quirks learned from traffic could not be saved; the next restart relearns them: {err}",
			i18n.A{"err": err}))
	}
}

// learnQuirks 从上游的报错里学它的毛病，下次请求自动带上补丁。
//
// 只在**新学到**的时候打日志——同一件事每个请求刷一行就没人看了。
// 学到什么必须说，这是「不静默」的一部分：用户得知道我们从下一个请求开始
// 会往他的请求里多加东西。
func (s *Server) learnQuirks(reqID uint64, provider, model string, status int, body []byte) {
	for _, what := range quirk.Default.Learn(provider, model, status, body) {
		s.logf("[proxy] %s", i18n.T("#{req} learned {provider}/{model} {what} — applied to later requests automatically (turn off with `newgate st`)",
			i18n.A{"req": reqID, "provider": provider, "model": model, "what": what}))
		// 学到的东西**同时记进待落盘那一份**：这一笔只活在内存里的话，重启之后
		// 它会连带这个窗口一起消失（见 Server.pendingQuirks 的说明）。
		//
		// 记的是**这一个目标此刻全部已知的毛病位**，不是这一次新学的那一位：
		// 并集写进缓存，少写一位的症状是重启后多撞一次 400，而那是可恢复的
		// （见 probe.MergeQuirks）。
		s.pendingQuirks.Record(probe.Target{Provider: provider, Model: model},
			probe.QuirksOf(probe.Target{Provider: provider, Model: model}))
	}
}

// dump 在 NEWGATE_DUMP=1 时把「收到的」和「发出的」请求体落盘。
// 排查「是不是代理改坏了请求」时，这是唯一能拿出证据的手段。
// 只留最近 30 组——本会话的 transcript 单条就能上 MB，不设上限磁盘
// 很快就满（和 err-* 错误证据各自独立清理，互不删对方）。
func (s *Server) dump(reqID uint64, attempt int, in, out []byte) {
	if os.Getenv("NEWGATE_DUMP") == "" {
		return
	}
	dir := filepath.Join(paths.Config(), "dump")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	base := filepath.Join(dir, fmt.Sprintf("req-%06d-%d", reqID, attempt))
	var errs []error
	for _, f := range []struct {
		path string
		data []byte
	}{{base + ".in.json", in}, {base + ".out.json", out}} {
		if err := os.WriteFile(f.path, f.data, 0o600); err != nil {
			errs = append(errs, err)
		}
	}
	same := i18n.T("length delta vs original after rewrite: {diff} bytes", i18n.A{"diff": len(out) - len(in)})
	if err := errors.Join(errs...); err != nil {
		s.logf("[proxy] %s", i18n.T("#{req} dump write failed (check permissions/disk): {err}", i18n.A{"req": reqID, "err": err}))
	} else {
		s.logf("[proxy] #%d dump → %s.{in,out}.json  (%s)", reqID, base, same)
	}
	logx.PruneDirBy(dir, "req-", 30)
}

// redact 把请求/响应里像密钥的东西抹掉，日志和 dump 都用它。
var secretPat = regexp.MustCompile(`(sk-[A-Za-z0-9_\-]{8,}|Bearer\s+[A-Za-z0-9_\-\.]{8,})`)

func redact(b []byte) []byte {
	return secretPat.ReplaceAll(b, []byte("[REDACTED]"))
}

func headerDump(h http.Header) string {
	var keys []string
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		v := strings.Join(h[k], ", ")
		if k == "Authorization" || k == "X-Api-Key" || k == "Api-Key" {
			v = "[REDACTED]"
		}
		sb.WriteString("\n      ")
		sb.WriteString(k)
		sb.WriteString(": ")
		sb.WriteString(v)
	}
	return sb.String()
}

// saveErrEvidence 上游报错时把完整证据落盘。这是排查
// 「是不是代理改坏了请求」唯一能拿出手的东西，所以不设开关。
//
// **返回错误，调用方必须如实说**（2026-09-18 改）：这里原来是四个 `_ =`，而调用
// 点的日志是无条件打的「完整证据已存」。于是写盘失败时用户看到「已存」而磁盘上
// 一个字节都没有——CLAUDE.md §3.1 的多用户权限坑里，那个症状的原文就是
// 「400 证据『已存』其实没写出」。**声称存了而没存比不存更坏**：排查的人会以为
// 证据在，花时间去翻目录，然后怀疑是不是自己记错了路径。
func (s *Server) saveErrEvidence(reqID uint64, status int, inBody, outBody, respBody []byte,
	reqHdr http.Header, respHdr http.Header, routeStr string) (string, error) {
	dir := filepath.Join(paths.Config(), "dump")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	base := filepath.Join(dir, fmt.Sprintf("err-%03d-req%06d", status, reqID))
	meta := i18n.T("route: {route}\nstatus: {status}\n\n--- client request headers ---{reqHeaders}\n\n--- upstream response headers ---{respHeaders}\n",
		i18n.A{"route": routeStr, "status": status,
			"reqHeaders": headerDump(reqHdr), "respHeaders": headerDump(respHdr)})
	var errs []error
	for _, f := range []struct {
		path string
		data []byte
	}{
		{base + ".client-sent.json", redact(inBody)},
		{base + ".we-sent.json", redact(outBody)},
		{base + ".upstream-said.json", redact(respBody)},
		{base + ".meta.txt", []byte(meta)},
	} {
		if err := os.WriteFile(f.path, f.data, 0o600); err != nil {
			errs = append(errs, err)
		}
	}
	// 只清 err- 前缀的组：dump 目录里 req-* 也住一起，各设各的上限（见 dump 处
	// 的 PruneDirBy(dir,"req-",30)），空前缀会把对方的也一起删掉——错误证据刚
	// 落地几秒就被 req-* 挤没了，等于没存。
	logx.PruneDirBy(dir, "err-", 20)
	return base, errors.Join(errs...)
}

// saveShapeEvidence 请求形状被拒时把现场存进**专用目录**，不参与 dump 目录的
// req-*/err-* 滚动清理——这类 400 偶发又致命，丢了就再也复现不了（本会话的
// transcript 单条就能上 MB，dump 目录几十组就满了，而这类 400 往往隔很久才来
// 一次，等不到下一次就被挤没了）。当前认领它的只有 DeepSeek 那条判据
// （modules/deepseek/shape.go），但目录名取自检测器自己起的名字，所以加一条
// 新判据就会自动多一个证据目录，转发路径不用改。
//
// 目录结构：dump/<标记>-<判据>/req-<id>-<unixnano>/，里面放客户端发来的、
// 我们发出的、上游说的，外加一份逐条审计（哪几条 assistant 消息被补过字段）。
// 两段名字都来自策略给的 Evidence（今天标记是 "shape-400"、判据是 deepseek，
// 于是路径与 2026-09-18 之前一字不差）。等宽上限只按总字节数封顶
// （512MB，约几百个现场），超了才清最旧的——正常排查用根本到不了这个量，
// 等于「不删」。
//
// 两段名字进路径前都做了白名单过滤（见 shapeDirName）：策略是别的模块注册
// 进来的代码，名字里带 `/` 或 `..` 就能让这个函数往配置目录外写文件。
// 与 saveErrEvidence 同理：这类现场「丢了就再也复现不了」，所以写不出去必须说
// （2026-09-18 改，原来五个 `_ =` 加一句无条件的「现场已存档」）。
func (s *Server) saveShapeEvidence(reqID uint64, ev *policy.Evidence, inBody, outBody, respBody []byte,
	reqHdr http.Header, respHdr http.Header, routeStr string) (string, error) {
	dir := filepath.Join(paths.Config(), "dump", shapeDirName(ev.Tag, ev.Subject),
		fmt.Sprintf("req-%06d-%d", reqID, time.Now().UnixNano()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	meta := i18n.T("route: {route}\nstatus: 400\ntag: {tag}\ndetector: {detector}\n\n--- client request headers ---{reqHeaders}\n\n--- upstream response headers ---{respHeaders}\n",
		i18n.A{"route": routeStr, "tag": ev.Tag, "detector": ev.Subject,
			"reqHeaders": headerDump(reqHdr), "respHeaders": headerDump(respHdr)})
	var errs []error
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"client-sent.json", redact(inBody)},
		{"we-sent.json", redact(outBody)},
		{"upstream-said.json", redact(respBody)},
		{"audit.txt", []byte(special.AuditResponse(outBody))},
		{"meta.txt", []byte(meta)},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.data, 0o600); err != nil {
			errs = append(errs, err)
		}
	}
	pruneShapeEvidence(filepath.Dir(dir), 512<<20)
	return dir, errors.Join(errs...)
}

// recordShapeEvidence 是「被形状判据认领」这一发**唯一**的留痕实现，终局与
// 沿链转移两条路径共用它：先打 `[标记]` 的专属日志（判据名 + 通用证据基名），
// 再按 Evidence.Archive 把现场归档进不参与滚动清理的专用目录。
//
// 为什么必须共用（2026-09-17 的现场，见 forward_test.go 的
// TestShapeAdvanceKeepsClaimedEvidence）：形状 400 换个校验更松的上游是唯一
// 可能成功的路，所以「裁决为继续沿链」恰恰是它最常见的收场。旧实现把这段
// 只留在终局分支里，转移那一发就既不归档也不打日志——客户端最后拿到 200，
// 日志里却看不出中间那家拒过、拒的是什么形状，正是这次事故的形态。
//
// base 是通用 err-* 证据的基名（终局路径刚落的）；沿链转移那一发刻意不落
// 通用 err-*（整包 body dump 会让每次 fallback 多几 MB 落盘/日志，违反
// 「尽量少影响」），于是基名为空——专属日志照打，锚点是标记与判据名，不是
// 文件。判据名/标记都来自注册进来的策略，转发路径原样搬运、不解释。
func (s *Server) recordShapeEvidence(reqID uint64, binding string, ev *policy.Evidence, base string,
	inBody, outBody, respBody []byte, reqHdr, respHdr http.Header, routeStr string) {
	if ev == nil {
		return
	}
	// 这类结局要能一眼 grep 出来。转发路径不认识任何上游专有字符串，也不知道
	// 这条 400 是哪家的方言——`[shape-400]` 那个字面量与目录前缀由认领它的策略
	// 给（见 policy.Evidence）。
	baseName := base
	if baseName != "" {
		baseName = filepath.Base(baseName)
	}
	s.logf("[%s] %s", ev.Tag, i18n.T("#{req} {binding} request shape rejected (detector {detector}; evidence {base}.*)",
		i18n.A{"req": reqID, "binding": binding, "detector": ev.Subject, "base": baseName}))
	if !ev.Archive {
		return
	}
	rdir, shErr := s.saveShapeEvidence(reqID, ev, inBody, outBody, respBody, reqHdr, respHdr, routeStr)
	if shErr != nil {
		s.logf("[%s] %s", ev.Tag, i18n.T("#{req} scene NOT saved (check permissions/disk): {err}", i18n.A{"req": reqID, "err": shErr}))
		return
	}
	s.logf("[%s] %s", ev.Tag, i18n.T("#{req} scene archived to {dir}/", i18n.A{"req": reqID, "dir": rdir}))
}

// shapeDirName 把「标记 + 判据名」压成一个安全的目录名：只留字母数字和
// `-_.`，其余一律换成 `_`，空名退化成 `unknown`。两段名字都会进文件路径，
// 而它们都来自**别的模块**注册进来的策略——不设防就等于让一个注册项决定
// 往哪写文件（`../` 一次就能写到配置目录外面去）。
//
// 两段的来源不同，所以不合成一个参数：Tag 是**这一类结局**的名字（策略给，
// 今天恒为 "shape-400"，于是 dump/shape-400-deepseek/ 这个路径与 2026-09-18
// 之前一字不差），Subject 是**谁认领的**（形状检测器自己起的名字）。
func shapeDirName(tag, subject string) string {
	var b strings.Builder
	if tag == "" {
		tag = "shape"
	}
	for _, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	b.WriteString("-")
	before := b.Len()
	for _, r := range subject {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == before {
		b.WriteString("unknown")
	}
	return b.String()
}

// pruneShapeEvidence 按总字节数封顶清理证据目录：超了就删最旧的子目录，直到
// 回到上限以下。比按个数更可预测，磁盘安全——但上限给得很宽，正常排查根本
// 触不到（见 saveShapeEvidence）。
func pruneShapeEvidence(dir string, maxBytes int64) {
	ents, err := ioutil.ReadDir(dir)
	if err != nil {
		return
	}
	var subdirs []os.FileInfo
	var total int64
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		subdirs = append(subdirs, e)
		total += dirSize(filepath.Join(dir, e.Name()))
	}
	if total <= maxBytes {
		return
	}
	// ioutil.ReadDir 已按名字排序，子目录名带 unixnano，最旧的在前。
	for _, e := range subdirs {
		if total <= maxBytes {
			break
		}
		p := filepath.Join(dir, e.Name())
		total -= dirSize(p)
		_ = os.RemoveAll(p)
	}
}

func dirSize(dir string) int64 {
	var n int64
	ents, err := ioutil.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, e := range ents {
		if !e.IsDir() {
			n += e.Size()
		}
	}
	return n
}

// chainHeader 描述链实际走了哪几步。ASCII only（HTTP header 装不了中文）。
func chainHeader(trail []string, final resolve.Step) string {
	all := append(append([]string{}, trail...), final.Binding.String()+"(ok)")
	return strings.Join(all, " -> ")
}

func fmtSkips(skips []resolve.Skip) string {
	var parts []string
	for _, sk := range skips {
		t := sk.Target
		if t == "" {
			t = sk.Profile
		}
		parts = append(parts, t+"="+sk.Reason)
	}
	return strings.Join(parts, "; ")
}

// formatSteps 把链上的几步列成一行，给「被短路砍掉了谁」那条日志用。
// 用 Step 自己那套 `profile:provider/model` 写法——与 `newgate tier`、
// X-Newgate-Chain 同一个词汇，日志里不该有第二种认法。
func formatSteps(steps []resolve.Step) string {
	parts := make([]string, 0, len(steps))
	for _, s := range steps {
		parts = append(parts, s.String())
	}
	return strings.Join(parts, ", ")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + i18n.T("…(truncated, {n} characters total)", i18n.A{"n": len(r)})
}

// keysForLog 把 thinkcache 的 key 列表收窄成日志里该出现的那部分。
//
// 只留 `tool:` 开头的：那是**工具调用 id**，是纯身份字符串，能把「这一轮存了
// 什么」和下一轮「这轮补上了原文 / 这轮没有原文可补」精确对上。`text:` 开头的是正文的哈希——
// 它同时充当 key 和摘要，而本仓库有一条硬规矩：**任何路径都不打内容**，
// 连哈希也不该外泄到日志里去和别处的正文比对。所以只打前缀和个数。
//
// 上限 8：一次响应通常 1~3 个 tool call，够用；真遇到几十个并行调用的，也不
// 至于把一行日志撑爆。
// pluginOff 把「某个插件被单独关掉了吗」包成函数值——special 那一层的 API 收的
// 就是这个形状（它不认识 domain.State，也不该认识）。两处调用点共用这一份，免得
// 各写一遍闭包、哪天漏改一处。
//
// 开关状态住在 state.json 的 ModuleConfig["gateway"] 里，读法归 gatewaystate
// （见那个包的说明：网关的开关词汇不该出现在共享配置的类型里）。
func pluginOff(st *domain.State) func(string) bool {
	return func(name string) bool { return gatewaystate.PluginOff(st, name) }
}

func keysForLog(keys []string) []string {
	var out []string
	texts := 0
	for _, k := range keys {
		if strings.HasPrefix(k, "tool:") {
			if len(out) < 8 {
				out = append(out, k)
			}
			continue
		}
		texts++
	}
	if texts > 0 {
		out = append(out, i18n.N("+{n} body key", "+{n} body keys", texts, i18n.A{"n": texts}))
	}
	return out
}

// thinkingTagOf 抽出这次**实际发给上游**的 body 里 thinking.type 的原文。
//
// 为什么要打这个：2026-09-18 排查「为什么没有推理内容」时，最关键的一栏就是
// 它。DeepSeek 不认 Anthropic 的 adaptive（它是 Claude Code 的默认值），
// 收到 adaptive 时一个思考块都不吐——请求侧完全合法、日志里也全绿，只有把
// 这个值打出来才看得出「不是缓存坏了，是上游根本没思考」。返回值带引号，
// 空串表示**字段缺席**（那又是另一种情况：请求压根没进思考模式）。
func thinkingTagOf(body []byte) string {
	raw, has := rewrite.TopLevelRaw(body, "thinking")
	if !has {
		return i18n.T("(field absent)", nil)
	}
	t, ok := rewrite.TopLevelString(raw, "type")
	if !ok || t == "" {
		return i18n.T("(unrecognized)", nil)
	}
	return t
}

func trim(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 180 {
		return s[:180] + "…"
	}
	return s
}

func (s *Server) fail(w http.ResponseWriter, code int, msg string) {
	atomic.AddUint64(&s.failures, 1)
	s.logf("[proxy] %s", i18n.T("error {code}: {msg}", i18n.A{"code": code, "msg": msg}))
	// 错误体里必须带 newgate 字样和排查命令（docs/05-gateway.md）
	writeJSON(w, code, map[string]interface{}{
		"error": map[string]interface{}{
			"type":    "newgate_error",
			"message": "[newgate] " + msg,
			"hint":    i18n.T("troubleshooting: newgate status / newgate doctor / newgate stop (restores a direct connection)", nil),
		},
	})
}

// 等上游的时间参数不在这里定义：它们是 state.json 的 timeouts 字段
// （domain.Timeouts），watcher 热加载——改配置即生效，不用重编译。
// 缺省值见 domain.Timeouts 各 accessor（流式首字节 150s / 非流式 12s /
// 非流式总超时 15min）。

// respHopHeaders 响应里必须剥掉的 hop-by-hop 头。
var respHopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Transfer-Encoding": true,
	"Te": true, "Trailer": true, "Upgrade": true, "Proxy-Authenticate": true,
}

var hopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
	"Authorization": true, "X-Api-Key": true, // 客户端的假 key 一律丢掉
	"Content-Length": true, "Host": true,
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func setAuth(h http.Header, p domain.Provider) {
	key := p.Key()
	switch p.Protocol {
	case "anthropic":
		h.Set("x-api-key", key)
		if h.Get("anthropic-version") == "" {
			h.Set("anthropic-version", "2023-06-01")
		}
	default:
		h.Set("Authorization", "Bearer "+key)
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	b, _ := json.MarshalIndent(v, "", "  ")
	_, _ = w.Write(append(b, '\n'))
}

// writeShortCircuit 把 special.Responder 的回答作为本次请求的最终响应写出去。
//
// 短路插件返回的是**协议层响应体**（对 anthropic Messages 请求就是一个合法
// messages JSON），这里补 HTTP 头、打日志、发 metric，让一次「根本没发出去的
// 上游调用」同样在每一条观测带上留痕——这是「不静默」在短路路径上的落地：
// 响应看得到，日志看得到，计数器看得到，用户永远知道这一发没走上游。
//
// `profile` 是这一发**本来会走**的那份（upstream 已在 /p/<profile> 覆盖之后算好）。
// 短路发生在构链之前，所以「这一发解析到了哪条链」没有答案；但「调用方问的是哪
// 份 profile」有——把它回显出来，调用方才能回答「我明明点了 ds，为什么这一发没
// 有按 ds 走」。2026-09-23 之前这里写死 `n/a`：请求 `/a/claude/p/ds/...` 得回来
// 一个 `X-Newgate-Profile: n/a`，看着像 profile 丢了，实际是短路本来就不经过链。
// 回显之后 `n/a` 只留给「连默认 profile 都没有」的装配（骨架发行版没装 config
// 的链那段时）。
func (s *Server) writeShortCircuit(w http.ResponseWriter, _ *http.Request,
	reqID uint64, plugin string, body []byte, note, profile string) {
	metrics.Default.Inc("special." + plugin + ".shortcircuit")
	if note == "" {
		note = i18n.T("request short-circuited by special plugin {plugin} (no upstream call)", i18n.A{"plugin": plugin})
	}
	s.logf("[proxy] %s", i18n.T("#{req} {note} ({bytes} bytes)", i18n.A{"req": reqID, "note": note, "bytes": len(body)}))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Newgate-Route", "naked:"+plugin)
	w.Header().Set("X-Newgate-Chain", plugin)
	if profile == "" {
		profile = "n/a"
	}
	w.Header().Set("X-Newgate-Profile", profile)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
