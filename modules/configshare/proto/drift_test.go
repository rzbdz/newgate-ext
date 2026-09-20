package proto

import (
	paths "github.com/rzbdz/newgate/modules/config/paths"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// applyFiles 把一份快照"落盘并记账"，模拟一次成功的同步。
func applyFiles(t *testing.T, files map[string][]byte) map[string]string {
	t.Helper()
	res, err := Materialize(paths.Config(), files, nil)
	if err != nil {
		t.Fatalf("落盘失败: %v", err)
	}
	return res.Manifest
}

func TestDetectDriftClean(t *testing.T) {
	sandbox(t)
	files := snapshotFiles()
	applied := applyFiles(t, files)

	rep := DetectDrift(paths.Config(), applied, files)
	if !rep.Empty() {
		t.Fatalf("刚同步完就该是干净的，实际 %+v", rep)
	}
	if rep.Blocking() {
		t.Fatal("干净状态不该阻断")
	}
}

func TestDetectDriftModifiedBlocks(t *testing.T) {
	sandbox(t)
	files := snapshotFiles()
	applied := applyFiles(t, files)

	// 有人手改了 providers.json，而远端这一轮也是别的内容——写下去就会抹掉他。
	writeLive(t, ProvidersName, []byte(`{"providers":{"mine":{"base_url":"http://localhost"}}}`))
	incoming := snapshotFiles()
	incoming[ProvidersName] = []byte(`{"providers":{"远程":{"base_url":"https://x"}}}`)

	rep := DetectDrift(paths.Config(), applied, incoming)
	if len(rep.Modified) != 1 || rep.Modified[0] != ProvidersName {
		t.Fatalf("该恰好报出 providers.json 被改过，实际 %+v", rep)
	}
	if !rep.Blocking() {
		t.Fatal("被改过的托管文件必须阻断——否则下一轮就把它静默覆盖了")
	}
	if got := rep.Paths(); len(got) != 1 || got[0] != ProvidersName {
		t.Fatalf("Paths() 该给要归档的那一个，实际 %v", got)
	}
}

// TestDetectDriftConvergedIsNotDrift 是一个**边界情形**，判据在这里被定义成
// "materialize 会不会真的改变什么"，而不是"文件被动过没有"。
//
// 用户手改的内容恰好等于远端现在的版本：写下去什么也没破坏（用户的版本与权威
// 一致了）。拦下来只会逼他为了"接受一个本来就一样的版本"去做一次 --force——
// 那是一次没有意义的、可能丢东西的操作。
func TestDetectDriftConvergedIsNotDrift(t *testing.T) {
	sandbox(t)
	files := snapshotFiles()
	applied := applyFiles(t, files)

	// 用户先改成了 B……
	converged := []byte(`{"providers":{"same-as-remote":{"base_url":"https://same"}}}`)
	writeLive(t, ProvidersName, converged)
	// ……而远端这一轮也正好是 B。
	incoming := snapshotFiles()
	incoming[ProvidersName] = converged

	rep := DetectDrift(paths.Config(), applied, incoming)
	if rep.Blocking() {
		t.Fatalf("手改结果与远端一致时不该阻断（写下去等于没变），实际 %+v", rep)
	}
}

func TestDetectDriftDeleted(t *testing.T) {
	sandbox(t)
	files := snapshotFiles()
	applied := applyFiles(t, files)

	// 删掉我们写过的档位。
	if err := os.Remove(filepath.Join(paths.Config(), "mappings", "prod.json")); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	t.Run("远端还有它：阻断（否则会把它复活）", func(t *testing.T) {
		rep := DetectDrift(paths.Config(), applied, snapshotFiles())
		if len(rep.Deleted) != 1 || rep.Deleted[0] != "mappings/prod.json" {
			t.Fatalf("该报出被删的那个文件，实际 %+v", rep)
		}
		if !rep.Blocking() {
			t.Fatal("远端仍有它时必须阻断：materialize 会把它重新写出来，推翻用户的删除")
		}
	})
	t.Run("远端也没有它：收敛，不阻断", func(t *testing.T) {
		incoming := snapshotFiles()
		delete(incoming, "mappings/prod.json")
		rep := DetectDrift(paths.Config(), applied, incoming)
		if rep.Blocking() {
			t.Fatalf("远端也下线了它，双方一致，不该阻断，实际 %+v", rep)
		}
	})
}

// TestDetectDriftLocalLayerIsReportedNotBlocking 是"两层配置"在漂移守卫里的
// 落点：本地层（本机私有的档位文件）必须**只报告、不阻断**。
//
// 否则"本地定制"和"自动同步"就无法共存——用户要么放弃本地层，要么放弃同步。
// 而报告是必须的：不报就是 silent-override trap（远端同步成功了、本机行为
// 却由另一个文件决定，且没有任何信号）。
func TestDetectDriftLocalLayerIsReportedNotBlocking(t *testing.T) {
	sandbox(t)
	files := snapshotFiles()
	applied := applyFiles(t, files)

	writeLive(t, "mappings/local-only.json", []byte(`{"name":"local-only","roles":{}}`))

	rep := DetectDrift(paths.Config(), applied, snapshotFiles())
	if rep.Blocking() {
		t.Fatalf("本地层不该阻断同步，实际 %+v", rep)
	}
	if len(rep.Extra) != 1 || rep.Extra[0] != "mappings/local-only.json" {
		t.Fatalf("本地层必须被报告出来，实际 %+v", rep)
	}
	if rep.Empty() {
		t.Fatal("有本地层就不算 Empty——status 要显示它")
	}
}

// TestDetectDriftShadowed 是最阴的一种：store.readProfileFile 先看 .kv 再看 .json，
// 所以一个本地的 prod.kv 会让托管的 prod.json **写了也不生效**。
// 远端同步成功、本机行为不变、没有任何报错——必须当成阻断性漂移。
func TestDetectDriftShadowed(t *testing.T) {
	sandbox(t)
	// 快照里只有 .json 形态。
	files := map[string][]byte{
		ProvidersName:        validProviders(),
		"mappings/prod.json": validProfile(),
	}
	applied := applyFiles(t, files)

	// 本机有人写了一个同名的 .kv。
	writeLive(t, "mappings/prod.kv", []byte("normal = smt-deepseek/deepseek-chat\n"))

	rep := DetectDrift(paths.Config(), applied, files)
	if len(rep.Shadowed) != 1 || rep.Shadowed[0] != "mappings/prod.json" {
		t.Fatalf("该报出被本地 .kv 盖住的那个托管文件，实际 %+v", rep)
	}
	if !rep.Blocking() {
		t.Fatal("被盖住的托管文件必须阻断：写下去不生效，等于在撒谎说同步成功了")
	}
}

func TestDetectDriftShadowIgnoredWhenKVIsAlsoOurs(t *testing.T) {
	sandbox(t)
	// 两个形态都由宿主管：优先级由宿主自己决定，不是漂移。
	files := map[string][]byte{
		"mappings/prod.json": validProfile(),
		"mappings/prod.kv":   []byte("normal = smt-deepseek/deepseek-chat\n"),
	}
	applied := applyFiles(t, files)

	rep := DetectDrift(paths.Config(), applied, files)
	if len(rep.Shadowed) != 0 {
		t.Fatalf("两边都是托管的就不算漂移，实际 %+v", rep)
	}
	if rep.Blocking() {
		t.Fatalf("不该阻断，实际 %+v", rep)
	}
}

func TestArchiveFiles(t *testing.T) {
	sandbox(t)
	writeLive(t, ProvidersName, []byte("本地版本"))
	writeLive(t, "mappings/prod.json", []byte("本地档位"))

	dir, err := ArchiveFiles(paths.Config(), "20260917-214512",
		[]string{ProvidersName, "mappings/prod.json", "mappings/gone.json"})
	if err != nil {
		t.Fatalf("归档失败: %v", err)
	}
	if !strings.HasSuffix(dir, filepath.Join("backups", "configshare-20260917-214512")) {
		t.Fatalf("归档目录名不对: %s", dir)
	}
	// 内容必须原样保留（--force 会覆盖用户的改动，但**不能**让它成为不可恢复
	// 的操作——归档是这条命令能被安全地敲下去的前提）。
	if got, err := os.ReadFile(filepath.Join(dir, ProvidersName)); err != nil || string(got) != "本地版本" {
		t.Fatalf("归档的 providers.json 内容不对: %q / %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "mappings", "prod.json")); err != nil || string(got) != "本地档位" {
		t.Fatalf("归档该保留相对结构: %q / %v", got, err)
	}
	// 已经被删掉的文件跳过（没什么可存的），不该因此报错。
	if _, err := os.Stat(filepath.Join(dir, "mappings", "gone.json")); !os.IsNotExist(err) {
		t.Fatal("不存在的文件不该被归档出来")
	}
}

func TestArchiveFilesEmpty(t *testing.T) {
	sandbox(t)
	dir, err := ArchiveFiles(paths.Config(), "20260917-214512", nil)
	if err != nil || dir != "" {
		t.Fatalf("没有要归档的东西时该给空目录名、不报错，实际 %q / %v", dir, err)
	}
}
