package proto

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MetaFileName 是版本/schema 声明的托管文件名。它是托管集合的一员（会被搬到
// 每台副本上），所以副本自己也留着一份"这份配置是按哪个 schema 写的"。
const MetaFileName = "newgate-config.json"

// Meta 是 newgate-config.json 的内容。
//
// 它存在的理由：宿主与副本的 newgate 二进制**不会同时升级**（副本可能几周不
// 重启），而配置格式会变。这份声明让副本在应用之前就能问自己一句"我解释得了
// 这份东西吗"。
type Meta struct {
	// SchemaVersion 配置格式版本，小整数、手工 bump。
	SchemaVersion int `json:"schema_version"`

	// MinNewgateVersion 期望的最低构建（`git describe` 串）。
	//
	// 它**只驱动警告**，永远不阻断——理由见 Gate。
	MinNewgateVersion string `json:"min_newgate_version,omitempty"`

	UpdatedAt string `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"` // 哪台宿主/哪个人发布的
}

// GateInput 是判定所需的两个外部事实。
type GateInput struct {
	// CurrentVersion 本二进制的构建串（cli.BuildInfo.Version）。
	CurrentVersion string
}

// Gate 是版本/schema 的两级门。返回：
//
//	ok     —— 能否应用这份配置
//	warn   —— 空串表示没话说；非空只出现在三处（daemon 日志、config status/
//	          doctor、Diagnostics），绝不进请求路径，见下面的"明令禁止"
//	refuse —— 拒绝应用的原因（此时 ok=false，调用方保留上一份配置继续服务）
//
// # 为什么两级的行为性质不同，而不是"同一件事的两个严重度"
//
// **min_newgate_version 不匹配 ⇒ 放行 + 警告。** 这一份配置本二进制**今天
// 就解析得了**，只是按一个更新的构建写的：未知字段要么被 store 容忍（它
// 保留不认识的顶层键），要么缺席走默认值。硬拒绝会让一台还能用的机器收不到
// provider 更新——把一个装饰性的不匹配升级成事故。而且这个串是
// `git describe` 产物，`build-17-…` 与 `build-18-…` **没有数值大小关系**
// （`--dirty` 更糟），只能判相等，判相等就更没资格阻断。
//
// **schema_version 更高 ⇒ 拒绝应用 + 保留上一份。** 这时失败的不是"少个
// 功能"，而是"**字段含义变了**"——本二进制无法正确解释这份内容，而且误读
// 不会响亮报错。本仓库恰好发生过这类变更（normal→mid 的回退、三档扩四档、
// anthropic_url 拆方言）。误读的现场长这样：路由到错的上游，或者报
// "provider 没有 api_key" 然后 404——**看起来像上游故障**。拒绝严格更安全，
// 而且仍然 fail-open：机器继续用上一份配置服务。
//
// # 明令禁止（写在这里免得以后有人"顺手加上"）
//
// 警告**不许**实现成 RegisterRequestHook / special 插件（那些跑在请求热路径
// 里、用 notes 汇报——notes 是会送到用户面前的通道）；**不许**加任何响应头
// （`X-Newgate-*` 是 agent CLI 会读的）；**不许**从 wrapper/launch 路径打印
// （那在 agent 自己的启动路径里）。这三条是 SSE 保证：一次 schema 拒绝绝不该
// 在用户的会话流里冒出一个字。
func Gate(meta *Meta, in GateInput) (ok bool, warn, refuse string) {
	if meta == nil {
		// 宿主没放 newgate-config.json（老版本宿主，或 ser 手写的配置目录）。
		// 按当前 schema 解释——这与"schema 更高"不同，后者是**已知的解释不了**。
		return true, "快照里没有 " + MetaFileName + "（对方没声明 schema），按当前 schema 解释", ""
	}
	if meta.SchemaVersion > SupportedSchema {
		return false, "", fmt.Sprintf(
			"这份配置的 schema_version=%d，本二进制只认到 %d：字段含义可能已经变了，误读不会报错（会路由到错的上游或报 provider 没有 api_key）。已保留上一份配置继续服务。升级 newgate 后会自动跟上。",
			meta.SchemaVersion, SupportedSchema)
	}
	if meta.SchemaVersion <= 0 {
		return true, MetaFileName + " 没写 schema_version，按当前 schema 解释", ""
	}
	// 构建串相等才算匹配。dev 构建（没注入版本）跳过——否则每个开发机上都是
	// 一句噪音。
	if want := strings.TrimSpace(meta.MinNewgateVersion); want != "" {
		got := strings.TrimSpace(in.CurrentVersion)
		if got != "" && got != "dev" && got != want {
			return true, fmt.Sprintf(
				"宿主声明的构建是 %s，本机是 %s：本机仍能解析这份配置（未知字段忽略/缺席走默认），已放行。要完全对齐就升级 newgate。",
				want, got), ""
		}
	}
	return true, "", ""
}

// ParseMeta 从快照里取出 Meta；文件缺席返回 (nil, nil)，内容坏掉返回错误。
//
// 内容坏掉（有文件但不是 JSON）**不是**"没声明"——那是宿主写坏了，得让
// 调用方拒绝，不能悄悄按当前 schema 解释。
func ParseMeta(files map[string][]byte) (*Meta, error) {
	raw, ok := files[MetaFileName]
	if !ok {
		return nil, nil
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("%s 解析失败: %w", MetaFileName, err)
	}
	return &meta, nil
}
