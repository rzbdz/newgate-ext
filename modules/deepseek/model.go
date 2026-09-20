// Package deepseek owns behavior intrinsic to the DeepSeek model family.
// It does not know which client issued a request.
package deepseek

import "strings"

func MatchTarget(model, provider, baseURL string) bool {
	if strings.Contains(strings.ToLower(baseURL), "api.anthropic.com") {
		return false
	}
	for _, value := range []string{model, provider, baseURL} {
		if strings.Contains(strings.ToLower(value), "deepseek") {
			return true
		}
	}
	return false
}
