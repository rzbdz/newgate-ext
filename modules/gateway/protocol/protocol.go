// Package protocol 处理 API 方言与模型名规范化。
package protocol

import "strings"

// OneMMarker Claude Code 用 [1m] 后缀声明 100 万上下文能力。
// 那是**客户端侧的能力标记**，不属于上游模型 id，转发前必须剥掉。
const OneMMarker = "[1m]"

// NormalizeRole 从客户端发来的 model 字段里取出档位名。
//
//	"newgate/heavy"  → "heavy"    剥掉虚拟 provider 前缀
//	"heavy[1m]"      → "heavy"    剥掉客户端能力标记
func NormalizeRole(model string) string {
	r := strings.TrimSpace(model)
	if i := strings.LastIndex(r, "/"); i >= 0 {
		r = r[i+1:]
	}
	if strings.HasSuffix(strings.ToLower(r), OneMMarker) {
		r = strings.TrimSpace(r[:len(r)-len(OneMMarker)])
	}
	return r
}
