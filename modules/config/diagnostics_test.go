package config

import (
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// 锁住 2026-10-28 这一条：per-agent 的「单独定过」判据是 state.Active 里有没有
// 记录，不是「跟全局缺省一不一样」。后者会把「明确给 claude 选了 ds、而缺省正好
// 也是 ds」藏起来——用户以后改全局缺省会以为 claude 跟着走，而它不会。
func TestStatusShowsExplicitOverrideEvenWhenItEqualsDefault(t *testing.T) {
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProfile(&domain.Profile{Name: "ds", Roles: map[string]domain.Candidates{
		"normal": {{Provider: "relay", Model: "model-x"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(&domain.State{
		DefaultProfile: "ds",
		Port:           8899,
		Active:         map[string]string{"claude": "ds"},
	}); err != nil {
		t.Fatal(err)
	}

	lines := reporter{}.Status()
	if len(lines) != 1 {
		t.Fatalf("status lines = %d, want 1", len(lines))
	}
	if !strings.Contains(lines[0].Value, "claude → ") {
		t.Fatalf("status value %q does not show the explicit claude override", lines[0].Value)
	}

	// 没有记录时不能凭空把「跟随缺省」说成「单独定过」。
	if err := store.ClearActiveProfile("claude"); err != nil {
		t.Fatal(err)
	}
	if got := (reporter{}).Status()[0].Value; strings.Contains(got, "claude → ") {
		t.Fatalf("status value %q shows an override that is not in state.Active", got)
	}
}
