package store

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
)

func kvSandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeMapping(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := ioutil.WriteFile(filepath.Join(dir, "mappings", name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestParseProfileKV 语法面：档位缩写、裸模型、候选列表、注释、未知 key
// 必须报错（不静默忽略）。
func TestParseProfileKV(t *testing.T) {
	p, err := ParseProfileKV(`# 一行注释
extends = kimi
desc = 高速主循环
prio = 60
excluded = true
window = 1000000
compact = 500000

mid = kimi-k2.7-code-highspeed
heavy = kimi/kimi-k3, other/big
fallback = kimi/kimi-k3
`)
	if err != nil {
		t.Fatal(err)
	}
	if p.Extends != "kimi" || p.Description != "高速主循环" || !p.Excluded {
		t.Fatalf("标量解析错: %+v", p)
	}
	if p.Prio() != 60 || p.ContextWindow != 1000000 || p.AutoCompactWindow != 500000 {
		t.Fatalf("数值解析错: prio=%d window=%d", p.Prio(), p.ContextWindow)
	}
	if mid := p.Roles["mid"]; len(mid) != 1 || mid[0].Model != "kimi-k2.7-code-highspeed" || mid[0].Provider != "" {
		t.Fatalf("裸模型解析错: %v", mid)
	}
	if hv := p.Roles["heavy"]; len(hv) != 2 || hv[0].String() != "kimi/kimi-k3" || hv[1].String() != "other/big" {
		t.Fatalf("候选列表解析错: %v", hv)
	}
	if p.Fallback == nil || p.Fallback.String() != "kimi/kimi-k3" {
		t.Fatalf("fallback 解析错: %v", p.Fallback)
	}

	// 未知 key 报错——拼错字段名被静默忽略是最坑人的。
	// 断言英文（源语言）：文案是 i18n 的译文，身份才是这句话本身。
	if _, err := ParseProfileKV("midle=x\n"); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("未知 key 该报错，实际 %v", err)
	}
	// 坏布尔不猜
	if _, err := ParseProfileKV("excluded=maybe\n"); err == nil {
		t.Fatal("布尔值 true/false 之外该报错")
	}
	// 裸 key=value 形状
	if _, err := ParseProfileKV("这不是kv\n"); err == nil {
		t.Fatal("不是 key=value 的行该报错")
	}
}

// TestProfileKVRoundTrip 序列化再解析，字段一个不丢（含任意档位 key 和
// "*"——json 时代留下的形态不能在转换里蒸发）。
func TestProfileKVRoundTrip(t *testing.T) {
	prio := 18
	p := &domain.Profile{
		Name: "x", Description: "d", Priority: &prio, Pinned: true, Excluded: true,
		Extends: "base", ContextWindow: 1000000, AutoCompactWindow: 500000,
		Roles: map[string]domain.Candidates{
			"heavy":  {domain.Binding{Provider: "a", Model: "m1"}},
			"mid":    {domain.Binding{Provider: "a", Model: "m2"}, domain.Binding{Provider: "b", Model: "m3"}},
			"search": {domain.Binding{Provider: "a", Model: "m4"}},
			"*":      {domain.Binding{Provider: "a", Model: "m5"}},
		},
		Fallback: &domain.Binding{Provider: "f", Model: "fb"},
	}
	p2, err := ParseProfileKV(SerializeProfileKV(p))
	if err != nil {
		t.Fatal(err)
	}
	if p2.Description != "d" || p2.Prio() != 18 || !p2.Pinned || !p2.Excluded ||
		p2.Extends != "base" || p2.ContextWindow != 1000000 || p2.AutoCompactWindow != 500000 {
		t.Fatalf("标量往返丢字段: %+v", p2)
	}
	for _, k := range []string{"heavy", "mid", "search", "*"} {
		if got, want := len(p2.Roles[k]), len(p.Roles[k]); got != want {
			t.Fatalf("roles[%s] 候选数 %d != %d", k, got, want)
		}
	}
	if p2.Roles["mid"][1].String() != "b/m3" {
		t.Fatalf("候选列表往返错: %v", p2.Roles["mid"])
	}
	if p2.Fallback == nil || p2.Fallback.String() != "f/fb" {
		t.Fatalf("fallback 往返错: %v", p2.Fallback)
	}
}

// TestLoadProfileExtends extends 合并：差异覆盖、缺项继承、裸模型从 base
// 同档位借 provider、bool 不继承。
func TestLoadProfileExtends(t *testing.T) {
	dir := kvSandbox(t)
	writeMapping(t, dir, "kimi.json", `{"name":"kimi","priority":22,"pinned":false,
		"roles":{"heavy":{"provider":"kimi","model":"kimi-k3"},
		         "mid":{"provider":"kimi","model":"k2.7"},"light":{"provider":"kimi","model":"k2.7"}}}`)
	writeMapping(t, dir, "kimi-fast.kv", `extends=kimi
excluded=true
desc=高速
mid=k2.7-highspeed
`)

	p, err := LoadProfile("kimi-fast")
	if err != nil {
		t.Fatal(err)
	}
	if p.Extends != "" {
		t.Fatal("解析后的视图不该还背着 extends 声明")
	}
	if p.Excluded != true {
		t.Fatal("自己的 excluded 要生效")
	}
	if p.Pinned {
		t.Fatal("bool 不继承（base 没写 pinned，这里也不该凭空有）")
	}
	if p.Prio() != 22 {
		t.Fatalf("priority 应继承 base 的 22，实际 %d", p.Prio())
	}
	if m := p.Roles["mid"]; len(m) != 1 || m[0].String() != "kimi/k2.7-highspeed" {
		t.Fatalf("裸模型应借 base 的 provider: %v", m)
	}
	if m := p.Roles["heavy"]; len(m) != 1 || m[0].String() != "kimi/kimi-k3" {
		t.Fatalf("没提的档位应继承: %v", m)
	}
}

// TestLoadProfileExtendsCycle 成环必须报错，不能栈溢出。
func TestLoadProfileExtendsCycle(t *testing.T) {
	dir := kvSandbox(t)
	writeMapping(t, dir, "a.kv", "extends=b\n")
	writeMapping(t, dir, "b.kv", "extends=a\n")
	if _, err := LoadProfile("a"); err == nil || !strings.Contains(err.Error(), "extends cycle") {
		t.Fatalf("extends 成环该报错，实际 %v", err)
	}
	// extends 不存在的 base
	writeMapping(t, dir, "c.kv", "extends=ghost\n")
	if _, err := LoadProfile("c"); err == nil {
		t.Fatal("extends 不存在的 base 该报错")
	}
}

// TestKVWinsOverJSON 同名 .kv 和 .json 并存：kv 赢（它只会被人刻意创建），
// ListProfiles 只出一个名字。
func TestKVWinsOverJSON(t *testing.T) {
	dir := kvSandbox(t)
	writeMapping(t, dir, "p.json", `{"name":"p","roles":{"mid":{"provider":"j","model":"from-json"}}}`)
	writeMapping(t, dir, "p.kv", "mid=kv/model\n")

	p, err := LoadProfile("p")
	if err != nil {
		t.Fatal(err)
	}
	if m := p.Roles["mid"]; len(m) != 1 || m[0].String() != "kv/model" {
		t.Fatalf("kv 应赢，实际 %v", m)
	}
	names, err := ListProfiles()
	if err != nil || len(names) != 1 || names[0] != "p" {
		t.Fatalf("同名只该列一次: %v %v", names, err)
	}
}
