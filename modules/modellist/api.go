package modellist

import (
	"context"
	"time"

	modules "github.com/rzbdz/newgate/component"
)

// Name 是这个模块在装配表与 `newgate plugin` 里的名字（= 目录名）。
const Name = "modellist"

// Endpoint 是它登记到 web BFF 上的名字：`POST /ui/api/ext/modellist`。
//
// 机器标记，不翻译——前端 `api.ts` 的 `modelList()` 用的就是这一条。
const Endpoint = "modellist"

// Model 是上游列出的一个模型。
//
// ID 是**上游认的那个名字**（绑定里写的就是它）；Name 只是给人看的长名字
// （有的上游返回 `display_name`，有的没有）。Group 是上游自己给的分组——没有
// 就是空，界面按「一组」画。
type Model struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Group string `json:"group,omitempty"`
}

// Catalog 是一次「问上游要模型清单」的回答。
//
// # Supported 为 false 不是错误
//
// 多数上游没有 `GET /models`，或者这个 key 不被允许列模型。那是一个**正常的
// 答案**：界面回落到 providers.json 里声明的清单，并把 Note 写在菜单顶上。
// 真正出错（providers.json 读不了、provider 名字不存在）才走 error 返回。
type Catalog struct {
	Provider string `json:"provider"`
	// Supported = 上游真的给了清单（Models 非空）。
	Supported bool `json:"supported"`
	// Adapter 是命中的适配器（`openai` / `anthropic`）。机器标记，不翻译。
	Adapter string `json:"adapter,omitempty"`
	// Endpoint 是实际问的地址（**不含凭据**：key 只在请求头里）。
	Endpoint string `json:"endpoint,omitempty"`
	// Models 为空数组而不是 nil：前端不必为「没有」与「字段缺失」写两条分支。
	Models []Model `json:"models"`
	// Groups 是出现过的分组（按第一次出现的顺序）。没有分组时省略。
	Groups []string `json:"groups,omitempty"`
	// FetchedAt 是这份清单是什么时候问到的（UTC）。
	FetchedAt time.Time `json:"fetched_at"`
	// Cached = 这一份来自进程内缓存（界面据此显示「缓存」）。
	Cached bool `json:"cached,omitempty"`
	// Note 是 Supported=false 时给人看的原因（已翻译）。
	Note string `json:"note,omitempty"`
}

// Options 是 List 的开关。
type Options struct {
	// Refresh 跳过缓存，重新问一次上游（下拉旁边的 ↻ 按钮）。
	Refresh bool
}

// Service 是别的模块（将来也许有 CLI）问模型清单的端口。
//
// 它**不认识 provider 的存储位置**：名字进来，清单出去。读 providers.json 的
// 知识（环境变量里的 key、两种方言的 base）全部留在实现里——与 config 的
// Config 端口同一条规矩。
type Service interface {
	List(ctx context.Context, provider string, opts Options) (*Catalog, error)
}

// Capability 是它在装配图上的名字。
var Capability = modules.NewCapability[Service]("modellist")
