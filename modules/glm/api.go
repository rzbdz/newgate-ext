package glm

import modules "github.com/rzbdz/newgate/component"

// Model is the outward-facing decision port for the GLM model family.
// Keep the matching logic inside the model's owner; client cross-cutting components only
// consume the conclusion.
type Model struct {
	MatchTarget func(model, provider, baseURL string) bool
}

// Capability identifies the one GLM model-family decider.
var Capability = modules.NewCapability[Model]("model-family.glm")
