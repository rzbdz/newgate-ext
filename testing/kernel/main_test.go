package kernel

import (
	"context"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/entry"
	app "github.com/rzbdz/newgate/app"
	modules "github.com/rzbdz/newgate/component"
)

// onlyEntry is "a graph with only the entry ledger" - the kernel's one needed module, unclaimed.
type onlyEntry struct{}

func (onlyEntry) Load() ([]modules.Component, error) {
	return []modules.Component{entry.New()}, nil
}

// With nobody claiming the call, Main must exit in plain words (69), not crash, not 0.
//
// It used to live in cmd/newgate, testable only by starting a real process. In app it is an
// ordinary function call - **the return for putting composition-root logic in the kernel**:
// the distribution's main takes the same path, so exit codes cannot drift apart.
func TestMainExitsWhenNoEntryClaims(t *testing.T) {
	t.Setenv("NEWGATE_HOME", t.TempDir()) // 装配留痕要往配置目录写日志

	if code := app.Main(context.Background(), app.Options{Loader: onlyEntry{}}); code != 69 {
		t.Errorf("exit code when only the entry ledger exists and nobody claims it = %d, want 69", code)
	}
}
