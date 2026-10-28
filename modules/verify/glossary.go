package verify

import (
	i18n "github.com/rzbdz/newgate/lib/i18n"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
)

// verifyGlossary 是帮助屏术语表里「指纹」那一行。
//
// 为什么由本模块报：指纹这个说法是 verify 引入的，定义只有这里知道；界面
// （cli）不认识它，也不该为了这一行去 import 本模块（见 cliapi.Glossarist）。
type verifyGlossary struct{}

var _ cliapi.Glossarist = verifyGlossary{}

func (verifyGlossary) Glossary() []cliapi.GlossaryLine {
	return []cliapi.GlossaryLine{{
		Rank: 40,
		Term: i18n.T("fingerprint", nil),
		Definition: i18n.T(
			"a statistical identity computed from hundreds of integers the upstream generates; compared against the reference bank to see which model really answers",
			nil),
	}}
}
