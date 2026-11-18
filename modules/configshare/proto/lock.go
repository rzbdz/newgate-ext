package proto

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// WithLock runs fn under the configshare advisory lock, returning whether it really ran.
//
// # Why a lock is needed
//
// A graceful handoff leaves two daemons alive for a few hundred ms (socket fd handoff,
// see cli.Serve). Two pollers then read-modify-write the same ledger - last-writer-wins,
// so either can revert the other's generation and file list. A manual `config pull`
// collides with the poller the same way.
//
// # Why flock instead of a pidfile
//
// Closing the fd drops the lock. Even kill -9 leaves no stale lock needing manual
// cleanup - this repo already has a pidfile staleness incident ("not running ->
// degrade to stop+start", CLAUDE.md §3.1), and does not need a second.
//
// flock is cross-process **and** cross-goroutine: two fds on one file are independent
// open file descriptions, so the in-process CLI and poller exclude each other as well.
//
// wait=0 means skip this round when the lock is taken - right for background polling
// (next round will do it, the user notices nothing). Manual commands wait a few seconds:
// having typed config pull, waiting two seconds beats getting "busy" back.
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
