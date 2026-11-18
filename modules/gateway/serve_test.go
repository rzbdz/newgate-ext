package gateway

import (
	"testing"

	entryapi "github.com/rzbdz/newgate/component/entry"
)

// TestServeEntryClaims 钉住守护进程入口的认领判据。
//
// 这张表一半是「认」，一半是**「不许认」**，而后者才是要紧的：认多了的症状是
// 敲一条不存在的命令却起了一个守护进程（要等端口被占、或者根本连不上才会发现），
// 认少了的症状是纯 dashboard 的构建**永远起不来**（`no entry claimed this call`，
// 2026-09-20 实测过）。
//
// headless 那一列是「装配里没有终端界面」，由 Start 按装了什么算出来
// （见 module.go）——它决定无子命令时这个进程是什么。
func TestServeEntryClaims(t *testing.T) {
	cases := []struct {
		name     string
		headless bool
		args     []string
		want     bool
	}{
		{"with a UI: an explicit __serve claims", false, []string{"__serve", "--port", "8899"}, true},
		{"with a UI: no args does not claim (that is for the UI to answer)", false, nil, false},
		{"with a UI: --port alone does not claim", false, []string{"--port", "8899"}, false},
		{"with a UI: subcommands do not claim", false, []string{"status"}, false},

		{"headless: no args claims (this process is the daemon)", true, nil, true},
		{"headless: --port alone claims", true, []string{"--port", "8899"}, true},
		{"headless: --port=N claims", true, []string{"--port=8899"}, true},
		{"headless: an explicit __serve still claims", true, []string{"__serve", "--port", "8899"}, true},

		// 下面三条是「不许认」：没有界面时，敲了别的东西该得到「没人认领这次
		// 调用」这句人话，而不是悄悄起一个守护进程。
		{"headless: unknown subcommands do not claim", true, []string{"frobnicate"}, false},
		{"headless: --help does not claim (do not swallow it into starting the service)", true, []string{"--help"}, false},
		{"headless: flags belonging to others do not claim", true, []string{"--json"}, false},
		{"headless: a subcommand among its own flags does not claim", true, []string{"--port", "8899", "status"}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := serveEntry{headless: c.headless}
			if got := e.Claims(entryapi.Process{Argv0: "newgate", Args: c.args}); got != c.want {
				t.Errorf("Claims(%v) headless=%v = %v, want %v", c.args, c.headless, got, c.want)
			}
		})
	}
}

// TestServeEntryNameIsStable：名字进日志（组合根那句 `[entry] resolve: … → <Name>`），
// 也是诊断时唯一能认出它的东西，所以别跟着文案走。
func TestServeEntryNameIsStable(t *testing.T) {
	if got := (serveEntry{}).Name(); got != "__serve" {
		t.Errorf("the entry name should be __serve (matching the args daemon.Spawn builds), got %q", got)
	}
}
