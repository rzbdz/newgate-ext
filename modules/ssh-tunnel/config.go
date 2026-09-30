// Package sshtunnel 把远端机器上的 newgate webui 带到本机浏览器跟前。
//
// # 它解决的那件事
//
// 内网里常有一类机器：只开 SSH，别的端口一律不出。newgate 在那台机器上照常跑着
// （webui 在 8899），但本机的浏览器够不到它。这个模块给出的答案是**穿过 SSH**：
//
//	本地转发端口（保真）   newgate tunnel up snode1 → http://127.0.0.1:9401/ui/
//	共享端口上的 route     http://127.0.0.1:8899/ui/remote/snode1/   （尽力而为）
//
// 两条路是**两种产品取舍**，不是「一种做法的两个入口」：
//
//   - 转发端口把一条 TCP 通道原样送到远端，路径一个字节都不动——远端界面完全保真，
//     代价是本机多占一个端口（而「只开 8899」正是这类环境的约束，所以它不能是唯一
//     出路）。
//   - route 不占端口，但**必须在响应里改写路径**：远端界面的绝对前缀是构建期烘死
//     的（vite 的 base "/ui/" 出现在 index.html 的 src、CSS 的 url()、以及 JS 里
//     import.meta.env.BASE_URL 折叠出来的字符串里）。不改写就白屏。这条路**尽力
//     而为**，README 与 route 首页都这么说——把「等价」当成结论写出去，用户第一次
//     撞上某个没改到的地址就会觉得是我们骗人。
//
// # 为什么是一个模块
//
// 它要的正是 porthub 提供的那件事（共享端口上的一个前缀），并且它自己也贡献控制面
// （`newgate tunnel` 与 web 界面上那一节）。做成模块之后它跟别的服务一样被装配、
// 被摘除、被 `newgate plugin` 列出来；不装这份规格书它就整个消失。
//
// # 凭据的事说在最前面
//
// 认证**只认密钥文件与 ssh-agent**——不做密码、不做 keyboard-interactive、不做跳板机。
// 直接后果是：**这个模块的配置里没有任何秘密**，于是它可以整份住在 state.json
// （0660、web 界面能编辑）。`identity` 存的是**路径**，`host_key` 存的是**策略**，
// 两者都不是凭据。密码做不到这一点：它要么落在那个谁都能读的文件里，要么另开一个
// 只有命令行能碰的存储——而后者会把「web 界面能配」这件事砍掉一半。取舍写在这里，
// 因为它决定了后面每一个字段的形状。
package sshtunnel

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
)

// StateKey 是 state.json 的 ModuleConfig 里属于本模块的那一格。
//
// 与目录名同名（web-dashboard / locale 都是这个约定）。对不上的症状很安静：
// 读到的永远是空配置，保存了、界面上也对，**重启就没了**。
const StateKey = "ssh-tunnel"

// HostKey 的取值（见 Target.HostKey）。
const (
	// HostKeyStrict：known_hosts 里没有这台机器就拒连（默认）。
	HostKeyStrict = "strict"
	// HostKeyAcceptNew：第一次见到就记下来，之后按 StrictHostKeyChecking=accept-new
	// 的语义走（变了仍然拒）。每次真的记了一条都会打日志——「信任这件机器」是一个
	// 用户该知道的事件，不是一次静默的内部状态变化。
	HostKeyAcceptNew = "accept-new"
)

// 默认值，集中在一处。散在 normalize 与视图里会让「界面上显示的值」与「实际用的值」
// 有两份真相，而两者不一致的症状是「我看着是 8899，它连的却是别的」。
const (
	DefaultSSHPort    = 22
	DefaultRemoteHost = "127.0.0.1"
	DefaultRemotePort = 8899 // = domain.ProxyPort，远端 newgate 的默认端口
	DefaultIdleSecs   = 300
)

// Target 是一个远端 webui 的坐标，外加怎么过去。
//
// 字段全部可选（除 ID/Host），缺省在 normalize 里补齐——**归一化只发生一次**，
// 在读到配置的那一刻：视图、CLI、拨号三方读到的都是归一化之后的那份，于是
// 「界面显示的就是实际用的」不需要靠三处各写一遍默认值来保证。
type Target struct {
	// ID 是机器标记：state.json 里的身份、URL 里的路径段（/ui/remote/<id>/）、
	// 以及行动作回传的值。**不翻译**，而且不许含 `/`（它是路径段）或空白。
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`

	// 怎么过去。
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	Identity string `json:"identity,omitempty"` // 私钥**路径**；空 = 默认那几把 + agent

	// 过去之后看什么。从**远端那台机器**的视角：remote_host 默认 127.0.0.1
	// （远端的 webui 就监听在那儿），remote_port 默认 8899。
	RemoteHost string `json:"remote_host,omitempty"`
	RemotePort int    `json:"remote_port,omitempty"`

	// LocalPort 是要在本机占的那个端口（路线一）。0 = 不占，只走 route。
	LocalPort int `json:"local_port,omitempty"`

	// Persistent 为 true 时 daemon 一起来就连、断了退避重连；false（默认）是懒的：
	// 第一个请求才拨号，空闲 idle_seconds 之后断开。
	Persistent  bool `json:"persistent,omitempty"`
	IdleSeconds int  `json:"idle_seconds,omitempty"`

	HostKey string `json:"host_key,omitempty"`
}

// normalize 把一份**可能什么都缺**的配置补成一份可以直接用的。
//
// 不改 ID（它是身份，空的留给校验去拒）。返回值是拷贝，不改入参——视图那边会拿着
// 原始结构体反复算，就地改它会造出「读一次变一次」的假象。
func (t Target) normalize() Target {
	if t.Label == "" {
		t.Label = t.ID
	}
	if t.Port == 0 {
		t.Port = DefaultSSHPort
	}
	if t.RemoteHost == "" {
		t.RemoteHost = DefaultRemoteHost
	}
	if t.RemotePort == 0 {
		t.RemotePort = DefaultRemotePort
	}
	if t.IdleSeconds == 0 {
		t.IdleSeconds = DefaultIdleSecs
	}
	if t.HostKey == "" {
		t.HostKey = HostKeyStrict
	}
	return t
}

// RemoteAddr 是拨号的目标（从远端的视角看的那个地址）。
func (t Target) RemoteAddr() string {
	return fmt.Sprintf("%s:%d", t.RemoteHost, t.RemotePort)
}

// DialAddr 是 SSH 服务端的地址。
func (t Target) DialAddr() string {
	return fmt.Sprintf("%s:%d", t.Host, t.Port)
}

// Problem 是一条被拒掉的配置，连同原因。
//
// 为什么要留着它而不是只留日志：拒绝发生在**读配置**的时候（每次快照都读一遍），
// 而用户唯一能看到它的地方就是界面上那张表与 `newgate tunnel ls`。只写日志等于
// 「这条配置坏了」这件事只对会翻日志的人可见。
type Problem struct {
	ID   string
	Why  string
	Line string // 界面/终端上那一行原文
}

// Loaded 是一次读配置的全部结论。
type Loaded struct {
	Targets  []Target  // 合法且已归一化的，按 ID 排序（顺序稳定，界面才不跳）
	Rejected []Problem // 不合法的：**跳过，但不咽下去**
	// Base 是 state.json 此刻的修订哈希（store.Revision），可写的记录集要用它做
	// CAS——没有基线就无从判断「我手里这份是不是最新的」，而那正是「命令行刚改过
	// 同一个文件」的唯一防线。
	Base string
}

// Load 读这份配置。
//
// fail-open：读不出来（文件坏了、JSON 坏了）得到的是一个**空表**，不是错误——
// 与 web-dashboard 读皮肤选择同一条：一个偏好读不出来不该让整个界面打不开。
// 坏掉的单条进入 Rejected，界面与 CLI 都报得出来。
func Load() Loaded {
	out := Loaded{Base: store.Revision(paths.StateFile())}
	raw := store.LoadState().ModuleConfig[StateKey]
	if len(raw) == 0 {
		return out
	}
	var st struct {
		Targets []Target `json:"targets"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		out.Rejected = append(out.Rejected, Problem{
			Why:  "the ssh-tunnel section of state.json is not readable JSON",
			Line: err.Error(),
		})
		return out
	}
	seen := map[string]bool{}
	localPorts := map[int]string{}
	for _, t := range st.Targets {
		// **先归一化，再校验。** 反过来会把「没写」当成「写了个 0」——而 0 在
		// 这个文件里处处意味着「用缺省值」（见 normalize）。这个顺序错过的症状
		// 是整个文件被一条不剩地判为非法：那种失败看起来像「配置没保存成功」，
		// 而不是像「校验太严」。
		n := t.normalize()
		if why := validate(n); why != "" {
			out.Rejected = append(out.Rejected, Problem{ID: t.ID, Why: why,
				Line: fmt.Sprintf("%s: %s", orDash(t.ID), why)})
			continue
		}
		if seen[n.ID] {
			out.Rejected = append(out.Rejected, Problem{ID: n.ID,
				Why:  "two targets share this id",
				Line: fmt.Sprintf("%s: two targets share this id", n.ID)})
			continue
		}
		if n.LocalPort != 0 {
			if other, dup := localPorts[n.LocalPort]; dup {
				out.Rejected = append(out.Rejected, Problem{ID: n.ID,
					Why:  fmt.Sprintf("local port %d is already taken by %s", n.LocalPort, other),
					Line: fmt.Sprintf("%s: local port %d is already taken by %s", n.ID, n.LocalPort, other)})
				continue
			}
			localPorts[n.LocalPort] = n.ID
		}
		seen[n.ID] = true
		out.Targets = append(out.Targets, n)
	}
	sort.Slice(out.Targets, func(i, j int) bool { return out.Targets[i].ID < out.Targets[j].ID })
	return out
}

// validate 说这条配置为什么不能用（空串 = 能用）。
//
// 判据写在字段旁边而不是集中成一个正则：每一条都是**具体某个字段的**约束，读的人
// 该在改字段时看到它。
func validate(t Target) string {
	switch {
	case t.ID == "":
		return "id is required"
	case strings.ContainsAny(t.ID, "/?#") || strings.ContainsAny(t.ID, " \t"):
		// `/` 会把它变成两个路径段（/ui/remote/a/b/ 指向谁就不确定了），`?` 与 `#`
		// 会被浏览器的 URL 解析吃掉，空白让地址没法复制粘贴。
		return "id must not contain / ? # or whitespace — it is a URL path segment"
	case len(t.ID) > 64:
		return "id is longer than 64 characters"
	case t.Host == "":
		return "host is required"
	case t.Port < 1 || t.Port > 65535:
		return fmt.Sprintf("ssh port %d is out of range", t.Port)
	case t.RemotePort < 1 || t.RemotePort > 65535:
		return fmt.Sprintf("remote port %d is out of range", t.RemotePort)
	case t.LocalPort < 0 || t.LocalPort > 65535:
		return fmt.Sprintf("local port %d is out of range", t.LocalPort)
	case t.IdleSeconds < 0:
		return "idle_seconds must not be negative"
	case t.HostKey != "" && t.HostKey != HostKeyStrict && t.HostKey != HostKeyAcceptNew:
		return fmt.Sprintf("host_key %q is neither %q nor %q", t.HostKey, HostKeyStrict, HostKeyAcceptNew)
	}
	return ""
}

// save 把整份 target 列表写回去（**整份替换**，不是逐条改）。
//
// 为什么整份：这个文件是**人的配置**，不是一张会被并发增量更新的表。逐条改要处理
// 「两个人同时改不同的两条」的合并，而合并的代价在别处已经付过了（配置卡那条 CAS）。
// 整份写 + 基线判断就够：谁的基线旧了谁重读。
func save(targets []Target, base string) (string, error) {
	// 归一化之后再落盘：写进去的就是实际会用的那几个值。不归一化的话，文件里
	// 会留着一堆「没写」的空字段，而**读回来时补的缺省值**才是真相——两份真相
	// 迟早对不上（改了缺省值之后，老文件的行为会跟着变，而它看着没被改过）。
	norm := make([]Target, 0, len(targets))
	for _, t := range targets {
		norm = append(norm, t.normalize())
	}
	raw, err := json.Marshal(struct {
		Targets []Target `json:"targets"`
	}{Targets: norm})
	if err != nil {
		return "", err
	}
	st := store.LoadState()
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	st.ModuleConfig[StateKey] = raw

	if base == "" {
		// 没有基线（调用方手里那份是刚构造的，没跟盘上对过）：直接写。
		return "", store.SaveState(st)
	}
	data, err := store.StateBytes(st)
	if err != nil {
		return "", err
	}
	return store.WriteIfUnchanged(paths.StateFile(), base, data)
}

// Save 是给 CLI 用的写入口：读—改—写，没有基线就现取一个。
//
// 为什么 CLI 也要走 CAS：命令行与 web 界面可能同时改这份文件（一个人在终端敲
// `tunnel add`，另一个人在浏览器里拖字段）。不判断基线的话后写的那次会**静默**
// 盖掉前一次——而这两次改动可能毫不相干。
func Save(targets []Target) error {
	_, err := save(targets, store.Revision(paths.StateFile()))
	return err
}
