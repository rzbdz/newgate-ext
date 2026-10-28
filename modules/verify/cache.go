package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/paths"
)

// 指纹的落盘缓存。
//
// # 为什么存在
//
// 不是省 token（verify 是显式动作，每次都要真打），而是**界面上的那一格**：
// 用户打开模型下拉时要立刻看到「上一次验到的是谁」。把结论放在内存里，进程一
// 重启就没了——而 daemon 是长命的、CLI 是短命的，写一次命令再打开网页是最常见
// 的顺序。
//
// # 为什么每次读写都回盘
//
// 同一个文件有两个写者：网页所在的 daemon，和用户可能在终端里敲的 `newgate
// verify`（每条命令都是一个新进程）。若把文件读进内存就不再回看，终端里刚验过
// 的那一条在网页上永远不出现——而两份都是我们对同一件事的说法。文件只有几十
// KB，每次读一遍是这里最便宜的正确性。

const cacheVersion = 1

type cacheFile struct {
	Version int              `json:"version"`
	Entries map[string]Entry `json:"entries"`
}

type cacheStore struct {
	path string
	mu   sync.Mutex
}

func newCacheStore() *cacheStore {
	return &cacheStore{path: filepath.Join(paths.Root(), "fingerprints", "cache.json")}
}

// dataPath 是 lm-detector 参考库的缓存位置。放在 newgate 的配置根下而不是
// 用户自己的 `~/.cache/lmfpd`：这一份数据是**这个产品**的运行时文件，与
// providers.json 一样该在一处能找到；换 NEWGATE_HOME 时整套东西跟着走。
func (c *cacheStore) dataPath() string {
	return filepath.Join(filepath.Dir(c.path), "data.bin")
}

// dataReady 报告参考库缓存文件在不在。它只解释「第一次验证要不要下载」；
// 能不能真的读进来由 detector 那一步回答。
func (c *cacheStore) dataReady() bool {
	_, err := os.Stat(c.dataPath())
	return err == nil
}

func (c *cacheStore) read() (map[string]Entry, error) {
	out := map[string]Entry{}
	raw, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, i18n.Ef(err, "read fingerprint cache {path}: {err}",
			i18n.A{"path": c.path, "err": err})
	}
	var f cacheFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return out, i18n.Ef(err, "parse fingerprint cache {path}: {err}",
			i18n.A{"path": c.path, "err": err})
	}
	if f.Entries != nil {
		out = f.Entries
	}
	return out, nil
}

// list 按 provider/model 排序返回全部结论。
func (c *cacheStore) list() ([]Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := c.read()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Entry, 0, len(keys))
	for _, k := range keys {
		out = append(out, entries[k])
	}
	return out, nil
}

func (c *cacheStore) get(provider, model string) (Entry, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := c.read()
	if err != nil {
		return Entry{}, false, err
	}
	e, ok := entries[provider+"/"+model]
	return e, ok, nil
}

// put 合并一条结论并落盘。
//
// 合并而不是覆盖整张表：验证是并发的（CLI 一次验好几个、网页上每行一个按钮），
// 后完成的那个不该把先完成的抹掉。锁只在进程内有效；跨进程的合并靠「写之前
// 重新读一遍」——两个进程同时写仍有一次丢失的窗口，而这条缓存丢一格的代价是
// 「下次再看它时再验一次」，不是数据损坏。
func (c *cacheStore) put(e Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := c.read()
	if err != nil {
		return err
	}
	entries[e.Provider+"/"+e.Model] = e
	f := cacheFile{Version: cacheVersion, Entries: entries}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o2770); err != nil {
		return i18n.Ef(err, "create {dir}: {err}", i18n.A{"dir": dir, "err": err})
	}
	// 临时文件名带随机段：两个进程同时写时不能撞在同一个 inode 上（与内核
	// store.writeFileAtomic 同一条理由）。
	tmp, err := os.CreateTemp(dir, ".cache-*.tmp")
	if err != nil {
		return i18n.Ef(err, "create a temporary file in {dir}: {err}", i18n.A{"dir": dir, "err": err})
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o660); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return i18n.Ef(err, "write {path}: {err}", i18n.A{"path": tmpName, "err": err})
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		return i18n.Ef(err, "replace {path}: {err}", i18n.A{"path": c.path, "err": err})
	}
	return nil
}
