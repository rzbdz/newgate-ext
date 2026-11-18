package confighook

import (
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
)

// Pin "every client is listed in status": followers appear too, set-apart ones marked.
// The previous version had only config's line and expanded an agent only on difference.
func TestStatusBlockListsEveryAgentAndSource(t *testing.T) {
	t.Setenv("NEWGATE_HOME", t.TempDir())
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(&domain.State{
		DefaultProfile: "ds",
		Port:           8899,
		Active:         map[string]string{"claude": "ds"},
	}); err != nil {
		t.Fatal(err)
	}
	r := &registry{agents: make(map[string]*Agent), tokens: make(map[string]uint64)}
	if _, err := r.RegisterAgent(&Agent{ID: "claude", Slots: []Slot{{Name: "opus", Tier: "normal"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterAgent(&Agent{ID: "codex"}); err != nil {
		t.Fatal(err)
	}

	blocks := agentStatus{r}.StatusBlocks()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	text := strings.Join(blocks[0].Lines, "\n")
	for _, want := range []string{"claude", "codex", "ds", "set", "opus=normal", "discovered from its config at takeover"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status block missing %q:\n%s", want, text)
		}
	}
}
