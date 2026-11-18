// Package controlpath 是网关控制面端点的路径常量。
//
// 为什么单开一个叶子包（2026-09-18）：这些端点是 **gateway 的 HTTP 服务器注册的**
// （modules/gateway/forward 的 mux），而调用它们的是 CLI——`newgate status/metrics`
// 要隔着 HTTP 去问跑着的守护进程。两边各写一遍字符串字面量，「改路径时只改了一边」
// 就是个静默失效：服务器换了路径，客户端还在打老的那个，症状是「功能突然没了」，
// 而不是编译错误。
//
// 它必须是**叶子**：不能放在 gateway 根包里。理由是 **import 重量**——cli、
// config、breaker、runtime 以及 gateway 自己的 controlplane 都要引这几个路径
// 常量，放在根包里等于每个引用者都拖进整个网关实现。
//
// （2026-09-18 修正这条理由两次：先是「gateway Need(cli)，所以 cli 反过来
// import gateway 会成环」——那个环在 cli 的出边砍空之后就不存在了；后来 gateway
// 对 ui 用的是 Inject 这条不排序的边。现在那条边改回了 Optional(cli)（弱依赖，
// 见 component.Optional），仍不成环：方向是 gateway → ui 一条，cli 没有任何出边，
// 而 Go 层面 gateway 引的是 cli/extension 叶子、不是 modules/cli。结论始终没变，
// 变的只是理由。）
//
// 同一条规矩已经有一个先例：**配置共享**（发行版的功能，见 §1 的注）把
// `/__newgate/config` `/__newgate/secrets` 定义在它自己的叶子里。
//
// 与那两个的关系：同一段 `/__newgate/` 命名空间，但**不是同一台服务器**
// ——配置共享的端点由它自己起，这里的五个由数据面起。所以常量各归各家，不合并。
package controlpath

const (
	// Status 守护进程状态文档：pid、端口、请求数、熔断表、推理缓存计数。
	// `newgate status` 与所有「读 daemon 内存」的命令走它。
	Status = "/__newgate/status"
	// Metrics 计数器快照（内存，随重启归零）。
	Metrics = "/__newgate/metrics"
	// Health 主动探活结果回传口：CLI 在本进程跑完 probe，把结论交给 daemon 记账。
	Health = "/__newgate/health"
	// Stop 跨用户停机口。带 Bearer 令牌，是「进程没在跑」时唯一还能停下来的路。
	Stop = "/__newgate/stop"
	// Upgrade 优雅交接（socket fd 移交）。旧版 daemon 没注册它，所以调用方
	// 必须先探能力再动手——盲发会被当成未知路径。
	Upgrade = "/__newgate/upgrade"
)
