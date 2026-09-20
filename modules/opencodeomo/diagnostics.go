package opencodeomo

import (
	"fmt"
	"os"

	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
)

type diagnostics struct{}

var _ cliapi.DiagnosticProvider = (*diagnostics)(nil)

func (diagnostics) Diagnostics() []cliapi.Diagnostic {
	reg := ReadOmoSlots()
	if reg == nil {
		return []cliapi.Diagnostic{{
			Label: "槽位", State: "skip",
			Line: "无注册表（未接管过 oh-my-openagent）",
		}}
	}
	keys := 0
	for _, slot := range reg.Slots {
		if slot.Key != "" {
			keys++
		}
	}
	if keys == 0 {
		return []cliapi.Diagnostic{{
			Label: "槽位", State: "skip", Line: "注册表为空",
		}}
	}
	if fi, err := os.Stat(SlotsFile()); err == nil && fi.Mode().Perm()&0o040 == 0 {
		return []cliapi.Diagnostic{{
			Label: "槽位", State: "warn",
			Line:    fmt.Sprintf("%d 个槽位键，但注册表组不可读（跑 daemon 的另一个用户看不到）", keys),
			Details: []string{"chmod 0660 " + SlotsFile()},
		}}
	}
	item := cliapi.Diagnostic{
		Label: "槽位", State: "ok",
		Line: fmt.Sprintf("%d 个槽位键 · 模式 %s", keys, modeName(reg)),
	}
	if n := diffCount(reg); n > 0 {
		item.Details = append(item.Details,
			fmt.Sprintf("%d 个键有建议档位（newgate omo ls 查看）", n))
	}
	return []cliapi.Diagnostic{item}
}

func modeName(reg *OmoSlots) string {
	if reg != nil && reg.Mode == "suggested" {
		return "suggested"
	}
	return "current"
}

func diffCount(reg *OmoSlots) int {
	if reg == nil {
		return 0
	}
	n := 0
	for _, slot := range reg.Slots {
		if _, overridden := reg.Overrides[slot.Key]; overridden {
			continue
		}
		if slot.Suggested != "" && slot.Suggested != slot.Current {
			n++
		}
	}
	return n
}
