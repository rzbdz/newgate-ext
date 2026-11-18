package pluginmanager

import (
	"fmt"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/lib/durarg"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Status 贡献 `newgate status` 里的「模块开关」一行：只列**离开出厂态**的那些。
//
// 为什么由本模块自己报、而不是让 cli 来读这份账本——这是解开那个环的那一步：
// cli 若为了这一行去 Need 本模块，本模块就无法再 Need(cli) 去注册自己的命令
// （cli → pluginmanager → cliapi → cli 成环），于是 `newgate plugin` 只能被迫
// 写在 cli 里、让 cli 认识本模块。谁的状态谁自己报，环就没有了。
//
// 与 cli 里那行核心开关（debug / schema-repair / special_treatment）分开显示：
// 那几个是 domain.State 上的字段，不属于本模块的账本。
func (c *command) Status() []cliapi.StatusLine {
	st := store.LoadState()
	var parts []string
	for _, mod := range c.manager.Modules() {
		for _, sw := range mod.Switches {
			enabled := switchEnabled(st, sw)
			if enabled == sw.Default {
				continue // 还是出厂态，不占版面
			}
			word := "=on"
			if !enabled {
				word = "=off"
			}
			label := sw.Path + word
			if until := Remaining(st, sw.Path); !until.IsZero() {
				label += fmt.Sprintf("(%s)", durarg.Format(int(time.Until(until).Seconds())))
			}
			// footgun 用红色：它是「关掉会破坏正确性」的那一档，在一行式输出里
			// 必须一眼能认出来。
			parts = append(parts, dangerColor(sw.Danger)(label))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return []cliapi.StatusLine{{
		Label: i18n.T("Module switches", nil),
		Value: strings.Join(parts, "   "),
	}}
}

// switchEnabled 一条开关点现在是不是开着的。极性由出厂态决定：
// 出厂开的（kill switch）看 Off()，出厂关的（mode）看 On()。
func switchEnabled(st *domain.State, sw Switch) bool {
	if sw.Default {
		return !Off(st, sw.Path)
	}
	return On(st, sw.Path)
}
