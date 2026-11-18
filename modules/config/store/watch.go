package store

import (
	"crypto/sha256"
	"encoding/hex"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/roleprov"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Watcher 在内存里持有配置快照，后台监测磁盘变化并原子换页。
//
// 为什么需要它（这是服务稳定性的地基）：
//
//  1. **不打断正在跑的会话。** 改了 claude 的 profile，正在跑的 opencode
//     毫不受影响——它们读的是各自 agent 的绑定，而且换页是原子的，
//     不存在「读到一半配置变了」的中间态。
//
//  2. **坏配置不致命。** 手改坏一个 JSON，新快照加载失败时**保留旧快照**
//     并大声告警。正在跑的一切继续工作。如果每个请求都直接读盘，一个
//     手误就能让所有 agent 同时报错——那不是可以接受的失败模式。
//
//  3. **热路径零 IO。** 请求侧只做一次原子指针读，不碰文件系统。
//
// 用轮询 mtime 而不是 inotify：零外部依赖，且 inotify 在 NFS / 容器
// 挂载上本来就不可靠，而配置目录往往正在这类文件系统上。
type Watcher struct {
	cur      atomic.Value // *Snapshot
	interval time.Duration

	mu       sync.Mutex
	lastSig  string
	stop     chan struct{}
	stopped  bool
	onChange func(old, new *Snapshot)
	onError  func(error)
	// generation 每次成功换页 +1，用于日志和 status。
	generation int64
}

// NewWatcher 立刻做一次同步加载，失败就返回错误（启动期就该发现问题）。
func NewWatcher(interval time.Duration) (*Watcher, error) {
	if interval <= 0 {
		interval = time.Second
	}
	w := &Watcher{interval: interval, stop: make(chan struct{})}
	snap, err := Load()
	if err != nil {
		return nil, err
	}
	w.cur.Store(snap)
	w.lastSig = signature()
	w.generation = 1
	return w, nil
}

// OnChange / OnError 注册回调。必须在 Start 之前调用。
func (w *Watcher) OnChange(f func(old, new *Snapshot)) { w.onChange = f }
func (w *Watcher) OnError(f func(error))               { w.onError = f }

// Current 当前快照。热路径调用，无锁、无 IO。
func (w *Watcher) Current() *Snapshot { return w.cur.Load().(*Snapshot) }

// Generation 已成功换页多少次。
func (w *Watcher) Generation() int64 { return atomic.LoadInt64(&w.generation) }

// Start 起后台轮询。
func (w *Watcher) Start() {
	go func() {
		t := time.NewTicker(w.interval)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-t.C:
				w.Reload(false)
			}
		}
	}()
}

func (w *Watcher) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.stopped {
		w.stopped = true
		close(w.stop)
	}
}

// Reload 检查磁盘是否变化并换页。force 为真时无条件重载。
//
// 关键：加载失败**不动**当前快照。宁可用旧配置继续跑，也不能因为
// 一次手误让所有 agent 同时失效。
func (w *Watcher) Reload(force bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	sig := signature()
	if !force && sig == w.lastSig {
		return false
	}

	snap, err := Load()
	if err != nil {
		// 记下签名，避免对同一个坏文件反复报错刷屏；
		// 但**不**替换快照——正在跑的会话继续用旧配置。
		w.lastSig = sig
		if w.onError != nil {
			w.onError(err)
		}
		return false
	}

	old := w.cur.Load().(*Snapshot)
	w.cur.Store(snap)
	w.lastSig = sig
	atomic.AddInt64(&w.generation, 1)
	if w.onChange != nil {
		w.onChange(old, snap)
	}
	return true
}

// signature 配置目录的指纹：所有相关文件的 (路径, 大小, mtime) 哈希。
//
// 用 mtime+size 而不是内容哈希：配置文件可能上百个，每秒读一遍内容
// 没必要。代价是同一秒内的原地改写可能漏检——但我们自己的写入都走
// 原子 rename（mtime 必变），手工编辑器也几乎都是 rename。
func signature() string {
	h := sha256.New()
	var files []string
	// omo-slots.json 也是配置：它是模块贡献的角色键（omo-sisyphus 跟哪一档走）
	// 的唯一来源。接管 omo 会写它，改了就该在 1 秒内生效（见 core/roleprov）。
	files = append(files, paths.ProvidersFile(), paths.StateFile())
	files = append(files, roleprov.WatchFiles()...)
	if ents, err := ioutil.ReadDir(paths.Mappings()); err == nil {
		var names []string
		for _, e := range ents {
			if !e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names) // 顺序必须稳定，否则指纹会假变化
		for _, n := range names {
			files = append(files, filepath.Join(paths.Mappings(), n))
		}
	}
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			h.Write([]byte(f + "|missing\n"))
			continue
		}
		h.Write([]byte(f + "|" +
			fi.ModTime().UTC().Format(time.RFC3339Nano) + "|" +
			itoa(fi.Size()) + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// DiffSummary 人类可读的变更摘要，给日志用。
// 只报**结构性**变化，不打印任何密钥。
func DiffSummary(old, new *Snapshot) []string {
	var out []string
	if old == nil || new == nil {
		return out
	}
	if old.State.DefaultProfile != new.State.DefaultProfile {
		out = append(out, i18n.T("default profile: {from} → {to}", i18n.A{
			"from": old.State.DefaultProfile, "to": new.State.DefaultProfile}))
	}
	for agent, p := range new.State.Active {
		if old.State.Active[agent] != p {
			out = append(out, i18n.T("agent {agent} → profile {profile}",
				i18n.A{"agent": agent, "profile": p}))
		}
	}
	for agent := range old.State.Active {
		if _, still := new.State.Active[agent]; !still {
			out = append(out, i18n.T("agent {agent} falls back to the default profile",
				i18n.A{"agent": agent}))
		}
	}
	if len(old.Profiles) != len(new.Profiles) {
		out = append(out, i18n.T("the number of profiles changed", nil))
	}
	if len(old.Providers.Providers) != len(new.Providers.Providers) {
		out = append(out, i18n.T("the number of providers changed", nil))
	}
	return out
}
