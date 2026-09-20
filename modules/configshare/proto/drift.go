package proto

import (
	"crypto/sha256"
	"encoding/hex"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/rzbdz/newgate/lib/i18n"
	paths "github.com/rzbdz/newgate/modules/config/paths"
)

// DriftReport 是本机托管文件相对"我们上次写下去的样子"的偏离。
//
// # 它要堵的失效模式
//
// 副本上有人手改了 providers.json，以为生效了，下一个轮询周期把它**静默**
// 覆盖掉。用户的认知和实际分叉了，而且**没有任何信号**——直接违反本仓库
// "不静默"的硬要求。"一定要设计好宿主的概念，否则大家乱改 config"这句要求
// 的正解就是这个守卫：权威是单写者（宿主），副本上的改动不是错误，但必须
// 被看见、被正式处理，不能被悄悄抹掉。
//
// # 为什么必须哈希内容，不能 stat
//
// watch.go 自己的注释就写着 mtime+size 的局限（同一秒内的原地改写可能漏检）。
// 这里判的是"人改了内容"，只能靠内容哈希。
type DriftReport struct {
	// Modified 我们写过的文件，内容被改成了别的东西。
	Modified []string `json:"modified,omitempty"`
	// Deleted 我们写过的文件被删了，而远端还有它（会被重新写出来）。
	Deleted []string `json:"deleted,omitempty"`
	// Shadowed 托管的 `mappings/x.json` 被一个**本地的** `mappings/x.kv` 盖住——
	// 远端同步成功但本机行为不变。最阴的一种，必须报。
	Shadowed []string `json:"shadowed,omitempty"`
	// Extra 命中托管集合、但不归我们管的本地文件（= 正式的"本地层"）。
	// **只报告、不阻断**，见下面 Blocking 的说明。
	Extra []string `json:"extra,omitempty"`

	At time.Time `json:"at,omitempty"`
}

// Blocking 是否会阻断应用。
//
// 为什么 Extra **不**阻断：本设计明确定义了"两层——共享层（托管）+ 机器本地层
// （私有，永不入仓库、永不被覆盖）"（见 docs/13-config-share.md）。本地层在
// 文件系统上的形态就是"托管集合里多出来的本地文件"（比如本机专用的
// `mappings/local.json`）。如果它在托管集合里就阻断同步，那么本地层和自动同步
// 就无法共存——用户要么放弃本地定制，要么放弃同步。正确做法是：**只报告**
// （status 里显示出来，避免 silent-override trap），不阻断。
//
// 阻断的三种都是"materialize 会破坏用户已经做出来的东西 / 会做一件不生效的
// 事"：改过的会被覆盖、删了的会被复活、被盖住的写了等于没写。
func (d *DriftReport) Blocking() bool {
	if d == nil {
		return false
	}
	return len(d.Modified)+len(d.Deleted)+len(d.Shadowed) > 0
}

// Empty 什么都没发现（干净的副本）。
func (d *DriftReport) Empty() bool {
	if d == nil {
		return true
	}
	return len(d.Modified)+len(d.Deleted)+len(d.Shadowed)+len(d.Extra) == 0
}

// Paths 是阻断性漂移涉及的文件（归档就归档这些；别的没被动过）。
func (d *DriftReport) Paths() []string {
	if d == nil {
		return nil
	}
	out := append([]string(nil), d.Modified...)
	out = append(out, d.Deleted...)
	out = append(out, d.Shadowed...)
	sort.Strings(out)
	return out
}

// DetectDrift 比对"我们写下去的样子"（applied 清单）、本机现状、以及这一轮
// 要写下去的内容。
//
// incoming 传内容而不是路径列表，是为了分辨一个边界情形：用户手改的字节**恰好
// 等于**远端现在的字节。那时写下去什么也没破坏（user 的版本与权威一致了），
// 不该拦——拦了反而要用户为了"接受一个本来就一样的版本"去做一次 --force。
// 判据因此是"materialize 会不会真的改变什么"，而不是"文件被动过没有"。
func DetectDrift(dir string, applied map[string]string, incoming map[string][]byte) DriftReport {
	var rep DriftReport
	rep.At = time.Now()

	ours := make(map[string]bool, len(applied))
	// 先把清单排个序，报告才是稳定顺序（测试与 status 输出都靠它）。
	paths := make([]string, 0, len(applied))
	for rel := range applied {
		if _, ok := ManagedPath(rel); !ok {
			continue // 老版本写下的、现已不在托管集合里的记录：忽略
		}
		ours[rel] = true
		paths = append(paths, rel)
	}
	sort.Strings(paths)

	for _, rel := range paths {
		live, err := ioutil.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			if isNotExist(err) {
				// 被删了。远端还有它 → materialize 会把它复活，那会推翻用户的
				// 删除动作；远端也没有它 → 收敛，无事发生。
				if _, still := incoming[rel]; still {
					rep.Deleted = append(rep.Deleted, rel)
				}
				continue
			}
			// 读不了（权限/是目录）：当成漂移报出来，别默默跳过。
			rep.Modified = append(rep.Modified, rel)
			continue
		}
		if hashBytes(live) == applied[rel] {
			continue // 这就是我们写下去的样子
		}
		if want, ok := incoming[rel]; ok && hashBytes(live) == hashBytes(want) {
			continue // 手改的结果与远端一致，写下去等于没变
		}
		rep.Modified = append(rep.Modified, rel)
	}

	// 影子：托管的 mappings/x.json 被本地的 mappings/x.kv 盖住。
	incomingPaths := make([]string, 0, len(incoming))
	for rel := range incoming {
		incomingPaths = append(incomingPaths, rel)
	}
	sort.Strings(incomingPaths)
	for _, rel := range incomingPaths {
		sh := shadowOf(rel)
		if sh == "" || ours[sh] {
			continue // 两边都是托管的：宿主自己管着这个优先级，不是漂移
		}
		if fileExists(filepath.Join(dir, sh)) {
			rep.Shadowed = append(rep.Shadowed, rel)
		}
	}

	// 本地层：托管集合里多出来的、不归我们管的文件。
	if live, err := ListManagedFiles(dir); err == nil {
		for _, rel := range live {
			if ours[rel] {
				continue
			}
			if _, inSnapshot := incoming[rel]; inSnapshot {
				continue // 远端要开始管它了（首次接管），不算本地层
			}
			rep.Extra = append(rep.Extra, rel)
		}
	}
	return rep
}

// ArchiveFiles 把给定的托管文件复制到 backups/configshare-<stamp>/ 下（保留相对
// 结构），返回归档目录。找不到的文件跳过（它已经被删了，没什么可存的）。
//
// 沿用既有的 backups/<时间戳>/ 约定与"永不删"精神：--force 会覆盖用户的改动，
// 但**不能**让它成为不可恢复的操作。归档是这条命令能被安全地敲下去的前提。
func ArchiveFiles(dir, stamp string, rels []string) (string, error) {
	if len(rels) == 0 {
		return "", nil
	}
	dst := filepath.Join(paths.BackupDir(), "configshare-"+stamp)
	for _, rel := range rels {
		src := filepath.Join(dir, rel)
		data, err := ioutil.ReadFile(src)
		if err != nil {
			if isNotExist(err) {
				continue
			}
			return "", i18n.Ef(err, "cannot archive {path}: {err}", i18n.A{"path": rel})
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o2770); err != nil {
			return "", err
		}
		if err := ioutil.WriteFile(target, data, 0o660); err != nil {
			return "", i18n.Ef(err, "cannot archive {path}: {err}", i18n.A{"path": rel})
		}
	}
	return dst, nil
}

// ---------- 小工具 ----------

// readFileIfExists 读文件；不存在返回 (nil, nil) 而不是错误。
//
// 为什么需要这个区分：漂移比对要问的是"这个文件有没有被本地改过"，而
// **不存在**与**读不了**是两件事——不存在是全新机器（正常），读不了（权限、
// 是目录）才是要报的。用 os.IsNotExist 逐个判会把调用点写得很啰嗦。
func readFileIfExists(path string) ([]byte, error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		if isNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return b, nil
}

// driftOrNil 把一份空报告折成 nil。
//
// 记账里用 nil 表示"干净"，用非 nil 表示"有东西要显示"——空结构体与非 nil 的
// 空报告在 JSON 里长得不一样（`{}` vs 缺席），而 status 判的是 nil。折算只有
// 这一个实现，免得某条路径写出一个"看起来有漂移、其实什么都没有"的记录。
func driftOrNil(d DriftReport) *DriftReport {
	if d.Empty() {
		return nil
	}
	return &d
}

// mergeDrift 把两份漂移并成一份（给 status 显示用）。
//
// 两个通道（配置、密钥）各有各的漂移，存储上**分开**（见 state.go 的
// SecretsDrift），只在展示时合并——用户关心的是"我这台机器上有什么没进共享"，
// 不关心它是哪个通道报的。
func mergeDrift(old, add *DriftReport) *DriftReport {
	if add.Empty() {
		return old
	}
	if old.Empty() {
		return add
	}
	return &DriftReport{
		Modified: append(append([]string(nil), old.Modified...), add.Modified...),
		Deleted:  append(append([]string(nil), old.Deleted...), add.Deleted...),
		Shadowed: append(append([]string(nil), old.Shadowed...), add.Shadowed...),
		Extra:    append(append([]string(nil), old.Extra...), add.Extra...),
		At:       add.At,
	}
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isNotExist(err error) bool { return os.IsNotExist(err) }

// stamp 是归档目录名里的时间戳（20260917-214512）。
func stamp(t time.Time) string { return t.Format("20060102-150405") }
