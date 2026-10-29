package webdashboard

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 界面的前端**不认识任何模块**：它给框架接口（Kind、槽位、插件契约、host.ext），
// 模块往里填。一个模块的前端知识应当住在那个模块里（插件），多个模块拼起来的
// 界面住在一层包装模块里——而不是在 web/src 里点名某个模块的扩展端点。
//
// 这条棘轮守的是最容易越界、也最容易查的那一种：前端源码里**写死**一个扩展端点
// 的名字（`/ext/<名字>`）。插件走 host.ext(变量)，不会命中它。
//
// 2026-10-29 加它时已经有两处越界（模型下拉直接问 modellist 与 verify 的端点，
// 指纹那一屏整个是 verify 的界面）。它们列在下面，**只减不增**：迁走一个就删一行；
// 多出来一个、或者列着的已经不在了，这条都会红（后者是提醒你把名单缩短）。
var extLiteralAllowlist = map[string]string{
	"modellist":   "ModelPicker / api.ts: the model picker asks modules/modellist directly",
	"fingerprint": "Fingerprints.svelte / ModelPicker / api.ts: the fingerprint page is modules/verify's UI living in the shell",
}

var extLiteral = regexp.MustCompile(`/ext/([a-z0-9_-]+)`)

func TestShellFrontendNamesNoExtension(t *testing.T) {
	found := map[string][]string{}
	err := filepath.WalkDir("web/src", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !(strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".svelte")) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range extLiteral.FindAllStringSubmatch(string(b), -1) {
			found[m[1]] = append(found[m[1]], p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for n := range found {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, ok := extLiteralAllowlist[n]; !ok {
			t.Errorf("web/src names the extension %q (%v): the shell must not know a module — "+
				"ship the UI as a plugin from that module (api.go, \"前端插件\"), or aggregate it in a wrapper module",
				n, found[n])
		}
	}
	for n := range extLiteralAllowlist {
		if _, ok := found[n]; !ok {
			t.Errorf("%q is on the allowlist but web/src no longer names it — remove it from extLiteralAllowlist", n)
		}
	}
}
