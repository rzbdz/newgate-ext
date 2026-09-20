// Package glm 拥有 GLM 模型家族的身份判断。
//
// 它目前不需要单方协议修补，所以组件只提供 Model capability。需要同时了解
// Claude Code 和 GLM 的行为由 claudecode_glm 组合，避免为了未来可能性预建
// 空插件或把客户端知识塞进模型模块。
package glm

import (
	modules "github.com/rzbdz/newgate/component"
)

// New 声明纯模型身份组件。它没有生命周期副作用，
// 只把 GLM 目标判定能力提供给需要组合客户端行为的组件。
func New() modules.Component {
	return modules.Component{
		Name: "glm",
		Type: "model",
		Provides: []modules.Provision{
			modules.Provide(Capability, Model{
				MatchTarget: MatchTarget,
			}),
		},
	}
}
