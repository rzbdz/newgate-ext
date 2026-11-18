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
		{"有界面：显式 __serve 认领", false, []string{"__serve", "--port", "8899"}, true},
		{"有界面：无参数不认领（那是给界面回答的）", false, nil, false},
		{"有界面：只带 --port 也不认领", false, []string{"--port", "8899"}, false},
		{"有界面：子命令不认领", false, []string{"status"}, false},

		{"无界面：无参数认领（这个进程就是守护进程）", true, nil, true},
		{"无界面：只带 --port 认领", true, []string{"--port", "8899"}, true},
		{"无界面：--port=N 认领", true, []string{"--port=8899"}, true},
		{"无界面：显式 __serve 照常认领", true, []string{"__serve", "--port", "8899"}, true},

		// 下面三条是「不许认」：没有界面时，敲了别的东西该得到「没人认领这次
		// 调用」这句人话，而不是悄悄起一个守护进程。
		{"无界面：未知子命令不认领", true, []string{"frobnicate"}, false},
		{"无界面：--help 不认领（别把它吞成起服务）", true, []string{"--help"}, false},
		{"无界面：别人的 flag 不认领", true, []string{"--json"}, false},
		{"无界面：子命令夹在自己的 flag 里也不认领", true, []string{"--port", "8899", "status"}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := serveEntry{headless: c.headless}
			if got := e.Claims(entryapi.Process{Argv0: "newgate", Args: c.args}); got != c.want {
				t.Errorf("Claims(%v) headless=%v = %v，期望 %v", c.args, c.headless, got, c.want)
			}
		})
	}
}

// TestServeEntryNameIsStable：名字进日志（组合根那句 `[entry] resolve: … → <Name>`），
// 也是诊断时唯一能认出它的东西，所以别跟着文案走。
func TestServeEntryNameIsStable(t *testing.T) {
	if got := (serveEntry{}).Name(); got != "__serve" {
		t.Errorf("入口名该是 __serve（与 daemon.Spawn 拼出来的参数一致），实际 %q", got)
	}
}
