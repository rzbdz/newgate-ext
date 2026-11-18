package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Rotator 一个带大小上限的日志写入器。写满就轮转，只保留 keep 份历史。
//
// 必须有：debug 模式下单条请求可能记 8KB+（opencode 的系统提示就有 97KB），
// 没有上限的话磁盘很快就满。
type Rotator struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	f        *os.File
	size     int64
}

func New(path string, maxBytes int64, keep int) (*Rotator, error) {
	r := &Rotator{path: path, maxBytes: maxBytes, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Rotator) open() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			// 轮转失败也别丢日志，继续往当前文件写。
			// `newgate: ` 那个前缀是程序名（机器标记），留在消息外面。
			fmt.Fprintf(os.Stderr, "newgate: %s\n",
				i18n.T("log rotation failed: {err}", i18n.A{"err": err}))
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate 把 x.log → x.log.1 → x.log.2 …，超出 keep 的删掉。
//
// 失败必须报出来：轮转不发生时没有任何现象，只是日志文件一直长——而日志是
// 这个项目排查问题的唯一手段（CLAUDE.md §5），一个悄悄涨到几 GB 的日志既
// 拖慢 grep 也可能把盘写满。所以这里把每一步的错误收起来返回，由 Write 打
// 到 stderr（原来每个 `_ =` 后面都是空的）。
//
// 「源文件不存在」不算错：还没轮到那么多代，或者上一轮已经删过——那是
// 正常状态，报出来只会刷屏。
func (r *Rotator) rotate() error {
	if r.f != nil {
		_ = r.f.Close()
		r.f = nil
	}
	// 从旧到新挪，避免覆盖
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep))
	for i := r.keep - 1; i >= 1; i-- {
		from, to := fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return i18n.Ef(err, "rotating {from} → {to}: {err}",
				i18n.A{"from": from, "to": to})
		}
	}
	// 这一条是真正要紧的：它失败 = 当前日志没被挪走 = 之后继续往同一个文件写，
	// 于是 maxBytes 形同虚设。
	if err := os.Rename(r.path, r.path+".1"); err != nil && !os.IsNotExist(err) {
		return i18n.Ef(err, "rotating {from} → {to}.1: {err}",
			i18n.A{"from": r.path, "to": r.path})
	}
	return r.open()
}

func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// PruneDir 只保留目录下最新的 keep 组文件（按前缀分组计数）。
// 用于限制错误证据 dump 的数量。
func PruneDir(dir string, keep int) { PruneDirBy(dir, "", keep) }

// PruneDirBy 只清理以 prefix 开头的文件组（空前缀 = 全部）。dump 的
// req-* 和错误证据的 err-* 共用一个目录、各自设上限，互不删对方。
//
// 一组 = 同一个 base（"err-400-req001840" 这样，取到第一个 "." 为止）
// 下的那几个文件；组的年龄取组内**最新**那个文件的 mtime——一组是刚落地
// 的，只要还有一份文件在，这组就算「新」，所以用最新的那份代表它（用
// os.Stat，不依赖文件系统给的目录顺序）。
//
// 原来那版把 base 按名字字典序排、删 names[:len-keep]，留下的是「名字
// 最大」的 keep 组，不是「最新」的 keep 组。名字里带状态码时
// err-400 < err-402 < … < err-502，字典序最小的那组永远第一个被删。
// 2026-09-22 的现场：日志里 15 次「上游 400，完整证据已存」，磁盘上
// err-400-* 一个不剩——因为 saveErrEvidence 结尾就调本函数，刚写完的
// err-400-* 当场被自己删掉，那正是字典序最小的一组。
//
// 排序：mtime 降序；mtime 相同用名字升序做稳定 tiebreak。os.Stat 失败的
// 那组算「年龄未知」，排在最新一侧（不会被优先删）：本函数存在的意义是
// 给盘占住上限，而它保护的证据删掉就再也拿不回来（CLAUDE.md §5 说它是
// 排查「是不是代理改坏了请求」唯一能拿出手的东西）。两害相权，宁可多留
// 一组（下一次 prune 或人工清都能回收，总量也仍受 keep 约束），也不肯在
// 没有任何年龄依据时先动手删掉可能正是刚落地的那组。
func PruneDirBy(dir, prefix string, keep int) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	// 按 "err-400-req000003" 这样的前缀分组，一组是四个文件。组里只要
	// 还有一份文件能 stat 到，这组就有年龄；全都 stat 不到才算未知。
	type group struct {
		base     string
		mtime    time.Time
		hasMtime bool
	}
	byBase := map[string]*group{}
	var groups []*group
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if prefix != "" && !strings.HasPrefix(n, prefix) {
			continue
		}
		base := n
		if i := indexAny(n, "."); i > 0 {
			base = n[:i]
		}
		g := byBase[base]
		if g == nil {
			g = &group{base: base}
			byBase[base] = g
			groups = append(groups, g)
		}
		if fi, err := os.Stat(filepath.Join(dir, n)); err == nil {
			if mt := fi.ModTime(); !g.hasMtime || mt.After(g.mtime) {
				g.mtime, g.hasMtime = mt, true
			}
		}
	}
	if len(groups) <= keep {
		return
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.hasMtime != b.hasMtime {
			return !a.hasMtime // 年龄未知的排最新
		}
		if a.hasMtime && !a.mtime.Equal(b.mtime) {
			return a.mtime.After(b.mtime)
		}
		return a.base < b.base
	})
	for _, g := range groups[keep:] {
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			if len(e.Name()) >= len(g.base) && e.Name()[:len(g.base)] == g.base {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
}

func indexAny(s, chars string) int {
	for i := 0; i < len(s); i++ {
		for j := 0; j < len(chars); j++ {
			if s[i] == chars[j] {
				return i
			}
		}
	}
	return -1
}
