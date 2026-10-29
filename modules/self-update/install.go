package selfupdate

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/gateway/controlplane"
)

// 本文件是「换上去」那半：落一份候选、试跑、留旧版、rename、交接 daemon、回滚。
// 两条来源（Release、源码）走到这里时手上都只是一个文件，从这里起完全一样。

// installTarget 是要换掉的那个文件：正在跑的这个二进制（解开软链接）。
//
// 换「自己」而不是某个约定路径：用户从哪儿装的就换哪儿（~/.local/bin、
// /data/home2/…/WorkSpace/bin），PATH shim 指向的也是它。
func installTarget() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	// `go run` 编出来的临时二进制换了也白换：下次 go run 又是一份新的。
	if strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return "", i18n.E("this binary was started by `go run` ({exe}); build one with build/build.sh first", i18n.A{"exe": exe})
	}
	return exe, nil
}

// stage 在目标的**同一个目录**里建候选文件：rename 只在同一个文件系统里是原子的。
// 权限沿用旧文件（daemon 可能以另一个用户跑，它要能执行这份新的）。
//
// 调用方持有升级锁，所以同目录里残留的候选（上一次升级被 Ctrl-C / kill 在下载或
// 构建中途打断留下的）一定没人在用，顺手清掉——否则每断一次就在 bin 目录里多一个
// 十几 MB 的隐藏文件。
func stage(target string) (*os.File, error) {
	pattern := "." + filepath.Base(target) + ".new-"
	if stale, _ := filepath.Glob(filepath.Join(filepath.Dir(target), pattern+"*")); len(stale) > 0 {
		for _, f := range stale {
			_ = os.Remove(f)
		}
	}
	f, err := os.CreateTemp(filepath.Dir(target), pattern)
	if err != nil {
		return nil, i18n.Ef(err, "cannot write next to {target}: {err}", i18n.A{"target": target})
	}
	mode := os.FileMode(0o755)
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm() | 0o100
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}

// stageFrom 把一个已经在别处的产物（源码构建的输出）拷成候选。
func stageFrom(src, target string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	f, err := stage(target)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, in); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// smoke 以 newgate 的名字跑一次候选的 `version`，返回它报的版本号。
//
// 这是换上去之前**唯一**能拦住坏产物的地方：架构不对（exec format error）、
// 下载被截断、编成了别的发行版——换上去之后才发现，坏的就是每一条命令，包括
// 用来换回去的那条（rollback 还在，但那时用户得知道去找 .prev）。
//
// argv0 必须是 newgate：多调用型二进制按它分派，别的名字会被当成 profile。
func smoke(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.Args[0] = "newgate"
	cmd.Env = append(os.Environ(), "NEWGATE_LANG=en", "NO_COLOR=1")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	f := strings.Fields(line)
	if len(f) < 2 || f[0] != "newgate" {
		return "", i18n.E("unexpected `version` output: {line}", i18n.A{"line": firstLine(line)})
	}
	return f[1], nil
}

// install 把候选换上去，旧的那份留成 <target>.prev。**留不下旧版就不换**：
// 换上去之后的每一种失败（daemon 交接不上、新进程起不来）都靠 .prev 回退，没有它
// 等于没有退路。
//
// 旧版用**硬链接**留：先 link 再 rename，任何一刻 target 这个路径上都有一个完整
// 的二进制（先把 target 挪成 .prev 的话，中间那一下 PATH shim 会找不到命令）。
// 跑着的进程不受影响：它们握着旧 inode，rename 只换目录项（Text file busy 的那个
// 道理，见内核 CLAUDE.md §3.1）。
func install(candidate, target string) (string, error) {
	prev := target + ".prev"
	_ = os.Remove(prev)
	if err := os.Link(target, prev); err != nil {
		if err := copyFile(target, prev); err != nil {
			_ = os.Remove(candidate)
			return "", i18n.Ef(err, "cannot keep the current binary as {prev}, so there would be no way back; nothing was replaced: {err}",
				i18n.A{"prev": prev})
		}
	}
	if err := os.Rename(candidate, target); err != nil {
		_ = os.Remove(candidate)
		return "", i18n.Ef(err, "cannot replace {target}: {err}", i18n.A{"target": target})
	}
	return prev, nil
}

// rollback 把 <target>.prev 换回来，换下来的那份成为新的 .prev（再 rollback 一次
// 就又换回去——它是一个开关，不是一条单行道）。
func rollback(ctx context.Context, target string) (string, error) {
	prev := target + ".prev"
	if _, err := os.Stat(prev); err != nil {
		return "", i18n.E("there is no previous binary to roll back to ({prev} is missing)", i18n.A{"prev": prev})
	}
	ver, err := smoke(ctx, prev)
	if err != nil {
		return "", i18n.Ef(err, "the previous binary does not run: {err}", nil)
	}
	swap := target + ".swap-" + strconv.Itoa(os.Getpid())
	_ = os.Remove(swap)
	if err := os.Link(target, swap); err != nil {
		if err := copyFile(target, swap); err != nil {
			return "", err
		}
	}
	if err := os.Rename(prev, target); err != nil {
		_ = os.Remove(swap)
		return "", i18n.Ef(err, "cannot replace {target}: {err}", i18n.A{"target": target})
	}
	if err := os.Rename(swap, prev); err != nil {
		_ = os.Remove(swap)
	}
	return ver, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// restartDaemon 让跑着的 daemon 换到新二进制上：用**新的那份**执行 `restart`
// （优雅交接：旧进程把监听 socket 交给新进程，在途请求在旧进程里流完）。
//
// 为什么不自己发 /__newgate/upgrade：那条协议（探能力、带令牌、等 ready、退回
// stop+start）在内核 runtime 的 restart 里有一份完整实现，这里再写一份就是两份。
// 而用新二进制跑它，交接逻辑本身也是新版本的。
//
// daemon 不是从 target 起来的（另一份安装）时不动它——换的是这一份，重启那一份
// 只会让它继续跑旧的，还以为升级过了。
func restartDaemon(target string, log io.Writer) (string, error) {
	info, _ := controlplane.State()
	if info == nil {
		return i18n.T("the daemon is not running; its next start uses the new binary", nil), nil
	}
	if info.Exe != "" && !sameFile(info.Exe, target) {
		return i18n.T("the daemon (pid {pid}) runs from {exe}, not {target}; it was left alone",
			i18n.A{"pid": info.PID, "exe": info.Exe, "target": target}), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, target, "restart")
	cmd.Args[0] = "newgate"
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		return "", i18n.Ef(err, "`newgate restart` failed: {err}", nil)
	}
	// 不只看「有个进程」：要它**在答话**（控制面的 status 回得上来）。一个起得来、
	// 却在服务循环里出毛病的新版本，`version` 那一下是试不出来的。restart 自己最多
	// 等 5 秒，这里再给几秒余量。
	var after *controlplane.Info
	var doc *controlplane.Doc
	for i := 0; i < 20; i++ {
		if after, doc = controlplane.State(); after != nil && doc != nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if after == nil {
		return "", i18n.E("after the restart no daemon is running; see newgate logs", nil)
	}
	if doc == nil {
		return "", i18n.E("after the restart the daemon (pid {pid}) does not answer on its control port; see newgate logs",
			i18n.A{"pid": after.PID})
	}
	if runtime.GOOS == "linux" && !sameFile("/proc/"+strconv.Itoa(after.PID)+"/exe", target) {
		return "", i18n.E("the daemon (pid {pid}) is still running an old binary; see newgate logs",
			i18n.A{"pid": after.PID})
	}
	return i18n.T("daemon now runs the new binary (pid {old} → {new})",
		i18n.A{"old": info.PID, "new": after.PID}), nil
}

func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

// lockUpgrade 保证同一时刻只有一个升级在换文件。flock：进程死了锁就没了，
// 不会留下要人手动清理的陈旧锁。
func lockUpgrade() (func(), error) {
	if err := os.MkdirAll(paths.Root(), 0o2770); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(paths.Root(), ".self-update.lock"), os.O_CREATE|os.O_RDWR, 0o660)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, i18n.E("another upgrade is already running", nil)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
