package daemon

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/lib/i18n"
)

// sandbox 把整套路径（含 legacy 的 $HOME 位置）圈进临时目录。
// 注意不能设 NEWGATE_HOME——那会关掉 legacy 兜底；改 HOME 才是正路：
// root() 和 LegacyPidFile() 就都落在沙箱里，摸不到真实机器上的活 daemon。
func sandbox(t *testing.T) string {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	_ = os.Unsetenv("NEWGATE_HOME")
	_ = os.Unsetenv("XDG_CONFIG_HOME")
	return dir
}

// newgateNamedProcess 起一个 /proc/<pid>/cmdline 里带 "newgate" 的进程
// （Alive 靠这个字符串判断身份，普通 sleep 会被当成无关进程），返回 pid。
//
// argv0 由 execve 直接设定，不经过 shell（同 newgateServeProcess 的说明）。
// 原来是 `bash -c 'exec -a newgate-sleep sleep 30'`：那一跳不但留下一段窗口，
// 还逼着这里必须用 bash——本机的 dash 不支持 `exec -a`，用 sh 会当场退出，
// 探测就变成和僵尸赛跑的玄学（见 Stop 的失败重现实录）。现在没有 shell 可挑。
func newgateNamedProcess(t *testing.T) int {
	cmd := exec.Command("sleep", "30")
	cmd.Args[0] = "newgate-sleep"
	if err := cmd.Start(); err != nil {
		t.Skipf("起不了测试进程: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	// 等判据自己成立，而不是等一个「cmdline 非空」的中间态（见 waitForProcess）：
	// 用它的测试要的正是这个组合——Alive 认它，isServe 不认它。
	waitForProcess(t, cmd.Process.Pid,
		func(p int) bool { return Alive(p) && !isServe(p) }, "一个像 newgate 但不是 __serve 的进程")
	return cmd.Process.Pid
}

// TestReadPidFallsBackToLegacy 升级后的形态：pid 在老位置（$HOME），
// 新位置（共享目录）还没有。读不到它，正在跑的旧 daemon 就「失踪」了。
func TestReadPidFallsBackToLegacy(t *testing.T) {
	dir := sandbox(t)
	_ = os.MkdirAll(filepath.Join(dir, ".config", "newgate"), 0o755)
	if err := ioutil.WriteFile(filepath.Join(dir, ".newgate.pid"),
		[]byte(`{"pid":4242,"port":8899}`), 0o644); err != nil {
		t.Fatal(err)
	}
	i, err := ReadPid()
	if err != nil || i == nil {
		t.Fatalf("应兜底读到 legacy pid: %v", err)
	}
	if i.PID != 4242 || i.Port != 8899 {
		t.Fatalf("读错了: %+v", i)
	}
}

// TestRemoveCleansBothLocations stop 掉一个 legacy pid 指到的旧 daemon 时，
// 新老两处文件都得带走，否则下次又把死文件当活实例读出来。
func TestRemoveCleansBothLocations(t *testing.T) {
	dir := sandbox(t)
	cfg := filepath.Join(dir, ".config", "newgate")
	_ = os.MkdirAll(cfg, 0o755)
	_ = ioutil.WriteFile(filepath.Join(cfg, ".newgate.pid"), []byte("1"), 0o644)
	_ = ioutil.WriteFile(filepath.Join(dir, ".newgate.pid"), []byte("2"), 0o644)
	_ = ioutil.WriteFile(filepath.Join(cfg, ".newgate.lock"), []byte("1"), 0o644)
	_ = ioutil.WriteFile(filepath.Join(dir, ".newgate.lock"), []byte("2"), 0o644)
	RemovePid()
	RemoveLock()
	for _, p := range []string{
		filepath.Join(cfg, ".newgate.pid"),
		filepath.Join(dir, ".newgate.pid"),
		filepath.Join(cfg, ".newgate.lock"),
		filepath.Join(dir, ".newgate.lock"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s 没被清掉", p)
		}
	}
}

// TestAcquireLockHonorsLiveLegacyLock 老位置的活锁必须挡住新实例：
// 不认它就会起出第二个 daemon 去撞 8899 端口。
func TestAcquireLockHonorsLiveLegacyLock(t *testing.T) {
	dir := sandbox(t)
	pid := newgateNamedProcess(t)
	if err := ioutil.WriteFile(filepath.Join(dir, ".newgate.lock"),
		[]byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := AcquireLock()
	if err == nil {
		RemoveLock()
		t.Fatal("老位置有活锁，不该抢成功")
	}
}

// TestStopViaLegacyPid 完整走一遍升级路径：pid 只在老位置、进程活着 →
// Stop 杀掉它、清掉两处文件。
func TestStopViaLegacyPid(t *testing.T) {
	dir := sandbox(t)
	pid := newgateNamedProcess(t)
	if err := ioutil.WriteFile(filepath.Join(dir, ".newgate.pid"),
		[]byte(`{"pid":`+strconv.Itoa(pid)+`,"port":18899}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(dir, ".newgate.lock"),
		[]byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Stop()
	if err != nil || got != pid {
		t.Fatalf("Stop 经 legacy pid 应杀掉 %d: got %d, err %v", pid, got, err)
	}
	for _, p := range []string{
		filepath.Join(dir, ".newgate.pid"),
		filepath.Join(dir, ".newgate.lock"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s 没被清掉", p)
		}
	}
}

// TestStopViaHTTPToken 跨用户停机路径：信号发不出去（EPERM）时走控制端点。
// 这里用一个只认令牌的假 daemon 验证 stopViaHTTP 本身——令牌对就停掉并
// 清理 pid/lock；令牌缺失或被拒都返回错误、不动文件。
func TestStopViaHTTPToken(t *testing.T) {
	dir := sandbox(t)
	stateFile := filepath.Join(dir, ".config", "newgate", "state.json")
	_ = os.MkdirAll(filepath.Dir(stateFile), 0o755)
	writeState := func(tok string) {
		s := `{"default_profile":"ds","port":0`
		if tok != "" {
			s += `,"control_token":"` + tok + `"`
		}
		s += `}`
		if err := ioutil.WriteFile(stateFile, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// startFakeDaemon 起一个假 daemon：HTTP 面只认 expect 令牌，收到带对
	// 令牌的停机请求就把自己的进程杀掉（模拟「daemon 自己退」）。
	startFakeDaemon := func(expect string) (url string, seen chan string, pid, port int) {
		cmd := exec.Command("/bin/bash", "-c", "exec -a newgate-serve sleep 30")
		if err := cmd.Start(); err != nil {
			t.Skipf("起不了测试进程: %v", err)
		}
		seen = make(chan string, 4)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen <- r.Header.Get("Authorization")
			if r.Header.Get("Authorization") != "Bearer "+expect {
				w.WriteHeader(403)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
			_ = cmd.Process.Kill()
		}))
		t.Cleanup(func() {
			srv.Close()
			_ = cmd.Process.Kill()
		})
		port, _ = strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
		return srv.URL, seen, cmd.Process.Pid, port
	}

	pidFile := filepath.Join(dir, ".config", "newgate", ".newgate.pid")
	lockFile := filepath.Join(dir, ".config", "newgate", ".newgate.lock")
	writePidLock := func() {
		_ = os.MkdirAll(filepath.Dir(pidFile), 0o755)
		_ = ioutil.WriteFile(pidFile, []byte("9"), 0o644)
		_ = ioutil.WriteFile(lockFile, []byte("9"), 0o644)
	}

	t.Run("没有令牌要给出人话", func(t *testing.T) {
		writeState("")
		_, err := stopViaHTTP(&Info{PID: 1, Port: 1})
		// 断言消息身份（语言无关）：这句话的要点是「没有控制令牌」，不是它用哪种语言说。
		if err == nil || !strings.Contains(i18n.ID(err), "no control token") {
			t.Fatalf("应报「没有控制令牌」，实际: %v", err)
		}
	})

	t.Run("令牌被拒不清理", func(t *testing.T) {
		writeState("tok-a")
		_, seen, _, port := startFakeDaemon("tok-b")
		writePidLock()
		if _, err := stopViaHTTP(&Info{PID: 1, Port: port}); err == nil {
			t.Fatal("403 应该报错")
		}
		if a := <-seen; a != "Bearer tok-a" {
			t.Fatalf("发出去的令牌不对: %q", a)
		}
		if _, err := os.Stat(pidFile); err != nil {
			t.Fatalf("被拒时 pid 文件不该消失: %v", err)
		}
	})

	t.Run("令牌对停掉并清理", func(t *testing.T) {
		writeState("tok-right")
		_, seen, dpid, port := startFakeDaemon("tok-right")
		writePidLock()
		got, err := stopViaHTTP(&Info{PID: dpid, Port: port})
		if err != nil || got != dpid {
			t.Fatalf("应停掉假 daemon (pid %d): got %d, err %v", dpid, got, err)
		}
		if a := <-seen; a != "Bearer tok-right" {
			t.Fatalf("发出去的令牌不对: %q", a)
		}
		for _, p := range []string{pidFile, lockFile} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Errorf("%s 没被清掉", p)
			}
		}
	})
}
