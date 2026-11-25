package forward

import (
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
)

// pinnedProfileFor must never pin a chain to a profile that is not in the snapshot.
//
// The live failure (2026-11-25): with fallback off, a broken default profile pinned every chain
// to a name that did not exist, so every request answered "no usable candidate" -- one unparseable
// file took down every agent.
func TestPinnedProfileForMissingProfile(t *testing.T) {
	st := &domain.State{
		DefaultProfile: "claude",
		ModuleConfig:   map[string][]byte{gatewaystate.Key: []byte(`{"fallback":false}`)},
	}
	st.Normalize()
	profiles := []*domain.Profile{{Name: "ds", Roles: map[string]domain.Candidates{}}}

	// fallback off is the interesting case: pinned == chain head.
	if !gatewaystate.FallbackOff(st) {
		t.Fatalf("test setup: fallback must be off")
	}

	if got := pinnedProfileFor(Target{}, st, profiles); got != "" {
		t.Fatalf("pinned to a profile that is not there: %q", got)
	}
	// When the profile does exist, pinning stays exactly as before.
	profiles = append(profiles, &domain.Profile{Name: "claude"})
	if got := pinnedProfileFor(Target{}, st, profiles); got != "claude" {
		t.Fatalf("pinning to an existing profile must be kept, got %q", got)
	}
	// An explicit /p/<name> is the user's own choice and is not second-guessed here.
	if got := pinnedProfileFor(Target{Profile: "gone"}, st, profiles); got != "gone" {
		t.Fatalf("explicit naming must pass through, got %q", got)
	}
}
