package verify

import (
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
)

// 本文件是交给界面那一面：侧栏里的一节「指纹」，里面一张 KindFingerprints 卡。
//
// 登记时**一个文件都不读**：产出函数要等到真的有人来看界面才跑（见 lib/view 的
// 包注释）。这一条在 `newgate verify` 的每一条命令里都成立——CLI 进程也会装配
// 一遍模块，而它不会打开网页。

func registerView(v view.Service, svc *service) (modules.Release, error) {
	section := view.Title(func() string { return i18n.T("Fingerprints", nil) }).Of(view.FieldRoutes)
	return v.Register(Name, section, func() ([]view.Concept, error) {
		return []view.Concept{{
			ID:    "verify.fingerprints",
			Kind:  view.KindFingerprints,
			Title: i18n.T("Verification", nil),
			// Live：这一面会自己变——终端的 `newgate verify` 刚写了一条缓存，
			// 页面上那一格就该跟着变。读一次是几份 profile + 几十 KB 的缓存，
			// 与 config 那一节的「重读并重新解析全部配置」不是一回事。
			Live: true,
			Data: svc.State(),
		}}, nil
	})
}
