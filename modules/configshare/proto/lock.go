package proto

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// WithLock 拿配置共享的建议锁跑 fn，返回是否真的跑了。
//
// # 为什么需要一把锁
//
// 优雅交接时会有几百毫秒新旧两个 daemon 同时活着（socket fd 移交，见
// cli.Serve 里那段注释）。两个 poller 会同时读改写同一份记账、同时往同一批
// 文件上 rename——而记账是 last-writer-wins 的，谁都可能把对方的代数和清单
// 覆盖回去。手动 `config pull` 与后台轮询也会撞。
//
// # 为什么用 flock 而不是 pidfile
//
// fd 一关锁就没了。进程被 kill -9 也不会留下一个需要人工清理的陈旧锁——
// 这个仓库已经有一个 pidfile 陈旧导致「判定没在跑 → 退化成 stop+start」的
// 现场（CLAUDE.md §3.1），不必再造一个。
//
// flock 跨进程**也**跨 goroutine：同一个文件的两个 fd 是两个独立的 open file
// description，所以进程内的 CLI 与 poller 也互斥。
//
// wait=0 表示拿不到就跳过这一轮——这是后台轮询的正确反应（下一轮再说，用户
// 什么都没感觉到）。手动命令给一个几秒的等待：用户敲了 config pull，多等两秒
// 比回一句"正忙"好。
func WithLock(path string, wait time.Duration, fn func() error) (bool, error) {
	if path == "" {
		return true, fn() // 没配锁（测试里常见）：直接跑
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o2770); err != nil {
		return false, err
	}
	deadline := time.Now().Add(wait)
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
			return false, nil // 别人正拿着：不是错误
		}
		time.Sleep(50 * time.Millisecond)
	}
}
