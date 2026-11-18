package config

import (
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
)

func TestTierOverviewUsesCandidateBoundaries(t *testing.T) {
	rows := []tierView{{name: "heavy", steps: []resolve.Step{
		{Binding: domain.Binding{Provider: "smt-deepseek", Model: "deepseek-flash"}},
		{Binding: domain.Binding{Provider: "smt-claude", Model: "claude-opus-5"}},
		{Binding: domain.Binding{Provider: "smt-gemini", Model: "gemini-3.1-pro-preview"}},
	}}}

	got := tierOverview(rows)
	for _, binding := range []string{
		"smt-deepseek/deepseek-flash",
		"smt-claude/claude-opus-5",
		"smt-gemini/gemini-3.1-pro-preview",
	} {
		if !strings.Contains(got, binding) {
			t.Fatalf("tierOverview() split or lost %q:\n%s", binding, got)
		}
	}
	if strings.Contains(got, "…") {
		t.Fatalf("tierOverview() hid candidates: %q", got)
	}
}

func TestBindingChainKeepsIdentifiersWhole(t *testing.T) {
	steps := []resolve.Step{
		{Profile: "a", Binding: domain.Binding{Provider: "provider", Model: "model-one"}},
		{Profile: "b", Binding: domain.Binding{Provider: "provider", Model: "model-two"}},
	}
	got := bindingChain(steps, "  ")
	for _, binding := range []string{"provider/model-one", "provider/model-two"} {
		if !strings.Contains(got, binding) {
			t.Fatalf("bindingChain() split %q:\n%s", binding, got)
		}
	}

	detailed := numberedBindingChain(steps, nil)
	for _, binding := range []string{"provider/model-one", "provider/model-two"} {
		if !strings.Contains(detailed, binding) {
			t.Fatalf("numberedBindingChain() split %q:\n%s", binding, detailed)
		}
	}
}
