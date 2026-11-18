// Package glm owns the identity decision for the GLM model family.
//
// It needs no unilateral protocol patch today, so the component provides only the Model
// capability. Behavior that needs to know both Claude Code and GLM is composed by
// claudecode_glm, avoiding building an empty plugin for a future possibility or stuffing
// client knowledge into the model module.
package glm

import (
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// New declares a pure model-identity component. It has no lifecycle side effects;
// it only provides the GLM target decision capability to components that need to compose
// client behavior.
func New() modules.Component {
	return modules.Component{
		Name: "glm",
		Desc: func() string { return i18n.T("GLM upstream quirk patches", nil) },
		Type: "model",
		Provides: []modules.Provision{
			modules.Provide(Capability, Model{
				MatchTarget: MatchTarget,
			}),
		},
	}
}
