package store

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureControlToken 控制令牌的生成与存活：
// 幂等（不换令牌）、普通 SaveState 冲不掉它（cmdStart 写回 state 的路径）。
func TestEnsureControlToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, "mappings"), 0o755)

	s, err := EnsureControlToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ControlToken) < 32 {
		t.Fatalf("令牌太短（%d 字符），熵不够: %q", len(s.ControlToken), s.ControlToken)
	}

	again, err := EnsureControlToken()
	if err != nil {
		t.Fatal(err)
	}
	if again.ControlToken != s.ControlToken {
		t.Fatalf("第二次调用换了令牌: %s → %s", s.ControlToken, again.ControlToken)
	}

	// 普通的 Load→改→Save（--set-profile 等命令的形态）必须原样带回落盘
	s2 := LoadState()
	s2.DefaultProfile = "other"
	if err := SaveState(s2); err != nil {
		t.Fatal(err)
	}
	if got := LoadState().ControlToken; got != s.ControlToken {
		t.Fatalf("普通 SaveState 把令牌冲掉了: %q → %q", s.ControlToken, got)
	}

	// 落盘形态：令牌在 state.json 里（供 daemon 的 watcher 读到）
	b, err := ioutil.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), s.ControlToken) {
		t.Fatal("令牌没落盘")
	}
}
