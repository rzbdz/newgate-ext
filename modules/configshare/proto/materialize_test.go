package proto

import (
	paths "github.com/rzbdz/newgate/modules/config/paths"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSnapshotRejectsUnmanagedPath(t *testing.T) {
	sandbox(t)
	files := snapshotFiles()
	files["state.json"] = []byte("{}") // 机器本地状态，绝不该从网上写下来
	_, err := ValidateSnapshot(paths.Config(), files)
	if err == nil || !strings.Contains(err.Error(), "managed set") {
		t.Fatalf("托管集合之外的文件必须被拒绝，实际: %v", err)
	}
}

// TestValidateSnapshotRejectsInlineKey 是密钥纪律的地基：配置通道**不含密**。
//
// providers.json 在宿主侧是 git 管的，内联一次就永久进历史。宿主上出现这种
// 情况只有两种可能（版本太旧、有人手改），两种都该被拦下来，而不是让每台副本
// 安静地把明文密钥写进自己的 providers.json。
func TestValidateSnapshotRejectsInlineKey(t *testing.T) {
	sandbox(t)
	files := snapshotFiles()
	files[ProvidersName] = []byte(`{"providers":{
	  "leaky":{"base_url":"https://x","api_key":"sk-明文密钥"},
	  "ok":{"base_url":"https://y","api_key_env":"OK_KEY"}
	}}`)
	_, err := ValidateSnapshot(paths.Config(), files)
	if err == nil {
		t.Fatal("内联 api_key 必须被拒绝")
	}
	if !strings.Contains(err.Error(), "leaky") {
		t.Fatalf("错误文案该点名是哪个 provider: %v", err)
	}
	if !strings.Contains(err.Error(), "secrets channel") {
		t.Fatalf("错误文案该说清密钥该走哪条路: %v", err)
	}
	// api_key_env 是正当的，不该被误伤。
	files[ProvidersName] = []byte(`{"providers":{"ok":{"base_url":"https://y","api_key_env":"OK_KEY"}}}`)
	if _, err := ValidateSnapshot(paths.Config(), files); err != nil {
		t.Fatalf("api_key_env 不该被误判成内联密钥: %v", err)
	}
	// 完全没有密钥来源（等密钥通道）也是正当的。
	files[ProvidersName] = validProviders()
	if _, err := ValidateSnapshot(paths.Config(), files); err != nil {
		t.Fatalf("没有密钥来源不该被拒绝: %v", err)
	}
}

func TestValidateSnapshotRejectsBrokenFiles(t *testing.T) {
	tests := []struct {
		name string
		rel  string
		data string
	}{
		{"providers 不是 JSON", ProvidersName, `{`},
		{"档位 json 坏了", "mappings/prod.json", `{"roles":`},
		{"档位 kv 不是 key=value", "mappings/prod.kv", `这就是一行中文`},
		{"档位 kv 的未知 key", "mappings/prod.kv", "normal=smt-deepseek/x\n没听说过的键=y\n"},
		{"meta 坏了", MetaFileName, `{`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			files := map[string][]byte{tc.rel: []byte(tc.data)}
			if _, err := ValidateSnapshot(paths.Config(), files); err == nil {
				t.Fatalf("%s 该被拒绝", tc.name)
			}
		})
	}
}

func TestValidateSnapshotAcceptsGoodKV(t *testing.T) {
	sandbox(t)
	files := map[string][]byte{
		ProvidersName:      validProviders(),
		"mappings/prod.kv": []byte("# 手写友好\nname=prod\nnormal=smt-deepseek/deepseek-chat\n"),
	}
	if _, err := ValidateSnapshot(paths.Config(), files); err != nil {
		t.Fatalf("合法的 kv 该通过: %v", err)
	}
}

// TestValidateSnapshotWarnsButAccepts 锁住"语义疑点只警告、不拒绝"这条界限。
//
// 一个 provider 名字打错不该让整个车队收不到配置更新（fail-open）。宿主侧的
// publish 会**拒绝**这些——那时是发布者当场改，代价为零；副本侧只报。
func TestValidateSnapshotWarnsButAccepts(t *testing.T) {
	sandbox(t)
	files := map[string][]byte{
		// glm 有 api_key_env（正当），bare 两者都没有（密钥该来自密钥通道）。
		// 只该提醒后者——把有 api_key_env 的也报出来就是假警告，而假警告多了
		// 真警告就没人看了。
		ProvidersName: []byte(`{"providers":{
		  "glm":{"base_url":"https://g","api_key_env":"G"},
		  "bare":{"base_url":"https://b"}
		}}`),
		"mappings/prod.json": []byte(`{"name":"prod","roles":{
		  "normal":[{"provider":"打错的provider","model":"x"}]}}`),
	}
	warns, err := ValidateSnapshot(paths.Config(), files)
	if err != nil {
		t.Fatalf("悬空引用不该拒绝整个快照: %v", err)
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "打错的provider") {
		t.Fatalf("该报出悬空的 provider 引用，实际: %v", warns)
	}
	if !strings.Contains(joined, "provider bare has no api_key_env") {
		t.Fatalf("该提醒 bare 没有密钥来源，实际: %v", warns)
	}
	if strings.Contains(joined, "provider glm") {
		t.Fatalf("glm 有 api_key_env，不该被提醒（这是假警告）: %v", warns)
	}
}

// TestValidateSnapshotNoFalseWarningForLocalExtends：档位 extends 一个**本机
// 私有**的档位是正当用法。不把本地档位算进已知集合的话，每轮都会报一条假
// 警告——假警告多了，真警告就没人看了。
func TestValidateSnapshotNoFalseWarningForLocalExtends(t *testing.T) {
	sandbox(t)
	writeLive(t, "mappings/local-base.json", []byte(`{"name":"local-base","roles":{}}`))
	files := map[string][]byte{
		ProvidersName:        validProviders(),
		"mappings/prod.json": []byte(`{"name":"prod","extends":"local-base","roles":{}}`),
	}
	warns, err := ValidateSnapshot(paths.Config(), files)
	if err != nil {
		t.Fatalf("不该拒绝: %v", err)
	}
	for _, w := range warns {
		if strings.Contains(w, "local-base") {
			t.Fatalf("extends 本机私有的档位是正当的，不该报警告: %v", warns)
		}
	}
}

func TestMaterializeWritesSkipsAndRemoves(t *testing.T) {
	sandbox(t)
	first := snapshotFiles()
	res, err := Materialize(paths.Config(), first, nil)
	if err != nil {
		t.Fatalf("落盘失败: %v", err)
	}
	if len(res.Written) != 3 || len(res.Removed) != 0 {
		t.Fatalf("首次该写 3 个文件，实际 written=%v removed=%v", res.Written, res.Removed)
	}
	for rel := range first {
		if !existsLive(rel) {
			t.Fatalf("%s 没落盘", rel)
		}
	}

	// 第二轮：内容一致 → 一个都不写（不碰 mtime = 不给 watcher 制造假变化）。
	res2, err := Materialize(paths.Config(), first, res.Manifest)
	if err != nil {
		t.Fatalf("第二次落盘失败: %v", err)
	}
	if len(res2.Written) != 0 || len(res2.Unchanged) != 3 {
		t.Fatalf("内容一致时该全部跳过，实际 written=%v unchanged=%v", res2.Written, res2.Unchanged)
	}

	// 第三轮：宿主下线一个档位 + 改一个 provider 表。
	third := snapshotFiles()
	delete(third, "mappings/prod.json")
	third[ProvidersName] = []byte(`{"providers":{"只有这个":{"base_url":"https://only"}}}`)
	res3, err := Materialize(paths.Config(), third, res2.Manifest)
	if err != nil {
		t.Fatalf("第三次落盘失败: %v", err)
	}
	if len(res3.Removed) != 1 || res3.Removed[0] != "mappings/prod.json" {
		t.Fatalf("下线的档位该被删掉，实际 %v", res3.Removed)
	}
	if existsLive("mappings/prod.json") {
		t.Fatal("下线的档位还在——只写不删的话，@prod 之类引用会永远解析得到东西")
	}
	if !strings.Contains(string(readLive(t, ProvidersName)), "只有这个") {
		t.Fatal("改过的 provider 表没落盘")
	}
}

// TestMaterializeKeepsLocalFiles：**只删我们写过的**。本地层的文件永远不该被
// 这个函数碰——那是"两层配置"里属于用户的那一层。
func TestMaterializeKeepsLocalFiles(t *testing.T) {
	sandbox(t)
	writeLive(t, "mappings/local-only.json", []byte(`{"name":"local-only"}`))
	applied := applyFiles(t, snapshotFiles())

	next := snapshotFiles()
	delete(next, "mappings/prod.json") // 连托管的下线一起做，确认只删自己的
	if _, err := Materialize(paths.Config(), next, applied); err != nil {
		t.Fatalf("落盘失败: %v", err)
	}
	if !existsLive("mappings/local-only.json") {
		t.Fatal("本地层的文件被删了——materialize 只该删自己清单里的东西")
	}
}

func TestMaterializeFileMode(t *testing.T) {
	sandbox(t)
	if _, err := Materialize(paths.Config(), snapshotFiles(), nil); err != nil {
		t.Fatalf("落盘失败: %v", err)
	}
	// 0660 而不是靠 umask：CLAUDE.md §3.1 记着这个坑（root 写出的文件被削成 0640，
	// 之后以 claude 跑的 daemon 就写不了）。
	fi, err := os.Stat(filepath.Join(paths.Config(), ProvidersName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o660 {
		t.Fatalf("托管文件权限该是 0660，实际 %v", fi.Mode().Perm())
	}
}

func TestSecretsMaterialize(t *testing.T) {
	sandbox(t)
	path := secretsPath()

	// 还没有文件时读出来是空表，不是错误。
	sf, err := LoadSecretsFile(path)
	if err != nil || len(sf.Keys) != 0 {
		t.Fatalf("缺席该给空表，实际 %+v / %v", sf, err)
	}
	if _, ok := LookupSecret(path, "smt-deepseek"); ok {
		t.Fatal("没有文件时不该查出密钥")
	}

	sum, err := MaterializeSecrets(path, map[string]string{"smt-deepseek": "sk-abc", "glm": "sk-def"})
	if err != nil {
		t.Fatalf("落盘失败: %v", err)
	}
	if v, ok := LookupSecret(path, "smt-deepseek"); !ok || v != "sk-abc" {
		t.Fatalf("查不到刚写的密钥: %q %v", v, ok)
	}
	// 0600：谁能读它谁就能直接花掉那份订阅（providers.json 的 0660 是有意的组内
	// 可见，明文密钥不是）。
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("secrets.json 权限该是 0600，实际 %v", fi.Mode().Perm())
	}

	// 同样的内容再写一次：哈希不变（幂等），文件不被重写。
	again, err := MaterializeSecrets(path, map[string]string{"glm": "sk-def", "smt-deepseek": "sk-abc"})
	if err != nil {
		t.Fatalf("重写失败: %v", err)
	}
	if again != sum {
		t.Fatalf("同样内容该给同样的哈希：%s vs %s（顺序不同也必须一样，否则每轮都认为变了）", again, sum)
	}
}
