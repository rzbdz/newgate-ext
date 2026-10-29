package webdashboard

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

func testPlugin(name string) Plugin {
	return Plugin{
		Name:   name,
		Assets: fstest.MapFS{"plugin.js": {Data: []byte("export function mount(el){}")}, "x.css": {Data: []byte("a{}")}},
		Entry:  "plugin.js",
		Slots:  []PluginSlot{SlotTopbar},
	}
}

// 登记期就拦住作者的错：入口不在、槽位拼错、一个槽位都不填、重名。
func TestPluginRegistrationRejectsAuthorErrors(t *testing.T) {
	r := newPluginRegistry()
	bad := []Plugin{
		{Name: "Bad Name", Assets: testPlugin("x").Assets, Entry: "plugin.js", Slots: []PluginSlot{SlotTopbar}},
		{Name: "noentry", Assets: testPlugin("x").Assets, Entry: "missing.js", Slots: []PluginSlot{SlotTopbar}},
		{Name: "noslot", Assets: testPlugin("x").Assets, Entry: "plugin.js"},
		{Name: "typo", Assets: testPlugin("x").Assets, Entry: "plugin.js", Slots: []PluginSlot{"top-bar"}},
	}
	for _, p := range bad {
		if _, err := r.RegisterPlugin(p); err == nil {
			t.Errorf("%s: accepted", p.Name)
		}
	}
	rel, err := r.RegisterPlugin(testPlugin("ok"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterPlugin(testPlugin("ok")); err == nil {
		t.Fatal("a duplicate name was accepted")
	}
	_ = rel()
	if len(r.manifest()) != 0 {
		t.Fatal("release did not remove the plugin")
	}
}

// 清单 + 发文件：界面拿到的只有名字、地址、槽位；文件只从那个插件自己的 Assets
// 里出，路径穿越与不存在的名字一律 404（不落回 index.html）。
func TestPluginManifestAndFiles(t *testing.T) {
	h := newHandler()
	if _, err := h.plugins.RegisterPlugin(testPlugin("demo")); err != nil {
		t.Fatal(err)
	}
	rec := get(t, h, "/api/plugins")
	var doc struct {
		Plugins []pluginDoc `json:"plugins"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || len(doc.Plugins) != 1 {
		t.Fatalf("manifest = %s (%v)", rec.Body.String(), err)
	}
	p := doc.Plugins[0]
	if p.Name != "demo" || !strings.HasPrefix(p.Entry, "plugins/demo/plugin.js?v=") || len(p.Slots) != 1 || p.Slots[0] != SlotTopbar {
		t.Fatalf("manifest entry = %+v", p)
	}

	if rec := get(t, h, "/plugins/demo/plugin.js"); rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("entry: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, path := range []string{"/plugins/demo/../../index.html", "/plugins/nope/plugin.js", "/plugins/demo/missing.js", "/plugins/demo/"} {
		if rec := get(t, h, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, rec.Code)
		}
	}
}
