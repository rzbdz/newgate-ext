// Package controlplane 是与**跑着的守护进程**控制面打交道的客户端，以及那份
// 状态文档的形状。
//
// # 为什么它是一个共享叶子（2026-09-18）
//
// 这套东西原来长在 modules/cli 里（proxy.go + lifecycle.go 的一小块）。后果是
// 只有界面读得到守护进程的自报状态，于是**每个想读它的模块都被钉住了**：
// `newgate metrics`（计数器的语义归 gateway）、`newgate breaker`（健康表归
// breaker）、`newgate probe`（探活结果要回传给守护进程记账）——它们的知识明明
// 各自归自己，命令却只能写在界面里，因为界面是唯一持有那个 HTTP 客户端的地方。
//
// 抽成共享叶子之后，谁都能读守护进程了，那些命令也就搬得回自己的模块。
//
// # 它为什么不是一个模块
//
// 它没有状态、没有生命周期、不需要被启动或停止——就是一组对 loopback 控制面的
// 读写函数（外加一个发 SIGHUP 的便捷口）。这跟 lib/ 里那些纯工具同类，只是它
// 认识 newgate 的控制面协议，所以留在 gateway 这一侧（控制面的路径与文档都由
// 数据面定义，见 controlpath）。
//
// # 依赖方向
//
// controlplane → controlpath（路径）、breaker/status（文档里的 wire 类型）、
// runtime/daemon（pidfile）、lib/httpx。四个都是叶子或工具，没有回边，所以
// breaker 自己、gateway 自己、界面都能引它。
package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"syscall"
	"time"

	"github.com/rzbdz/newgate-ext/lib/httpx"
	"github.com/rzbdz/newgate-ext/modules/breaker/status"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlpath"
	"github.com/rzbdz/newgate-ext/modules/runtime/daemon"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Think 是推理内容缓存的计数。**只报计数，永远不报内容。**
type Think struct {
	Entries   int   `json:"entries"`
	Bytes     int64 `json:"bytes"`
	MaxBytes  int64 `json:"max_bytes"`
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Evictions int64 `json:"evictions"`
	// Unparsable 请求里解析不出来的 assistant 消息条数。它和 Misses 的结果一样
	// （都补空串），但处置不同：miss 去查缓存与上游，这个去查客户端发来的 JSON。
	Unparsable int64 `json:"unparsable"`
}

// Doc 是守护进程自报的运行时状态（`/__newgate/status` 的文档）。
//
// 整页状态（status / metrics / st）都只发**一次**这个请求就拿全——以前每个命令
// 各打各的探活，数字还可能对不上。
type Doc struct {
	OK       bool              `json:"ok"`
	Port     int               `json:"port"`
	UptimeS  int               `json:"uptime_s"`
	Requests uint64            `json:"requests"`
	Failures uint64            `json:"failures"`
	Breakers []status.Status   `json:"breakers"`
	Handoff  bool              `json:"handoff"`
	Default  string            `json:"default_profile"`
	Active   map[string]string `json:"active"`
	Think    Think             `json:"thinkcache"`
}

// Info 是守护进程的进程信息（来自 pidfile）。
type Info = daemon.Info

// State 取守护进程的进程信息 + 自报状态。
//
// pid 来自 pidfile（进程还在不在），doc 来自 HTTP（它自己怎么想的）。两者都可能
// 单独失败：进程活着但端口不响应，正是「出站代理劫持 loopback」那个经典故障，
// 必须能分开表达（见 newgate doctor）。
func State() (info *Info, doc *Doc) {
	info = daemon.Running()
	if info == nil || info.Port <= 0 {
		return info, nil
	}
	var d Doc
	if !get(info.Port, controlpath.Status, &d) {
		return info, nil
	}
	return info, &d
}

// Metrics 网关计数器与 uptime。和 status 是两个端点（计数器是守护进程内存里的
// 一张 map，status 只挑几个数报），所以单独取一次。
func Metrics(port int) (map[string]uint64, int, bool) {
	var out struct {
		Metrics map[string]uint64 `json:"metrics"`
		UptimeS int               `json:"uptime_s"`
	}
	if !get(port, controlpath.Metrics, &out) {
		return nil, 0, false
	}
	return out.Metrics, out.UptimeS, true
}

// Ping 端口上的控制面活着吗（HTTP 优先，退化成 TCP 探活）。
func Ping(port int) bool {
	resp, err := httpx.LocalClient(1500 * time.Millisecond).
		Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, controlpath.Status))
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			return true
		}
	}
	return httpx.TCPAlive("127.0.0.1", port, 500*time.Millisecond)
}

// Post 往控制面发一个带令牌的 POST。
func Post(port int, path, token string, body, out interface{}) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, path), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpx.LocalClient(3 * time.Second).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return i18n.E("daemon returned HTTP {status}", i18n.A{"status": resp.StatusCode})
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Notify 让运行中的守护进程立刻重读配置。
//
// 平时配置改动靠 watcher 的 1 秒轮询，对**手工编辑文件**足够快；但命令改配置是
// 用户刚敲下的，必须立刻生效，所以显式发 SIGHUP 强制重载。
func Notify() {
	if i := daemon.Running(); i != nil {
		_ = syscall.Kill(i.PID, syscall.SIGHUP)
	}
}

func get(port int, path string, v interface{}) bool {
	resp, err := httpx.LocalClient(1500 * time.Millisecond).
		Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	return json.NewDecoder(resp.Body).Decode(v) == nil
}

// Available 把 daemon 的全局熔断表冻结成一次命令内的一致快照：某条 binding 现在
// 能不能用（第二个返回值是给用户看的理由）。daemon 不在线时不凭空判坏
// （d == nil ⇒ 全部可用）——诊断退化为只看静态配置。
//
// **它是只读的近似，不是 daemon 里那个准入。** daemon 的 Admitter 有副作用
// （冷却期满时发放「半开试探名额」，一个名额只放行一次真实请求），这里只按快照里
// 的 Open 重建结论，既不发名额也不推进状态机。这个不一致是**刻意的**：
// CLI 每敲一次 `newgate tier` 就跑一遍，绝不能烧掉正在飞的试探名额。见
// docs/05-gateway.md。
func (d *Doc) Available() func(provider, model string) (bool, string) {
	blocked := map[string]string{}
	if d != nil {
		for _, b := range d.Breakers {
			if b.Open {
				reason := i18n.T("circuit open", nil)
				if b.Reason != "" {
					reason = i18n.T("circuit open ({reason})", i18n.A{"reason": b.Reason})
				}
				blocked[b.Provider+"/"+b.Model] = reason
			}
		}
	}
	return func(provider, model string) (bool, string) {
		if reason, ok := blocked[provider+"/"+model]; ok {
			return false, reason
		}
		return true, ""
	}
}

// Rank 把 daemon 算好的排序键原样递给调用方。
//
// **不在这里重算阈值**：3000ms / 12000ms 那套分桶只存在于 modules/breaker，抄一份
// 的话 daemon 改阈值这边不会跟着变（2026-09-17 之前就是两边各有一份）。
//
// 老 daemon 不发 `rank`（优雅交接期间 CLI 与 daemon 可以来自不同版本），读不到就
// 退化成中性值——排序退化为「按配置顺序」，不会因为版本不齐互相打架。被摘牌的
// binding 不在这里沉底：建链期先问 Available，被摘的根本进不了候选。
func (d *Doc) Rank() func(provider, model string) int {
	const neutral = 1_000_000
	scores := map[string]int{}
	if d != nil {
		for _, b := range d.Breakers {
			score := neutral
			if b.Rank != 0 {
				score = b.Rank
			}
			scores[b.Provider+"/"+b.Model] = score
		}
	}
	return func(provider, model string) int {
		if score, ok := scores[provider+"/"+model]; ok {
			return score
		}
		return neutral
	}
}
