package store

// 写盘之前留一份「上一版」。
//
// # 为什么要有它
//
// 2026-09-20 现场：一个前端 bug 把用户某个 profile 的档位写没了（`mid=/模型名`
// 这种 provider 为空的绑定落进了配置）。发现的时候，**没有任何地方能找回写之前
// 那一份**——`backups/` 是接管时给 opencode/claude 配置用的，与配置文件无关；
// git 也没有（配置目录不在版本控制里）。于是「改坏了」等于「重新凭记忆写一遍」。
//
// 判据是**代价不对称**：留备份的成本是每次写多一次小文件的复制（几 KB），而丢掉
// 一次的代价是用户手上的配置没了。所以这件事不该由调用方记得做——它必须在**写入
// 的唯一出口**上，谁都绕不过去。
//
// # 形状
//
// 每个被写过的文件一个目录（路径里的 `/` 换成 `__`），里面按时间命名，**只留最近
// 20 版**。20 这个数是「够回溯到发现问题之前」与「不会把配置目录撑大」之间的取舍：
// 一次编辑会话通常几次保存，20 版够翻回半小时到几小时之前。
//
// # 失败方向
//
// 备份失败**不阻止写盘**（fail-open）。为了留一份历史而让用户的保存失败，是把
// 安全网变成了路障——而安全网的意义正是「主流程出问题时还在」。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
)

const (
	// historyDirName 放在配置根下（与 `.write.lock` 同一种做法：点开头，不与人
	// 会去翻的那些目录混在一起）。
	historyDirName = ".history"
	// historyKeep 是每个文件保留的版本数。
	historyKeep = 20
)

// snapshotBeforeWrite 把 path **此刻**的内容抄一份进历史。
//
// 调用点必须在「目标已经被替换」之前。文件不存在（新建）或读不出来时什么都不做
// ——没有「上一版」可留，这不是错误。
func snapshotBeforeWrite(path string) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return
	}
	dir, err := historyDirFor(path)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o2770); err != nil {
		return
	}
	// 文件名按时间排：UnixNano 单调、可排序，同一纳秒内的两次写会撞名——撞了就
	// 退一格（多写一纳秒），不必为此上锁。
	name := time.Now().UnixNano()
	for i := 0; i < 100; i++ {
		p := filepath.Join(dir, fmt.Sprintf("%d.bak", name+int64(i)))
		if _, err := os.Stat(p); os.IsNotExist(err) {
			_ = os.WriteFile(p, b, 0o660)
			break
		}
	}
	pruneHistory(dir)
}

// pruneHistory 只留最近 historyKeep 版。删除失败不管：多留几版比删错强。
func pruneHistory(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var stamps []int64
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".bak") {
			continue
		}
		// 名字是 UnixNano；解析不出来的（人手放进去的东西）**不动它**——那不是
		// 我们写的，删它没有道理。
		var n int64
		if _, err := fmt.Sscanf(e.Name(), "%d.bak", &n); err == nil {
			stamps = append(stamps, n)
		}
	}
	if len(stamps) <= historyKeep {
		return
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i] < stamps[j] })
	for _, n := range stamps[:len(stamps)-historyKeep] {
		_ = os.Remove(filepath.Join(dir, fmt.Sprintf("%d.bak", n)))
	}
}

// historyDirFor 把配置文件路径映射成历史目录。
func historyDirFor(path string) (string, error) {
	root, err := filepath.Abs(paths.Root())
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	// 路径拼成一个目录名：`mappings/demo.kv` → `mappings__demo.kv`。用一个不可能
	// 出现在文件名里的分隔（路径分隔符本身已被换掉）。
	flat := strings.ReplaceAll(filepath.ToSlash(rel), "/", "__")
	if flat == "" || strings.HasPrefix(flat, "..") {
		return "", fmt.Errorf("store: %s is outside the config root", path)
	}
	return filepath.Join(root, historyDirName, flat), nil
}

// HistoryDir 是某个配置文件的历史目录（给测试与「去哪儿找备份」用）。
func HistoryDir(path string) (string, error) { return historyDirFor(path) }

// HistoryEntries 列出某个配置文件的历史版本，**从新到旧**。
func HistoryEntries(path string) []string {
	dir, err := historyDirFor(path)
	if err != nil {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".bak") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}
