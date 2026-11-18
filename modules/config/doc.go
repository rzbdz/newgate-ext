// Package config 回答一个问题：用户写下的语义档位，最终对应哪些上游模型。
//
// 它按“事实 → 规则 → IO”分层：
//   - domain 定义 Provider、Profile、Binding 和 State，这些值不读文件；
//   - resolve 把 profile、档位和健康状态组合成有顺序的候选链；
//   - store/paths 负责从磁盘取得事实并原子保存；
//   - roleprov 接收其他模块贡献的动态档位。
//
// 顶层 Config capability 只开放“注册动态档位”这一条写入口。网关消费已经解析
// 好的配置快照，客户端环境注入则属于 runtime；这样配置模块不会逐渐变成知道
// HTTP、进程和所有插件的中心对象。
package config
