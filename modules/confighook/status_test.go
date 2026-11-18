package confighook

import (
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
)

// 锁住「status 里每个客户端都列出来」：默认跟随的也要出现，单独定过的要标明。
// 上一版只有 config 的一行、且只在覆盖值与缺省不同时才展开一个 agent。
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
