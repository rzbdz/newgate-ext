package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rzbdz/newgate-ext/lib/httpx"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate/lib/i18n"
)

type Info struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	StartedAt string `json:"started_at"`
	Exe       string `json:"exe"`
}

func ReadPid() (*Info, error) {
	b, err := ioutil.ReadFile(paths.PidFile())
	if err != nil && paths.LegacyPidFile() != "" {
		// 老版本把 pid 写在 $HOME——升级后第一次 stop/status 靠它找到
		// 还在跑的旧 daemon（找到后会顺手清掉）。
		b, err = ioutil.ReadFile(paths.LegacyPidFile())
	}
	if err != nil {
		return nil, err
	}
	var i Info
	if err := json.Unmarshal(b, &i); err != nil {
		// 兼容纯数字 pid 文件
		if n, e := strconv.Atoi(strings.TrimSpace(string(b))); e == nil {
			return &Info{PID: n}, nil
		}
		return nil, err
	}
	return &i, nil
}

func WritePid(i *Info) error {
	b, _ := json.MarshalIndent(i, "", "  ")
	return ioutil.WriteFile(paths.PidFile(), append(b, '\n'), 0o660)
}

// RemovePid / RemoveLock 两处（共享目录 + 老的 $HOME 位置）都清：
// stop 一个 legacy pid 指到的旧 daemon 时，老文件得一起带走，否则
// 下次又把它当活实例读出来。
func RemovePid() {
	_ = os.Remove(paths.PidFile())
	if p := paths.LegacyPidFile(); p != "" {
		_ = os.Remove(p)
	}
}

func RemoveLock() {
	_ = os.Remove(paths.LockFile())
	if p := paths.LegacyLockFile(); p != "" {
		_ = os.Remove(p)
	}
}

// Alive 判断进程还在不在。只比 pid 会误判复用，所以还额外看 /proc 的 cmdline。
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		// EPERM：进程**在**，只是不归我们管（共享部署里别的用户起的
		// daemon）。「活没活着」和「能不能发信号」是两回事——后者由
		// Stop 走控制端点兜底。把它误判成不在，claude 用户就永远看到
		// 「代理没在跑」，这正是要修的「看不到状态」。
		if err != syscall.EPERM {
			return false
		}
	}
	b, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return true // 非 Linux 或读不到，退化为只看 kill(0)
	}
	// 空 cmdline 有两种含义，必须区分：
	//   1. 正在 exec 的过渡态（换镜像的瞬间 /proc 短暂读出空串）——活着。
	//      Spawn 刚返回就 Running()/Stop() 最容易撞上这个窗口，误判成
	//      死会引发「明明刚拉起却报没在跑」；
	//   2. 僵尸（死了没人收尸）——死。
	if len(b) == 0 {
		return !isZombie(pid)
	}
	return strings.Contains(string(b), "newgate")
}

// isZombie 看 /proc/<pid>/stat 的状态位。stat 的 comm 字段可含空格和括号，
// 所以从最后一个 ')' 之后取状态字符。
func isZombie(pid int) bool {
	st, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	if i := bytes.LastIndexByte(st, ')'); i >= 0 && i+2 < len(st) {
		return st[i+2] == 'Z'
	}
	return false
}

// isServe 确认这个 pid 是**守护进程本体**，而不是另一个也叫 newgate 的进程
// （CLI 自己、别的实例）。判据是 cmdline 里的 `__serve`——只有
// `newgate __serve` 持有监听 socket。
//
// 它只用在「从锁文件反推 daemon」这条**推断**路径上，所以要求比 Alive 更硬的
// 证据；pidfile 那条路是写者自证身份，仍然只用 Alive。判不了（非 Linux，
// 没有 /proc）时不猜、放过。
//
// 「判不了」与「证据不足」在这条路径上是两种处理，别混：判不了 = 这台机器
// 根本没给判据（放过）；证据不足 = 判据在、但读到的内容说明不了问题（**拒绝**，
// 老实说「不知道」）。原先把后者也算成放过（`len(b) == 0 || …`），2026-09-20
// 在 CI 上飘过一次：测试起的进程正在 exec 成另一个镜像，那个窗口里 cmdline 是
// 空的，于是「刚 fork 出来的 bash」被当成了守护进程本体。
func isServe(pid int) bool {
	b, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		// 这台机器没有 /proc（非 Linux）：判据不存在，放过。
		if _, e := os.Stat("/proc/self"); e != nil {
			return true
		}
		// 有 /proc 却读不到这个 pid：它在 Alive 与这里之间没了。**刚死不是
		// 是 daemon**——拿一个已经不存在的 pid 去当真身（status 报假 pid、
		// stop 去 SIGTERM 别人的进程）正是这条路径要防的事。
		return false
	}
	// 空 cmdline 是 exec 过渡态（同 Alive 的注释）。**推断**路径上它意味着
	// 证据不足，而证据不足要说不知道，不能猜。
	return len(b) > 0 && strings.Contains(string(b), "__serve")
}

// LockHolder 读锁文件里的 pid（新老两个位置），读不出给 0。
//
// 锁是「谁在服务」这件事的**权威证据**：它由抢到监听权的进程用 O_EXCL 写下、
// 写在它自己的生命周期里；优雅交接时由 AdoptRuntime 改写到新进程名下。
// pidfile 只是它的缓存（见 Reconcile）。
func LockHolder() int {
	for _, f := range []string{paths.LockFile(), paths.LegacyLockFile()} {
		if f == "" {
			continue
		}
		b, err := ioutil.ReadFile(f)
		if err != nil {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}

// AcquireLock 独占锁。发现死锁文件自动清理。
func AcquireLock() error {
	// 新老两个位置都查：老 daemon 的活锁在 $HOME，不认它就会起出
	// 第二个实例去撞端口。
	for _, f := range []string{paths.LockFile(), paths.LegacyLockFile()} {
		if f == "" {
			continue
		}
		if b, err := ioutil.ReadFile(f); err == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			if Alive(pid) {
				return i18n.E("newgate is already running (pid {pid}). Use `newgate restart` or `newgate stop`",
					i18n.A{"pid": pid})
			}
			// 陈旧锁
			RemoveLock()
			RemovePid()
		}
	}
	f, err := os.OpenFile(paths.LockFile(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o660)
	if err != nil {
		return i18n.Ef(err, "cannot acquire the lock: {err}", nil)
	}
	defer f.Close()
	f.WriteString(strconv.Itoa(os.Getpid()))
	f.WriteString("\n")
	return err
}

// Running 返回当前活着的实例信息。
func Running() *Info {
	i, _ := Reconcile()
	return i
}

// LastHeal 报告本进程内最近一次「对账修好了 pidfile」，没有修过给空串。
//
// 为什么需要它：修好之后 pidfile 就对了，**再对账是一片绿**。而 `newgate doctor`
// 里排在前面那几项（链路、代理）都会读 Running()，于是它们先把 pidfile 修好，
// 等到「守护进程」那一项跑的时候，那处不一致已经看不见了——体检全绿，用户永远
// 不知道自己刚才经历的是什么。这一行就是「不静默」在自愈路径上的落点。
func LastHeal() string {
	healMu.Lock()
	defer healMu.Unlock()
	return healNote
}

var (
	healMu   sync.Mutex
	healNote string
)

// Reconcile 回答「谁真的在跑」，顺手把 pidfile 修回真身；第二个返回值是发现的
// 那处不一致（doctor 用来出人话，一切正常时为空）。
//
// **为什么不能只看 pidfile**（2026-09-18，两次现场，都是重启后必现）：pidfile
// 会躺着一个「撞锁失败、当场退出的子进程」的 pid，而 daemon 一直在服务——
// 见 WriteOwn 记的那条完整链条。锁文件是这件事的权威证据，所以 pidfile 站不住
// 时用锁对账。
//
// 但**方向是单向的**：pidfile 指向活进程时一律信它，绝不反过来信锁。优雅交接的
// 那一瞬间，父进程会先把 pidfile 改写到自己名下、随后才改锁（AdoptRuntime），
// 此刻「pidfile = 新进程、锁 = 仍在排空的旧进程」——反过来信锁会把刚交出去的
// pidfile 又改回旧进程，交接就此失败。
//
// 「pidfile 指向死进程」与「真的没在跑」必须分开：前者是不一致（要说出来、要
// 修），后者是正常状态（一个字都不用说）。
func Reconcile() (*Info, []string) {
	i, err := ReadPid()
	if err == nil && i != nil && Alive(i.PID) {
		return i, nil
	}
	pid := LockHolder()
	if pid <= 0 || !Alive(pid) || !isServe(pid) {
		return nil, nil
	}
	healed := &Info{PID: pid, Port: portOf(i), Exe: exeOf(pid)}
	if t := procStartTime(pid); !t.IsZero() {
		healed.StartedAt = t.Format(time.RFC3339)
	}
	note := i18n.T("The process the pidfile points at is gone ({what}), and the lock file says the real daemon is pid {pid}",
		i18n.A{"what": describePid(i, err), "pid": pid})
	if werr := WritePid(healed); werr != nil {
		note += i18n.T("; the pidfile could not be corrected: {err}", i18n.A{"err": werr.Error()})
	} else {
		note += i18n.T(" (pidfile corrected)", nil)
		healMu.Lock()
		healNote = note
		healMu.Unlock()
	}
	return healed, []string{note}
}

// describePid 把「pidfile 说了什么」讲成人话，用在上面那句不一致里。
func describePid(i *Info, err error) string {
	switch {
	case err != nil || i == nil:
		return i18n.T("the pidfile is unreadable", nil)
	case i.PID <= 0:
		return i18n.T("the pidfile holds no pid", nil)
	default:
		return fmt.Sprintf("pid %d", i.PID)
	}
}

// portOf 优先用 pidfile 里记的端口；pidfile 不可用时退回配置里的端口。
func portOf(i *Info) int {
	if i != nil && i.Port > 0 {
		return i.Port
	}
	if st := store.LoadState(); st != nil {
		return st.Port
	}
	return 0
}

func exeOf(pid int) string {
	if p, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
		return p
	}
	return ""
}

// procStartTime 反推进程的启动时刻：/proc 只给「开机后第几个 tick」
// （stat 里去掉 pid/comm 之后的第 20 个字段），要加上 /proc/stat 的 btime
// 才是墙钟时间。拿不到就给零值——它只进 pidfile 供人看，猜一个不如空着。
func procStartTime(pid int) time.Time {
	btime := int64(0)
	if b, err := ioutil.ReadFile("/proc/stat"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "btime ") {
				btime, _ = strconv.ParseInt(strings.TrimSpace(line[len("btime "):]), 10, 64)
				break
			}
		}
	}
	st, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil || btime == 0 {
		return time.Time{}
	}
	rest := st[bytes.LastIndexByte(st, ')')+1:]
	fields := strings.Fields(string(rest))
	if len(fields) < 20 {
		return time.Time{}
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return time.Time{}
	}
	// Linux 的 CLK_TCK 恒为 100（os.Sysconf 不在标准库，而这里只是给人看的时间戳）
	return time.Unix(btime+ticks/100, 0)
}

// WriteOwn 由守护进程**自己**写下 pidfile：它才是拥有监听 socket 的那个进程。
//
// 2026-09-18 两次实测（重启后必现，两个现场签名一模一样）：pidfile 里躺着一个
// 「撞锁失败、当场退出的子进程」的 pid（4554 与 3080，两个僵尸），而真正的
// daemon（1556 与 2069）一直在服务、锁文件也一直写着它们。链条是：
//
//  1. 懒启动 → Spawn 起子进程 → **父进程抢先写 pidfile**；
//  2. 子进程 AcquireLock 读到上个进程留下的**陈旧锁** → RemoveLock + RemovePid，
//     把第 1 步刚写的那份 pidfile 一并删掉；
//  3. 子进程建锁、开始服务，而**再没有任何人会写一次 pidfile**；
//  4. 之后每个命令都读到「没在跑」→ 又懒启动一个注定撞锁的子进程 → 它的 pid
//     被写进 pidfile（第 2 步不再发生，因为锁现在是活的）→ 死 pid 留在那儿。
//
// 把「写」挪到**抢到锁之后、由持有者自己写**，就只剩一个写者，而且它一定活着。
func WriteOwn(port int) error {
	if port <= 0 {
		if st := store.LoadState(); st != nil {
			port = st.Port
		}
	}
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}
	return WritePid(&Info{PID: os.Getpid(), Port: port,
		StartedAt: time.Now().Format(time.RFC3339), Exe: exe})
}

// Spawn 把自己以 __serve 模式重新拉起，作为后台守护进程。
//
// **pidfile 不在这里写**（2026-09-18）：写它的人必须是那个真正抢到锁、开始服务的
// 进程，也就是子进程自己（见 WriteOwn 记的那条完整链条——父进程代写的每一份，
// 都可能被子进程随后的「陈旧锁清理」删掉，而再没有人补回来）。返回的 Info 仍然
// 是子进程的 pid，调用方拿它报「代理已启动 pid N」是对的：它就是刚起来的那个。
func Spawn(port int) (*Info, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	// 0660：共享部署下别的用户也能读日志排查（目录 setgid 保证组一致）
	logf, err := os.OpenFile(paths.LogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o660)
	if err != nil {
		return nil, err
	}
	defer logf.Close()

	cmd := exec.Command(exe, "__serve", "--port", strconv.Itoa(port))
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // 脱离终端
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	info := &Info{PID: cmd.Process.Pid, Port: port,
		StartedAt: time.Now().Format(time.RFC3339), Exe: exe}
	_ = cmd.Process.Release()
	return info, nil
}

// SpawnHandoff 是 nginx 式优雅升级的核心动作：把监听 socket 作为 fd 交给
// 新二进制，等它接管成功（ready 字节）后返回新进程信息。
//
// 为什么这能零停机：socket 的可用性由内核维护，和进程生死解耦。父进程
// 把 fd dup 给子进程，两个进程短暂同时在同一个 socket 上 accept（内核
// 分流），随后父进程只负责把在途请求流完——客户端自始至终没有任何一毫秒
// 面对过「连接被拒」。开发 newgate 的 Claude Code 会话本身就穿行在代理
// 里，restart 杀掉自己脚下这条线的事不能再发生。
//
// 约定（Serve 端配合）：
//   - fd3 = 监听 socket；fd4 = ready 管道写端
//   - 子进程绑好 socket、即将进入 accept 循环时往 fd4 写一个字节
func SpawnHandoff(port int, ln net.Listener) (*Info, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	tcp, ok := ln.(*net.TCPListener)
	if !ok {
		return nil, i18n.E("handoff needs a TCP listener, got {type}",
			i18n.A{"type": fmt.Sprintf("%T", ln)})
	}
	// File() 返回 dup 出来的新 fd——父进程自己的 listener 不受影响，
	// 之后的 Shutdown 关的是父进程那份。
	listenerFile, err := tcp.File()
	if err != nil {
		return nil, i18n.Ef(err, "cannot extract the listener fd: {err}", nil)
	}
	defer listenerFile.Close()

	// ready 管道：子进程接上 socket 的唯一凭证。等不到它就交棒失败。
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer readyR.Close()

	// 0660：共享部署下别的用户也能读日志排查（目录 setgid 保证组一致）
	logf, err := os.OpenFile(paths.LogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o660)
	if err != nil {
		return nil, err
	}
	defer logf.Close()

	cmd := exec.Command(exe, "__serve", "--port", strconv.Itoa(port))
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // 脱离终端
	cmd.ExtraFiles = []*os.File{listenerFile, readyW}    // fd3=socket, fd4=ready
	cmd.Env = append(os.Environ(),
		"NEWGATE_LISTENER_FD=3", "NEWGATE_READY_FD=4")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// 父进程立刻放下写端：否则子进程就算死了，读端也等不来 EOF。
	readyW.Close()
	if err := readyR.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	var b [1]byte
	if _, err := io.ReadFull(readyR, b[:]); err != nil {
		// 新进程没接上：杀掉它，本进程继续独占 socket，服务未受任何影响
		_ = cmd.Process.Kill()
		return nil, i18n.E("the new process did not attach to the listening socket within 5 seconds: {err}",
			i18n.A{"err": err})
	}
	info := &Info{PID: cmd.Process.Pid, Port: port,
		StartedAt: time.Now().Format(time.RFC3339), Exe: exe}
	_ = cmd.Process.Release()
	return info, nil
}

// AdoptRuntime 把 pid 文件和锁的归属改到新进程——优雅交接后由它持有。
// 只动共享目录：legacy 的 $HOME 位置在交接时早就迁移完了。
func AdoptRuntime(i *Info) error {
	if err := WritePid(i); err != nil {
		return err
	}
	return ioutil.WriteFile(paths.LockFile(),
		[]byte(strconv.Itoa(i.PID)+"\n"), 0o660)
}

// Stop 优雅停，等不到就强杀。
//
// 共享部署的兜底：daemon 可能是别的用户起的（root 起的、claude 用户来停）。
// 同一个组只给读文件的权限，不给 kill() 的权限——信号发不出去（EPERM）时
// 走代理自己的控制端点（POST /__newgate/stop + ControlToken）让它自己退。
//
// 杀谁由 Reconcile 决定，不是 pidfile 一个人说了算：pidfile 指向死进程时它会把
// 真身（锁的持有者）认出来。否则 `newgate stop` 会在「pidfile 坏了」的现场报
// 「本来没在跑」，顺手把活 daemon 的锁删掉——服务还在，锁没了（2026-09-18 现场）。
func Stop() (int, error) {
	i, _ := Reconcile()
	if i == nil {
		// 两边都没有活着的 daemon：把死文件清掉，免得下次又读到。
		RemovePid()
		RemoveLock()
		return 0, nil
	}
	if err := syscall.Kill(i.PID, syscall.SIGTERM); err == syscall.EPERM {
		return stopViaHTTP(i)
	}
	for k := 0; k < 40; k++ { // 最多等 2s
		if !Alive(i.PID) {
			RemovePid()
			RemoveLock()
			return i.PID, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(i.PID, syscall.SIGKILL)
	RemovePid()
	RemoveLock()
	return i.PID, nil
}

// stopViaHTTP 让别的用户起的 daemon 自己退出。
//
// 令牌在 state.json 里，和 providers 的 key 同级保密（0660、组可读）——
// 能读到它的组员本来就是这台机器的受信用户，停机权限给得合理。
func stopViaHTTP(i *Info) (int, error) {
	st := store.LoadState()
	if st.ControlToken == "" {
		return 0, i18n.E("the proxy (pid {pid}) runs as another user and has no control token — ask the user who started it to run `newgate stop` once (after that any group member can stop it)",
			i18n.A{"pid": i.PID})
	}
	port := i.Port
	if port <= 0 {
		port = st.Port
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/__newgate/stop", port), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+st.ControlToken)
	resp, err := httpx.LocalClient(3 * time.Second).Do(req)
	if err != nil {
		return 0, i18n.Ef(err, "cannot send the stop request to the proxy (pid {pid}): {err}",
			i18n.A{"pid": i.PID})
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, i18n.E("the proxy refused the stop request (HTTP {code})", i18n.A{"code": resp.StatusCode})
	}
	for k := 0; k < 40; k++ { // 最多等 2s
		if !Alive(i.PID) {
			RemovePid()
			RemoveLock()
			return i.PID, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, i18n.E("the stop request was accepted, but the proxy (pid {pid}) did not exit within 2 seconds (see the log {log})",
		i18n.A{"pid": i.PID, "log": paths.LogFile()})
}
