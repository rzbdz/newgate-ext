// Package glm owns GLM model-family identification and model-only behavior.
package glm

import "strings"

func MatchTarget(model, provider, baseURL string) bool {
	if strings.Contains(strings.ToLower(baseURL), "api.anthropic.com") {
		return false
	}
	for _, value := range []string{model, provider, baseURL} {
		if strings.Contains(strings.ToLower(value), "glm") {
			return true
		}
	}
	return false
}
