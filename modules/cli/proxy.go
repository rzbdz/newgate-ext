package cli

import (
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
)

// proxyState 代理的进程信息 + 自报状态。
//
// 文档形状与客户端都在 modules/gateway/controlplane（共享叶子）：读守护进程的
// 自报状态不是界面独有的能力，任何模块都能读——那正是 metrics / breaker / probe
// 那些命令搬回各自模块的前提。
//
// 这里**只留转发**：界面不解析那份文档里的任何字段（熔断表怎么排序、哪些 binding
// 被摘牌，都是 breaker 的知识，见 controlplane.Doc 上的 Available / Rank）。界面
// 用它只是为了回答「守护进程在不在、pid 是多少」这类进程问题。
func proxyState() (*controlplane.Info, *controlplane.Doc) { return controlplane.State() }

// notifyProxy 让运行中的代理立刻重读配置（转发到控制面叶子）。
//
// 为什么这一步归界面：命令改完配置要**立刻**生效，而界面是发起改动的那一方
// （见 Host.NotifyProxy）。
func notifyProxy() { controlplane.Notify() }
