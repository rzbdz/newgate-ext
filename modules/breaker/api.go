// Package breaker 拥有「这条 binding 现在能不能用」这件事的全部判断。
//
// 它从一个 gateway 内部包提升为顶级模块（2026-09-17），因为读它的地方比写它的
// 地方多：数据面（gateway/forward）在建链期问它、控制端点回报探活结论、CLI 把
// 它渲染成 `newgate breaker` / `newgate metrics` / `newgate tier`。放在 gateway
// 里的时候，这些消费者只能通过进程级单例 `health.Default` 摸到它——一个全局
// 可变量，测试之间互相污染，也没法在装配时换掉。现在它经 capability 注入，
// 谁需要谁声明。
//
// 本包**不依赖任何业务模块**：它只知道 binding 键（provider/model）、状态码和
// 延迟。上游专有的知识（什么响应体算「请求形状错误」）由上游模块通过
// RegisterShapeDetector 贡献进来，见 detectors.go。
package breaker

import (
	"time"

	"github.com/rzbdz/newgate-ext/modules/breaker/status"
	modules "github.com/rzbdz/newgate/component"
)

// ProbeGrade / Status 的事实定义在 modules/breaker/status（wire 契约叶子）。
// 这里做别名转发，本包内的代码与老调用方继续用短名字。
type (
	ProbeGrade = status.ProbeGrade
	Status     = status.Status
)

const (
	ProbeFluent      = status.ProbeFluent
	ProbeUsable      = status.ProbeUsable
	ProbeLaggy       = status.ProbeLaggy
	ProbeUnavailable = status.ProbeUnavailable
)

// Breaker 是健康表端口。数据面只读前两项、只写 Report/ObserveSuccess；
// 渲染层只读 Snapshot。
//
// # 这个接口为什么是「读多写少」（2026-09-21 收窄）
//
// 它曾经在上面挂着三个 setter：`SetPolicy` / `SetErrorHandler` / `SetVerifier`。
// 三个都是**无主的写门**——任何拿到健康表 capability 的模块都能调，而全仓库的
// 调用点**一个都不在接口上**（都在下面的 BindEnv 与测试里，作用于具体的 *table）。
// 也就是说：接口开得比所有真实用法都大，多出来的部分是一扇谁都能推的门。
//
// 多出来的代价不是假想的。`SetVerifier` 是上闸前那道安全诊断的开关，一句
// `SetVerifier(nil)` 就能把它**静默**卸掉——不报错、依赖图上没有边、code review
// 也看不出有人碰过它。而 `SetPolicy` 更干脆：它**零调用者**，是为一个还没做的
// 功能（把策略挂到 state.json）预留的 API。
//
// 判据是「一个能力只该有一扇门，门开在拥有它的模块上」：
//   - 数据面借给策略的运行期能力 → `policy.EnvBinder.BindEnv`（那条缝有主、
//     有向、时机明确），健康表在 BindEnv 里把日志出口与探针接回来；
//   - 策略本身 → 今天恒为 DefaultPolicy，将来要可配时再开一扇**有主**的门，
//     而不是现在留一个谁都能改的 setter。
//
// 那三个方法照旧在（都是 *table 上的），只是不再从端口上露出来。
type Breaker interface {
	// Available 回答这个 binding 现在能不能进候选链。建链期调用。
	//
	// 它有副作用：冷却期满时会把 binding 推进半开，并发放这一轮的试探名额。
	// 建链期每个候选只问一次，所以「放行一次真实请求」在这里天然成立。
	Available(provider, model string) bool
	// Rank 是建链期的排序键：预测首字节延迟，越小越靠前。
	Rank(provider, model string, contextBytes int) int

	// Report 回报一次上游交互的结局，返回判决（要不要沿链走）。
	// 分类与状态迁移都在实现里，数据面只如实描述发生了什么。
	Report(provider, model string, in Input) Result
	// ObserveSuccess 只记延迟样本，不动可用性（首字节已经拿到之后调用）。
	ObserveSuccess(provider, model string, contextBytes int, ttft time.Duration)
	// RecordProbe 记录一次主动探活的结论，返回评级与是否仍然熔断。
	RecordProbe(provider, model string, status, contextBytes int,
		latency, slowAfter time.Duration, probeErr string) (ProbeGrade, bool)

	// Snapshot 冻结一份可序列化的现状。
	Snapshot() []Status

	// Flush 把节流窗口内尚未落盘的延迟样本同步写出（优雅退出用）。
	Flush()
	// UseFile 让健康表跨优雅重启存活。
	//
	// 它与上面那三个 setter **不是**一类，所以留在端口上：它开的是「状态存哪儿」，
	// 不是「借你一个能力」——没有它健康表只是不落盘，行为可预期，也没有第二个人
	// 会来改它。收窄那一刀只砍无主的**能力**注入。
	UseFile(path string) error

	// RegisterShapeDetector 贡献一条「这个 4xx 是请求形状问题」的判据。
	//
	// 形状错误的定义**属于上游自己**（见 shape.go 的 ShapeDetector）：core 只
	// 提供端口和「永不摘牌、只计数」的策略，判据由知道那家校验规则的模块注册
	// 进来。没有注册任何检测器时，400 一律不记在任何人头上——少认一次只是少
	// 一个计数，误认一次会让真正的可用性故障被放过。
	//
	// 返回只属于本次注册的撤销句柄；consumer 在 Stop 里逆序释放。
	RegisterShapeDetector(ShapeDetector) (modules.Release, error)
}

// Capability 是健康表在组件图里的身份。
var Capability = modules.NewCapability[Breaker]("breaker")

// NewTable 造一份纯内存健康表（不落盘）。测试与模块装配都用它。
func NewTable() Breaker { return newTable() }
