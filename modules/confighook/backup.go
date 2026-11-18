package confighook

import (
	"bytes"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate/lib/i18n"
)

// 本文件是**每个配置接管都要走的那几步**：接管前存一份原件、原子地写回、以及
// 从原件还原。
//
// # 为什么它在内核而不是在某一家客户端的模块里
//
// 它 2026-09-21 之前长在发行版的 modules/opencodeomo 里，而 codex 的接管要写的
// 是同一套东西（先备份再改、写回要原子、逃生舱要能还原）。判据还是那一条
// ——**换个发行版，这东西还该在吗？** 该在：任何一家客户端的任何一份配置文件，
// 接管它的人都要做这三步，步骤本身不含任何一家的知识（哪份文件、算什么「已被
// 接管」，由调用方给）。
//
// 留在 opencodeomo 里的代价是实测过的：codex 的接管只能把这三步抄一遍，而**抄
// 来的那份会漂移**——这里是 `original/` 的写入防护，漂移掉之后症状是「用户的原
// 配置永久丢失」，而它在测试里一点声音都没有。

// TakenOver 判断一份文件的**当前内容**是不是已经被我们接管过。
//
// 判据必须由调用方给，因为「被接管过」长什么样是各家格式的事：opencode.json 里是
// 一个 `"newgate"` provider 键，codex 的 config.toml 里是一个 `[model_providers.newgate]`
// 段。内核不认识这两种格式，也不该认识。
type TakenOver func(content []byte) bool

// OriginalPath 是这个目标文件「没被碰过的那一份」存在哪。
//
// 按**文件名**索引（不带目录）：目标文件常常散落在各处（`~/.codex/config.toml`、
// `~/.config/opencode/opencode.json`），把整条路径搬到 backups/ 底下会长出一棵
// 与用户目录同构的树，而那份树只是给 `off` 用的内部记账。
func OriginalPath(target string) string {
	return filepath.Join(paths.BackupDir(), "original", filepath.Base(target))
}

// BackupFile 接管前把原文件存成 original/（用于还原），同时每次都留一份带时间戳
// 的历史。
//
// 关键防护：**original/ 只允许写「未被接管」的内容**。否则一旦 original/ 被误删，
// 下次接管就会把已接管的文件当成「原始」存进去，之后还原出来的就是被污染的版本
// ——用户的原配置永久丢失。这条不是理论风险：`off` 之后再 `on`、或者用户手工
// 删掉 backups/ 里的某个目录，都能走到这里。
func BackupFile(target string, isTakenOver TakenOver) error {
	b, err := ioutil.ReadFile(target)
	if err != nil {
		return err
	}

	orig := OriginalPath(target)
	if err := os.MkdirAll(filepath.Dir(orig), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(orig); os.IsNotExist(err) {
		if isTakenOver != nil && isTakenOver(b) {
			return i18n.E("refusing to back up: {file} has already been taken over by newgate, but {orig} does not exist.\n"+
				"  Using it as the original backup would lose your original config for good.\n"+
				"  Either put a clean copy from backups/<timestamp>/ back into original/,\n"+
				"  or revert the config by hand and run newgate start again",
				i18n.A{"file": filepath.Base(target), "orig": orig})
		}
		if err := ioutil.WriteFile(orig, b, 0o600); err != nil {
			return err
		}
	}

	ts := filepath.Join(paths.BackupDir(), time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(ts, 0o700); err != nil {
		return err
	}
	if err := ioutil.WriteFile(filepath.Join(ts, filepath.Base(target)), b, 0o600); err != nil {
		return err
	}
	PruneSnapshots(10)
	return nil
}

// PruneSnapshots 只保留最近 keep 份时间戳快照，别让备份目录无限长。
// original/ 永不删——那是逃生舱，删了就没得还原了。
func PruneSnapshots(keep int) {
	ents, err := ioutil.ReadDir(paths.BackupDir())
	if err != nil {
		return
	}
	var stamps []string
	for _, e := range ents {
		if e.IsDir() && e.Name() != "original" {
			stamps = append(stamps, e.Name())
		}
	}
	if len(stamps) <= keep {
		return
	}
	sort.Strings(stamps) // 时间戳格式可直接字典序排序
	for _, s := range stamps[:len(stamps)-keep] {
		_ = os.RemoveAll(filepath.Join(paths.BackupDir(), s))
	}
}

// RestoreFile 把 original/ 那份写回目标。返回是否真的还原过。
//
// 没备份 = 没接管过，不是错误（`off` 对一份我们从没碰过的文件应当无事发生）。
func RestoreFile(target string) (bool, error) {
	b, err := ioutil.ReadFile(OriginalPath(target))
	if err != nil {
		return false, nil
	}
	return true, WriteAtomic(target, b, 0o600)
}

// WriteAtomic 原子地写一份文件：先写同目录的临时文件，再 rename 换目录项。
//
// 为什么不能直接覆盖：这些文件是**别人家的程序正在读**的（codex 起一次读一次
// config.toml）。写一半被读到，轻则那一次启动报一个莫名其妙的解析错误，重则
// 我们把用户的配置截断了——而截断的那一份还是我们写的。
func WriteAtomic(path string, content []byte, mode os.FileMode) error {
	tmp := path + ".newgate.tmp"
	if err := ioutil.WriteFile(tmp, content, mode); err != nil {
		return err
	}
	// WriteFile 的 mode 会被 umask 削（root 建的目录给 claude 用时就是 0640），
	// 所以显式 Chmod 一次——与 docs 里那条多用户权限坑同源。
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ContainsMarker 是给 TakenOver 用的小工具：内容里有没有这段字节。
//
// 它存在只是为了别让每个调用方自己写一遍 bytes.Contains（那行本身没信息量，
// 而写错的形式——比如拿 string 转一遍——会在大文件上多一次全量分配）。
func ContainsMarker(content []byte, marker string) bool {
	return bytes.Contains(content, []byte(marker))
}
