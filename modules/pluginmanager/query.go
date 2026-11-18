package pluginmanager

import (
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
)

// rawState 取 state.json 里本模块那一段原始字节。state 为 nil（测试里常见）
// 或字段缺失都返回 nil，Parse 会把它当成「什么都没设过」。
func rawState(st *domain.State) []byte {
	if st == nil {
		return nil
	}
	return st.ModuleConfig[StateKey]
}

// Off 报告这条开关点是否被显式关掉。**默认 false**：用户没动过 = 没关。
// 用在 Default=true 的开关点上（出厂开、用户能关的 kill switch）。
//
// 纯函数是硬规矩，不是风格：它跑在转发热路径上、每个请求都要调，所以**只读传进来
// 的那份配置快照**——无锁、无 IO、不查注册表、不开文件。
//
// 「不查注册表」不只是性能：注册表是进程全局的，查它等于把热路径绑死在装配顺序上，
// 而且要在每请求路径上加一把读锁。这条纯读法还留着一个缝——将来要让共享配置层
// 统一治理车队（共享配置的两层模型：共享层 overlay + 机器本地层，本地优先），
// 做法是共享层提供 overlay、由 config 合并进快照，**这两个函数一个字都不用改**。
// 谁把它们写成「自己开文件读」，这条缝就焊死了。
func Off(st *domain.State, path string) bool {
	on, _ := lookup(Parse(rawState(st)).Off, path)
	return on
}

// On 报告这条开关点是否被显式打开。**默认 false**：用户没动过 = 没开。
// 用在 Default=false 的开关点上（出厂关、用户显式打开的模式，如 gateway.passthrough）。
//
// 纯度要求与 Off 完全相同，理由见上。
func On(st *domain.State, path string) bool {
	on, _ := lookup(Parse(rawState(st)).On, path)
	return on
}

// Remaining 这条设定还有多久失效。零值 = 不限时，或者根本没设过。
//
// 给 CLI 与 status 显示用（「还有 42s 自动关」）。它不在热路径上，但仍走同一个
// 解析入口——2026-09 那次教训是「过期判断分叉」：模块里有过一份自己写的过期检查，
// 与解析器里的不一致，于是同一份配置在两处结论不同。所以这里也复用 lookup。
func Remaining(st *domain.State, path string) time.Time {
	cfg := Parse(rawState(st))
	if on, until := lookup(cfg.Off, path); on {
		return until
	}
	if on, until := lookup(cfg.On, path); on {
		return until
	}
	return time.Time{}
}
