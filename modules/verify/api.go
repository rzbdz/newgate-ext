// Package verify 用**模型指纹**回答一个配置界面一直回答不了的问题：
//
//	这条 binding 写的是 gpt-5.6-terra，上游此刻真的在用它吗？
//
// # 它是什么
//
// 指纹检测的做法不是看模型自报的名字（那当然会照着你写的名字回），而是让上游
// 做一件**只有模型自己才会做的事**：吐几百个 1–355 之间的整数。不同的模型在
// 选数上有各自稳定的统计特征，拿这些数字和参考库比一遍，就能排出一个「这更像
// 谁」的名单（见 https://lm.ikale.io/docs/sdk）。本模块把这件事接成 newgate 的
// 一条命令（`newgate verify`）与网页上的一屏（指纹）。
//
// # 为什么它住在发行版
//
// 指纹参考库与身份判断是**产品取舍**：内核的 probe 只回答「通不通、说哪种方言」，
// 那是机制；「这个上游是不是它自称的那家」取决于你信哪一份参考库、失配时怎么
// 报。内核不该认识 lm.ikale.io，也不该认识某一家模型目录服务。所以内核只多出
// 一个共享的「profile → 去重后的上游」名单（config/targets），probe 与 verify
// 共用；其余全部在这里。
//
// # 它不认识界面
//
// 与 modellist 同一条：界面在就把扩展端点登记上去、把这一节挂到概念账本上；
// 界面不在（`dist-simple-cli.json` 那种只有终端的装配）命令照常可用。箭头只有
// 「本模块 → 界面」一个方向。
package verify

import (
	"time"
)

// Name 是这个模块在装配表与 `newgate plugin` 里的名字（= 目录名）。
const Name = "verify"

// Endpoint 是它登记到 web BFF 上的名字：`POST /ui/api/ext/fingerprint`。
//
// 名字刻意**不等于模块名**：前端只认「有一个指纹端点」，不认「有一个 verify
// 模块」（见 web-dashboard 的扩展端点注释）。机器标记，不翻译。
const Endpoint = "fingerprint"

// Match 是指纹给出的模型名与 binding 上写的模型名之间的关系。
//
// 它是机器标记（前端按它上色、CLI 按它出警告），不翻译。人话在于前端/CLI
// 各自的词表里。
type Match string

const (
	// MatchOK 名字（去掉分隔符后）完全一致。
	MatchOK Match = "match"
	// MatchDated 一个是另一个加日期/快照后缀：同一个模型的另一版。
	MatchDated Match = "dated"
	// MatchFamily 同一个家族里的另一个模型（比如写 gpt-5.5、指纹说是 gpt-5.6）。
	MatchFamily Match = "family"
	// MatchMismatch 名字对不上，也不是同族。
	MatchMismatch Match = "mismatch"
	// MatchUnknown 这一轮没有可用的排名（样本失败、样本不够、不是模型输出）。
	MatchUnknown Match = "unknown"
	// MatchError 连参考库都没加载成功，或 provider 配置不可用。
	MatchError Match = "error"
)

// Warning 报告这个结论是不是**需要人看一眼**。
//
// `dated` 不算：同一个模型的快照版是正常现象，把它也报成警告会让真正的失配
// 淹在噪音里。
func (m Match) Warning() bool {
	switch m {
	case MatchFamily, MatchMismatch, MatchUnknown, MatchError:
		return true
	}
	return false
}

// Candidate 是参考库给出的一个候选（按分数从高到低）。
type Candidate struct {
	Model       string   `json:"model"`
	DisplayName string   `json:"display_name,omitempty"`
	Family      string   `json:"family,omitempty"`
	FamilyName  string   `json:"family_name,omitempty"`
	Score       float64  `json:"score"`
	Probability *float64 `json:"probability,omitempty"`
}

// Entry 是一条 binding 的指纹结论。它同时是**落盘缓存**的形状。
//
// 缓存的意义不是省 token（verify 是用户显式发起的，每次都该真打），而是**界面
// 上那一格**：用户打开模型下拉时看到的必须是「上一次验到的是谁」，不能因为这次
// 没点按钮就空白。
type Entry struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// API 是实际使用的请求格式（responses / chat_completions / messages）。
	API string `json:"api,omitempty"`
	// Predicted 是参考库排第一的模型 ID；空 = 这一轮没有排名。
	Predicted     string `json:"predicted,omitempty"`
	PredictedName string `json:"predicted_name,omitempty"`
	Family        string `json:"family,omitempty"`
	FamilyName    string `json:"family_name,omitempty"`
	// Decision 是参考库的裁决：scored / partial / unscorable / not_model。
	Decision string `json:"decision,omitempty"`
	// Probability 是闭集内第一名的置信度；partial 时为 null（JSON 里省略）。
	Probability *float64    `json:"probability,omitempty"`
	Match       Match       `json:"match"`
	UsedOutputs int         `json:"used_outputs,omitempty"`
	Results     []Candidate `json:"results,omitempty"`
	CheckedAt   time.Time   `json:"checked_at"`
	Build       string      `json:"build,omitempty"`
	Error       string      `json:"error,omitempty"`
	// CacheError 是「这一轮验成功了，但结论没能落盘」——不影响这次结论，
	// 但决定下一次界面还看不看得到它，所以必须说出来。
	CacheError string `json:"cache_error,omitempty"`
}

// RoleState 是配置界面上一行：某个 profile 的某个档位解析到了哪条 binding。
type RoleState struct {
	Role     string `json:"role"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Err      string `json:"error,omitempty"`
}

// ProfileState 是一份 profile 的全部档位。
type ProfileState struct {
	Name  string      `json:"name"`
	Roles []RoleState `json:"roles"`
}

// ProviderState 是上游选择器的素材。**不含凭据**：key 只在验证请求里用，
// 一个字节都不该出现在界面报文里。
type ProviderState struct {
	Name     string   `json:"name"`
	Protocol string   `json:"protocol,omitempty"`
	BaseURL  string   `json:"base_url,omitempty"`
	HasKey   bool     `json:"has_key"`
	Models   []string `json:"models,omitempty"`
}

// State 是这一屏的全部素材：配置视图 + 上一次的指纹 + 参考库状态。
type State struct {
	Profiles  []ProfileState  `json:"profiles"`
	Providers []ProviderState `json:"providers"`
	Entries   []Entry         `json:"entries"`
	// Build 是参考库的 built_at（数据版本）；空 = 还没加载过。
	Build string `json:"build,omitempty"`
	// Loaded 说的是这个进程有没有把参考库读进来（第一次 verify 会下载约
	// 3.6 MB 并缓存）。
	Loaded bool `json:"loaded"`
	// DataCached 说的是参考库的缓存文件在不在这台机器上。它解释「第一次验证
	// 会下载」——文件已经在了，就只是解析（几十毫秒），不必吓人。
	DataCached bool `json:"data_cached"`
	// DataCheckedAt 是参考库缓存最后一次被下载/检查的时间（缓存文件的 mtime）。
	// 界面用它说「尺子有多旧」；nil = 没有缓存。
	DataCheckedAt *time.Time `json:"data_checked_at,omitempty"`
	// Error 是读配置/缓存时的整体错误。页面照常打开（自由验证那半仍然可用），
	// 但要把原因写在面板上。
	Error string `json:"error,omitempty"`
}

// RunRequest 是一次单点验证的输入。
type RunRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// API 空/auto = 按 provider 的 protocol 选（anthropic → messages，
	// 其余 → chat_completions）。也可以显式指定。
	API string `json:"api,omitempty"`
	// Count 是挑战数（1–3）。0 = 3（参考库推荐值；3 条才有校准概率）。
	Count int `json:"count,omitempty"`
}
