package proto

import (
	"strings"
	"testing"
)

// TestGateTruthTable 锁住两级门的**性质差异**——它不是同一件事的两个严重度，
// 所以两者的行为必须不一样：
//
//   - 构建串不匹配 ⇒ 放行 + 警告（这份配置今天解析得了，只是按更新的构建写的）
//   - schema 更高   ⇒ 拒绝 + 保留上一份（字段含义可能已经变了，误读**不会报错**）
func TestGateTruthTable(t *testing.T) {
	tests := []struct {
		name       string
		meta       *Meta
		cur        string
		wantOK     bool
		wantWarn   bool
		wantRefuse bool
	}{
		{"没有 meta：按当前 schema 解释", nil, "build-1", true, true, false},
		{"schema 相同：无话", &Meta{SchemaVersion: 1}, "build-1", true, false, false},
		{"schema 更高：拒绝", &Meta{SchemaVersion: 2}, "build-1", false, false, true},
		{"schema 是 0（没写）：放行 + 提醒", &Meta{}, "build-1", true, true, false},
		{"构建串相同：无话", &Meta{SchemaVersion: 1, MinNewgateVersion: "build-1"}, "build-1", true, false, false},
		{"构建串不同：放行 + 警告", &Meta{SchemaVersion: 1, MinNewgateVersion: "build-2"}, "build-1", true, true, false},
		{"dev 构建不警告（否则每个开发机上一句噪音）", &Meta{SchemaVersion: 1, MinNewgateVersion: "build-2"}, "dev", true, false, false},
		{"本机没有版本串：不警告", &Meta{SchemaVersion: 1, MinNewgateVersion: "build-2"}, "", true, false, false},
		{
			// `git describe` 串**不可排序**：build-17-… 与 build-18-… 之间没有
			// 数值大小关系，--dirty 更糟。所以判据只能是"相等"，于是它只配
			// 驱动警告——不能让它有牙。
			"dirty 后缀只是一个不同的串：仍放行",
			&Meta{SchemaVersion: 1, MinNewgateVersion: "build-1-gabc-dirty"},
			"build-1-gabc", true, true, false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, warn, refuse := Gate(tc.meta, GateInput{CurrentVersion: tc.cur})
			if ok != tc.wantOK {
				t.Fatalf("ok=%v，想要 %v（warn=%q refuse=%q）", ok, tc.wantOK, warn, refuse)
			}
			if (warn != "") != tc.wantWarn {
				t.Fatalf("warn=%q，想要有=%v", warn, tc.wantWarn)
			}
			if (refuse != "") != tc.wantRefuse {
				t.Fatalf("refuse=%q，想要有=%v", refuse, tc.wantRefuse)
			}
			if tc.wantRefuse {
				// 拒绝的文案必须说清"为什么拒绝"，因为用户会照着它决定升级还是
				// 去改宿主。只写"版本不支持"会让人以为是个装饰性的检查。
				if !strings.Contains(refuse, "schema_version") {
					t.Fatalf("拒绝文案该点名 schema_version: %q", refuse)
				}
				if !strings.Contains(refuse, "上一份") {
					t.Fatalf("拒绝文案该说明保留了上一份配置: %q", refuse)
				}
			}
		})
	}
}

func TestParseMeta(t *testing.T) {
	// 文件缺席 = 宿主没声明 → (nil, nil)，由 Gate 走"按当前 schema 解释"。
	meta, err := ParseMeta(map[string][]byte{"providers.json": []byte("{}")})
	if err != nil || meta != nil {
		t.Fatalf("缺席该给 (nil, nil)，实际 %v / %v", meta, err)
	}
	// 有文件但不是 JSON：这是宿主写坏了，**不是**"没声明"。必须报错，否则
	// 会被当成"没声明"而按当前 schema 解释——把一个明确的坏文件洗成正常。
	if _, err := ParseMeta(map[string][]byte{MetaFileName: []byte("这不是 JSON")}); err == nil {
		t.Fatal("坏掉的 meta 文件必须报错，不能被当成\"没声明\"")
	}
	good := map[string][]byte{MetaFileName: metaFile(3, "build-9")}
	meta, err = ParseMeta(good)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if meta.SchemaVersion != 3 || meta.MinNewgateVersion != "build-9" {
		t.Fatalf("解析结果不对: %+v", meta)
	}
}
