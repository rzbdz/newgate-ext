package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// 带基线的写（compare-and-swap）：**读的时候是哪一份，写的时候就还得是哪一份**。
//
// # 为什么需要它
//
// 同一份配置文件有两个写者：命令行，和 web 界面。用户在浏览器里改档位的这几分钟
// 里，完全可能在另一个终端敲了 `newgate tier`——两边都写，后写的赢，前一个人的
// 改动**静默消失**。这不是理论：writeJSON 的注释里自己写着「当前没有跨进程写锁，
// 多个写者并发更新同一文件时仍是最后写入者生效」。
//
// # 为什么是检测而不是阻止
//
// 让写配置的每条路径都走锁，等于要求内核所有模块改写入方式（它们今天直接在
// 自己的 Start 里读-改-写）。而**检测**给出的东西更值钱：它能把冲突的两份内容
// 都摆给用户看，让他自己决定——「你的改动和被人的改动撞在同一段上了」比
// 「保存失败，请重试」有用得多。
//
// 所以判据是：**基线比对管正确性（发现冲突），flock 管原子性（同一瞬间只有一个
// 写者在读-比对-写）**。锁只对同样走这把锁的写者有效；命令行不拿它，命令行造成
// 的冲突由基线发现。检查与 rename 之间那个极小的窗口是承认的代价。

// revPrefix 让基线在人眼前一眼能认出是内容哈希（也是以后换算法时的版本位）。
const revPrefix = "sha256:"

// lockName 是写锁文件，放在配置根下。
//
// 与 daemon 的 pid/lock 分开：那是「谁在服务这个端口」，这是「谁在改这份配置」，
// 共用一个文件会互相拖住（而且 daemon 的锁在 runtime 目录，语义完全不同）。
const lockName = ".write.lock"

// ErrStale 是「基线对不上」。调用方用 errors.As 取 *StaleError 拿两边的原文。
var ErrStale = errors.New("store: the file changed on disk since it was loaded")

// StaleError 带**两边的原文**。
//
// 只有把两份内容都摆出来，用户才能决定怎么办：他的改动与别人的改动可能只是碰巧
// 撞在同一段，也可能南辕北辙。只回一句「过期了」，等于把这件事交给用户去猜。
type StaleError struct {
	Path    string
	Base    string // 你加载时那份
	Current string // 磁盘上现在这份
	Disk    []byte // 磁盘现状的原文（给人看，也给 diff）
}

func (e *StaleError) Error() string {
	return fmt.Sprintf("%s (loaded %s, on disk %s)", e.Path, shortRev(e.Base), shortRev(e.Current))
}
func (e *StaleError) Unwrap() error { return ErrStale }

func shortRev(rev string) string {
	if len(rev) > 15 {
		return rev[:15]
	}
	if rev == "" {
		return "(absent)"
	}
	return rev
}

// Revision 是文件内容哈希；文件不存在时返回空串（「基线 = 什么都没有」）。
func Revision(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return RevisionOf(b)
}

// RevisionOf 是一段内容的身份（调用方已经读过内容时用，省一次读盘）。
func RevisionOf(b []byte) string {
	sum := sha256.Sum256(b)
	return revPrefix + hex.EncodeToString(sum[:])
}

// WriteIfUnchanged 在 base 仍然等于磁盘现状时写入 data，返回新基线。
//
// 不一致时**什么都不写**，回 *StaleError（带磁盘原文）。读取、比对、写入三步在
// 同一把 flock 里完成——不然两个写者会各自读到旧内容、各自通过比对、后一个
// rename 把前一个的改动盖掉。
func WriteIfUnchanged(path, base string, data []byte) (string, error) {
	var newRev string
	_, err := WithLock(func() error {
		disk, err := os.ReadFile(path)
		switch {
		case err == nil:
		case os.IsNotExist(err):
			disk = nil // 文件不存在：基线必须是空串，否则算冲突
		default:
			return err
		}
		current := ""
		if disk != nil {
			current = RevisionOf(disk)
		}
		if current != base {
			return &StaleError{Path: path, Base: base, Current: current, Disk: disk}
		}
		if err := writeFileAtomic(path, data); err != nil {
			return err
		}
		newRev = RevisionOf(data)
		return nil
	})
	return newRev, err
}

// RemoveIfUnchanged 删一个文件，前提是它还是**加载时那一版**（base 即快照报的
// 基线；空串 = 预期它不存在——那正好就是「还没建」，删成即为幂等成功）。
//
// 为什么删文件也要 CAS：界面按下「删除某个档位」时，命令行可能刚改了或刚删了
// 同一个文件。没有基线比对，界面以为自己在删一份它看过的东西，实际可能已经是
// 别的内容——一次删除把别人刚写的配置抹掉。报 StaleError，让界面走冲突，把两
// 边摆出来。
func RemoveIfUnchanged(path, base string) error {
	_, err := WithLock(func() error {
		disk, err := os.ReadFile(path)
		switch {
		case err == nil:
		case os.IsNotExist(err):
			if base == "" {
				return nil // 本来就预期它不存在：删成
			}
			return &StaleError{Path: path, Base: base}
		default:
			return err
		}
		current := ""
		if disk != nil {
			current = RevisionOf(disk)
		}
		if current != base {
			return &StaleError{Path: path, Base: base, Current: current, Disk: disk}
		}
		return os.Remove(path)
	})
	return err
}

// WithLock 拿配置目录的写锁跑 fn。
//
// flock 而不是 pidfile：fd 一关锁就没了，进程被 kill -9 也不会留下需要人工清理的
// 陈旧锁——这个仓库已经有一个 pidfile 陈旧导致「判定没在跑」的现场，不必再造一个。
// flock 跨进程也跨 goroutine（同一个文件的两个 fd 是两份独立的 open file
// description），所以一个进程里的界面与命令行也互斥。
//
// 拿不到就等一会儿（wait）——用户敲了保存，多等两秒比回一句「正忙」好。
func WithLock(fn func() error) (bool, error) {
	path := filepath.Join(paths.Root(), lockName)
	if err := os.MkdirAll(filepath.Dir(path), 0o2770); err != nil {
		return false, err
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o660)
		if err != nil {
			return false, err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			defer func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}()
			return true, fn()
		}
		_ = f.Close()
		if err != syscall.EWOULDBLOCK {
			return false, err
		}
		if !time.Now().Before(deadline) {
			return false, i18n.E("another save is in progress, try again in a moment", nil)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// writeFileAtomic 同目录临时文件 + rename：读者只会看到完整旧版或新版。
//
// 临时名带随机后缀，**不是固定名**：固定名会让两个并发写者撞在同一个 inode 上，
// 一个 rename 走了，另一个 rename 的是一份被对方写了一半的内容。
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o2770); err != nil {
		return err
	}
	// 先把**当前这份**抄进历史环（见 history.go）。放在替换之前，且不因它失败而
	// 拒绝写盘——为了留备份让用户的保存失败，是把安全网变成了路障。
	snapshotBeforeWrite(path)
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	// 0600 太严（配置目录是多人共享读的），落 0660 再 rename。
	if err := f.Chmod(0o660); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
