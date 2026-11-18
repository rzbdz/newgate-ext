package daemon

// 本文件锁的是 2026-09-18 那个现场：**daemon 明明在服务，每个命令都说它没在跑**。
//
// 两次实测（WSL 重启后必现，签名一模一样）：pidfile 里躺着一个「撞锁失败、当场
// 退出的子进程」的 pid（4554 与 3080，两个僵尸），而真正的 daemon（1556 与
// 2069）一直在服务、锁文件也一直写着它们。链条见 WriteOwn 的注释。
//
// 所以这里有两条互不相同的保证，缺一条都不算修好：
//   - **写得对**：pidfile 由持有监听权的进程在抢到锁之后自己写（WriteOwn），
//     父进程不再代笔——代笔的那一份会被随后的陈旧锁清理删掉；
//   - **读得回来**：pidfile 站不住时，从锁文件把真身认出来（Reconcile），
//     而不是报「没在跑」——已经坏在磁盘上的现场也得能自愈。

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newgateServeProcess 起一个 argv0 和真 daemon 一样的进程：`newgate __serve`。
// 两个判据都得命中——Alive 认 cmdline 里的 "newgate"，Reconcile 的**推断**路径
// 还额外要求 "__serve"（见 isServe：那是「这是个 daemon」而不是「这也是个
// newgate 进程」的证据）。
func newgateServeProcess(t *testing.T) int {
	t.Helper()
	// argv0 由 execve 直接设定（`cmd.Args[0]`），**完全不经过 shell**。
	//
	// 这里原来是 `/bin/bash -c 'exec -a "newgate __serve --port 1" sleep 30'`：
	// 多出来的那一跳（bash 先起来，再 exec 成 sleep）本身就是一段窗口——那段时间
	// 里 /proc/<pid>/cmdline 是 bash 自己那行 `-c …`（恰好也含 newgate 与
	// __serve，两个判据都以为成立），而 bash 随时可能退出。2026-09-20 发行版 CI
	// 上这个夹具连着让两个测试飘红（TestReconcileHealsFromTheLock、
	// TestLastHealRecordsTheRepair），都是 0.00 秒、都报「本该发现不一致」。
	// 换成直接设 argv0 之后，子进程**存在的那一刻**cmdline 就是它，没有窗口。
	cmd := exec.Command("sleep", "30")
	cmd.Args[0] = "newgate __serve --port 1"
	if err := cmd.Start(); err != nil {
		t.Skipf("起不了测试进程: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	waitForProcess(t, cmd.Process.Pid,
		func(p int) bool { return Alive(p) && isServe(p) }, "一个 __serve 进程")
	return cmd.Process.Pid
}

// waitForProcess 等到这个 pid 真的满足**被测判据**再往下断言。
//
// 这里原来等的是「cmdline 非空」——不够。`cmd.Start()` 返回的瞬间子进程还是
// bash，`exec -a` 还没跑，那个窗口里 /proc/<pid>/cmdline 读出空串；被测的
// Alive / isServe 都拿 cmdline 当证据，测试于是站在了过渡态上。空串只是这个
// 窗口**好认**的那种坏形态：读到的若还是 bash 那行 `-c …`，两个判据看着都成立，
// 可 shell 随时可能退出（PATH 里找不到 sleep、exec 被拒、起手慢），断言就站在
// 一个正在消失的进程上——2026-09-20 发行版 CI 上就是这么飘的：
// TestReconcileHealsFromTheLock 0.00 秒报「锁里有个活着的 daemon，不该报没在跑」，
// 而同一份代码在内核 CI 与本机压三十遍都绿。
//
// 所以改成等判据本身成立。认不出来时**失败并带上证据**，不是跳过：夹具起不来
// 通常意味着判据在这台机器上不成立，那正是生产代码要听的事。
func waitForProcess(t *testing.T, pid int, want func(int) bool, what string) {
	t.Helper()
	if _, err := os.Stat("/proc/self"); err != nil {
		t.Skipf("这台机器没有 /proc（非 Linux？）：Alive/isServe 都退化成 kill(0)，测不出东西")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if want(pid) {
			return
		}
		if time.Now().After(deadline) {
			b, _ := ioutil.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
			t.Fatalf("pid %d 等了 3 秒也没能成为%s（Alive=%v isServe=%v cmdline=%q）——"+
				"要么夹具起不来，要么判据在这台机器上不成立",
				pid, what, Alive(pid), isServe(pid), b)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// why 把「代理看到的现场」讲成一行，贴在断言失败的消息后面。
//
// 这类飘的根因全在 pidfile / lock / 进程三者的状态里，而 CI 上只留一句「本该
// 发现不一致」时根本查不动——2026-09-20 那两次就是这么隔着日志猜的。
func why() string {
	pid, err := ReadPid()
	pidDesc := "nil"
	if pid != nil {
		pidDesc = fmt.Sprintf("%d(alive=%v)", pid.PID, Alive(pid.PID))
	}
	lock := LockHolder()
	return fmt.Sprintf("pidfile=%s err=%v; lock=%d alive=%v isServe=%v; LastHeal=%q",
		pidDesc, err, lock, Alive(lock), isServe(lock), LastHeal())
}

// zombiePid 造一个**僵尸**：子进程退出了但没人收尸（不调 Wait）。这正是现场里
// pidfile 记着的那个东西——Alive 判它「不在」（空 cmdline + isZombie），可它又
// 不会自己消失，于是错误状态一直留在磁盘上。
func zombiePid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Skipf("起不了测试进程: %v", err)
	}
	pid := cmd.Process.Pid
	deadline := time.Now().Add(2 * time.Second)
	for !isZombie(pid) {
		if time.Now().After(deadline) {
			_ = cmd.Wait()
			t.Skip("没等到僵尸态")
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() { _ = cmd.Wait() }) // 收尸，别把僵尸留给测试进程
	return pid
}

// freePid 挑一个当前不存在的 pid。不写死 999999：那个号真有可能存在。
// Alive 对它为 false（kill 报 ESRCH），正是 pidfile 最常见的坏法。
func freePid(t *testing.T) int {
	t.Helper()
	for pid := 3999000; pid > 399000; pid -= 7919 {
		if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); os.IsNotExist(err) {
			return pid
		}
	}
	t.Skip("找不到空闲 pid（这机器进程数异常）")
	return 0
}

func writePidFile(t *testing.T, pid, port int) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".config", "newgate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"pid":` + strconv.Itoa(pid) + `,"port":` + strconv.Itoa(port) + `}`
	if err := ioutil.WriteFile(filepath.Join(dir, ".newgate.pid"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeLockFile(t *testing.T, pid int) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".config", "newgate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(dir, ".newgate.lock"),
		[]byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReconcileHealsFromTheLock 就是现场那一幕：pidfile 指向僵尸，锁指着真身。
// 结论必须是「在跑，pid 是它」，而且 pidfile 当场被修回去——否则 status/metrics
// 会一直瞎下去，每次懒启动还多生一个注定撞锁的子进程。
func TestReconcileHealsFromTheLock(t *testing.T) {
	sandbox(t)
	live := newgateServeProcess(t)
	writePidFile(t, zombiePid(t), 8899) // 现场：pidfile 里是个僵尸
	writeLockFile(t, live)              // 锁一直是对的

	info, notes := Reconcile()
	if info == nil {
		t.Fatal("锁里有个活着的 daemon，不该报「没在跑」：" + why())
	}
	if info.PID != live {
		t.Errorf("认错了真身：got pid %d, want %d", info.PID, live)
	}
	// 端口只能来自 pidfile/配置——锁文件里没有端口，别把它丢了。
	if info.Port != 8899 {
		t.Errorf("端口该沿用 pidfile 里那个: got %d", info.Port)
	}
	if len(notes) == 0 {
		t.Error("发现不一致却什么都不说——doctor 那一项就是靠它出人话的")
	}
	// 修回去：下一个命令不必再推一遍，也不会再有人从死 pid 出发做决定。
	back, err := ReadPid()
	if err != nil || back.PID != live {
		t.Errorf("pidfile 没被修回真身: %+v (err %v)", back, err)
	}
	// 修好之后 Running 直接命中，不再有任何不一致可说。
	if Running() == nil {
		t.Fatal("修好之后 Running 仍然说没在跑")
	}
	if _, again := Reconcile(); len(again) != 0 {
		t.Errorf("已经一致了还在报不一致: %v", again)
	}
}

// TestReconcileHealsWhenPidfileIsMissing pidfile 整个不见了（陈旧锁清理把它删掉
// 的那一瞬间，见 WriteOwn）——同样得靠锁把真身捞回来。
func TestReconcileHealsWhenPidfileIsMissing(t *testing.T) {
	sandbox(t)
	live := newgateServeProcess(t)
	writeLockFile(t, live)

	info, notes := Reconcile()
	if info == nil || info.PID != live {
		t.Fatalf("pidfile 缺失时应从锁认出真身: %+v", info)
	}
	if len(notes) == 0 {
		t.Error("不一致必须说出来")
	}
}

// TestReconcileLeavesALivePidfileAlone 方向是单向的：pidfile 指向活进程时一律
// 信它。优雅交接的那一瞬间 pidfile 已是新进程、锁还是仍在排空的旧进程
// （AdoptRuntime 先写 pidfile 再改锁）——反过来信锁会把刚交出去的 pidfile 改回
// 旧进程，交接当场失败。
func TestReconcileLeavesALivePidfileAlone(t *testing.T) {
	sandbox(t)
	fresh := newgateServeProcess(t) // pidfile 说的：新进程
	draining := newgateServeProcess(t)
	writePidFile(t, fresh, 8899)
	writeLockFile(t, draining) // 锁说的：还在排空的旧进程

	info, notes := Reconcile()
	if info == nil || info.PID != fresh {
		t.Fatalf("pidfile 指向活进程时该信 pidfile: %+v", info)
	}
	if len(notes) != 0 {
		t.Errorf("交接窗口内不一致不该报警（几毫秒后自己会对上）: %v", notes)
	}
	back, _ := ReadPid()
	if back == nil || back.PID != fresh {
		t.Errorf("pidfile 被改动了——交接会因此失败: %+v", back)
	}
}

// TestReconcileSaysNothingWhenNobodyRuns 「真的没在跑」不是不一致：两边都没有
// 活着的 daemon 时，什么都不用说（doctor 报 skip，不是 warn）。
func TestReconcileSaysNothingWhenNobodyRuns(t *testing.T) {
	t.Run("两边都是死 pid", func(t *testing.T) {
		sandbox(t)
		dead := freePid(t)
		writePidFile(t, dead, 8899)
		writeLockFile(t, dead)
		if info, notes := Reconcile(); info != nil || len(notes) != 0 {
			t.Errorf("没在跑就该给 (nil, 空): %+v %v", info, notes)
		}
	})
	t.Run("只有陈旧锁", func(t *testing.T) {
		sandbox(t)
		writeLockFile(t, freePid(t))
		if info, notes := Reconcile(); info != nil || len(notes) != 0 {
			t.Errorf("没在跑就该给 (nil, 空): %+v %v", info, notes)
		}
	})
}

// TestReconcileRefusesALockThatIsNotTheDaemon 光「有个 newgate 进程」不够——
// 锁里的 pid 可能是回收后被别的 newgate 进程（CLI 自己）占用的号。用它当 daemon
// 会连环出错：status 报一个假 pid，stop 还会去 SIGTERM 用户的命令行进程。
// 所以推断路径要求更硬的证据（__serve），证据不足就老实说「不知道」。
func TestReconcileRefusesALockThatIsNotTheDaemon(t *testing.T) {
	sandbox(t)
	notADaemon := newgateNamedProcess(t) // argv0 带 newgate，但不是 __serve
	writePidFile(t, freePid(t), 8899)
	writeLockFile(t, notADaemon)

	info, notes := Reconcile()
	if info != nil {
		t.Fatalf("证据不足（不是 __serve）时不该认它当真身: %+v", info)
	}
	if len(notes) != 0 {
		t.Errorf("没认出来就别报警: %v", notes)
	}
	// 也不能顺手改 pidfile——那是拿猜测覆盖事实。
	back, _ := ReadPid()
	if back == nil || back.PID == notADaemon {
		t.Errorf("pidfile 被猜测改写了: %+v", back)
	}
}

// TestStopTakesTheLockHolderWhenPidfileIsStale 停机也得认对真身。
// 旧代码在 pidfile 指向死进程时报「本来没在跑」，顺手把**活 daemon 的锁删掉**
// ——服务还在、锁没了，下次 start 就能起出第二个实例去撞端口。
func TestStopTakesTheLockHolderWhenPidfileIsStale(t *testing.T) {
	sandbox(t)
	live := newgateServeProcess(t)
	writePidFile(t, freePid(t), 8899)
	writeLockFile(t, live)

	got, err := Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got != live {
		t.Fatalf("停错了对象：got pid %d, want %d", got, live)
	}
	for _, f := range []string{".config/newgate/.newgate.pid", ".config/newgate/.newgate.lock"} {
		if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), f)); !os.IsNotExist(err) {
			t.Errorf("%s 没被清掉", f)
		}
	}
}

// TestStaleLockCleanupThenWriteOwnIsTheFix 锁住修复的**顺序**：陈旧锁清理
// （AcquireLock 里的 RemovePid）必须发生在写 pidfile **之前**。现场正是反过来
// 的：父进程先写、子进程随后把陈旧锁和那份刚写好的 pidfile 一起清掉，于是
// 「daemon 活着、锁写着它、pidfile 不存在」，而再没有人补回来。
//
// 这里照着子进程真实的那两个动作走一遍：先 AcquireLock（它会清掉陈旧文件），
// 再 WriteOwn（自己写）。跑完 pidfile 必须记着本进程。
func TestStaleLockCleanupThenWriteOwnIsTheFix(t *testing.T) {
	sandbox(t)
	stale := freePid(t)
	writePidFile(t, stale, 8899) // 上个进程留下的两样东西都指向死 pid
	writeLockFile(t, stale)

	if err := AcquireLock(); err != nil {
		t.Fatalf("陈旧锁该被自动清理，抢锁不该失败: %v", err)
	}
	t.Cleanup(RemoveLock)
	if err := WriteOwn(18899); err != nil {
		t.Fatalf("WriteOwn: %v", err)
	}
	i, err := ReadPid()
	if err != nil {
		t.Fatalf("pidfile 没写出来: %v", err)
	}
	if i.PID != os.Getpid() || i.Port != 18899 {
		t.Errorf("pidfile 该记着本进程: %+v", i)
	}
	if i.StartedAt == "" {
		t.Error("started_at 是空的——`newgate status` 靠它显示起来了多久")
	}
}

// TestProcStartTimeMatchesPs /proc 只给「开机后第几个 tick」，得加上 btime 才是
// 墙钟时间。反推错了会让 status 显示一个荒谬的启动时刻，而那正是排查「他到底
// 重启过没有」时要看的东西。
func TestProcStartTimeMatchesPs(t *testing.T) {
	got := procStartTime(os.Getpid())
	if got.IsZero() {
		t.Skip("这个内核读不到 btime/starttime")
	}
	// 自证：本进程的启动时刻必然在「很久以前」与「现在」之间，且不该是 1970。
	if got.After(time.Now().Add(time.Minute)) || got.Before(time.Now().Add(-10*365*24*time.Hour)) {
		t.Fatalf("反推出来的启动时刻不合理: %v", got)
	}
}

// TestLastHealRecordsTheRepair 修好之后 pidfile 就对了，再对账一片绿——那处不
// 一致就此消失。doctor 里排在前面那几项（链路、代理）都会读 Running()，于是它们
// 会先把现场抹平，等「守护进程」那一项跑到时已经无话可说。这一行记账就是
// 「不静默」在自愈路径上的落点：悄悄修了，也得有人能说出来。
func TestLastHealRecordsTheRepair(t *testing.T) {
	sandbox(t)
	live := newgateServeProcess(t)
	writePidFile(t, freePid(t), 8899)
	writeLockFile(t, live)

	if _, notes := Reconcile(); len(notes) == 0 {
		t.Fatal("这一发本该发现不一致：" + why())
	}
	h := LastHeal()
	if h == "" {
		t.Fatal("修好了却没记账——doctor 会把一处刚发生的不一致报成全绿")
	}
	for _, want := range []string{"pidfile", strconv.Itoa(live)} {
		if !strings.Contains(h, want) {
			t.Errorf("记账那句话里该有 %q: %q", want, h)
		}
	}
	// 第二遍已经一致：不该再记一笔新的（否则每次体检都报同一条）。
	before := LastHeal()
	if _, notes := Reconcile(); len(notes) != 0 {
		t.Fatalf("已经一致了还在报: %v", notes)
	}
	if after := LastHeal(); after != before {
		t.Errorf("没有新的不一致却改了记账: %q → %q", before, after)
	}
}
