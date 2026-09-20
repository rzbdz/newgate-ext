package proto

import (
	paths "github.com/rzbdz/newgate/modules/config/paths"
	"testing"
)

// TestManagedPathAllowlist 锁住"唯一契约"这张表。
//
// 它是三处共用的判据（宿主生成快照、副本校验快照、漂移检测枚举本地文件），
// 所以这里的每一条都有具体来历，不是随手列的。
func TestManagedPathAllowlist(t *testing.T) {
	tests := []struct {
		rel  string
		want bool
		why  string
	}{
		{"providers.json", true, "托管集合的核心"},
		{"newgate-config.json", true, "schema/版本声明，副本也要留一份"},
		{"mappings/prod.json", true, "档位"},
		{"mappings/prod.kv", true, "手写友好的档位形态，同样托管"},
		{"mappings/a-b_c.1.json", true, "档位名里可以有连字符/下划线/点"},

		// 坑 1：profile kv --write 会把旧 json 改名成 .bak。它在托管集合之外，
		// 否则就是每台机器上一堆既不被读、又会被当成"本地改动"的垃圾。
		{"mappings/prod.json.bak", false, "kv 转换留下的备份，不是配置"},

		// 坑 2：omo-slots.json 被 roleprov 盯着、按本机生成，进共享会和本机接管打架。
		{"omo-slots.json", false, "本机接管产物"},
		{"state.json", false, "机器本地状态（进它会和 watcher 打架）"},
		{"health.json", false, "熔断健康表是运行时数据"},
		{"secrets.json", false, "另走密钥通道（独立的记账/代数/权限）"},
		{"thinkcache.bin", false, "推理缓存"},
		{"probe-capabilities.json", false, "本机探活结果"},
		{"dump/x.json", false, "报文证据，永不外传"},
		{"backups/20260917/providers.json", false, "备份目录"},

		// 原子写的临时文件（我们自己造的，也可能有别人留下的）。
		{"providers.json.tmp", false, "写临时文件"},
		{".providers.json.tmp", false, "写临时文件"},
		{"mappings/.prod.json.tmp", false, "写临时文件"},
		{"mappings/.hidden.json", false, "点开头的不碰"},

		// 路径安全：payload 来自网络，文件名不该有能力把写入引到配置目录之外。
		{"../providers.json", false, "跑出配置目录"},
		{"mappings/../../etc/passwd", false, "跑出配置目录"},
		{"/etc/passwd", false, "绝对路径"},
		{"mappings/sub/x.json", false, "mappings 下不再有子目录"},
		{"mappings//prod.json", false, "// 会让清单里出现两条看着一样的记录"},
		{"mappings/", false, "空名字"},
		{"", false, "空路径"},
		{`mappings\prod.json`, false, "反斜杠在 Linux 上不是分隔符"},
		{"providers.json/", false, "结尾斜杠"},
	}
	for _, tc := range tests {
		t.Run(tc.rel, func(t *testing.T) {
			got, ok := ManagedPath(tc.rel)
			if ok != tc.want {
				t.Fatalf("ManagedPath(%q) = %v，想要 %v（%s）", tc.rel, ok, tc.want, tc.why)
			}
			if ok && got != tc.rel {
				t.Fatalf("ManagedPath(%q) 规范化成了 %q——契约是斜杠相对路径，不该改写", tc.rel, got)
			}
		})
	}
}

func TestListManagedFiles(t *testing.T) {
	sandbox(t)
	// 造一批文件，其中一半该被列出来、一半不该。
	for _, rel := range []string{
		"providers.json",
		"state.json",     // 不该
		"omo-slots.json", // 不该
		"mappings/prod.json",
		"mappings/prod.json.bak", // 不该
		"mappings/other.kv",
		"mappings/.tmp.json", // 不该
	} {
		writeLive(t, rel, []byte("{}"))
	}

	got, err := ListManagedFiles(paths.Config())
	if err != nil {
		t.Fatalf("枚举失败: %v", err)
	}
	want := []string{"mappings/other.kv", "mappings/prod.json", "providers.json"}
	if len(got) != len(want) {
		t.Fatalf("枚举结果 %v，想要 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("枚举结果 %v，想要 %v（必须已排序，报告才稳定）", got, want)
		}
	}
}
