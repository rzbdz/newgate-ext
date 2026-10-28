package codex

import (
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
)

// status 把 codex 的**接管模式**报进 `newgate status`。
//
// 为什么由本模块报：模式与那张模型名表都是 codex 自己的知识产权（见 models.go），
// 界面不认识它们。上一版它们只在网页的「Codex 模型名」卡上有，终端里一个字都
// 看不到——用户在 CLI 里切不了、也确认不了「我现在到底是哪种模式」。
type status struct{}

var _ cliapi.StatusProvider = status{}

func (status) Status() []cliapi.StatusLine {
	// 没装 codex 的机器上，这一行说的是一个不存在的工具的行为。不报。
	if !(facts{}).Installed() {
		return nil
	}
	_, warn := readModels()
	value := i18n.T("takeover · config.toml gets the tier name", nil)
	if Mode() == ModeRename {
		value = i18n.N("{n} model name maps back to a tier",
			"{n} model names map back to tiers", len(effectiveModels()),
			i18n.A{"n": len(effectiveModels())})
	}
	if warn != "" {
		value += " " + style.Yellow(warn)
	}
	return []cliapi.StatusLine{{Rank: 26, Label: "Codex", Value: value}}
}
