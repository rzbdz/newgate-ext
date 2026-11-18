package locale

import (
	"encoding/json"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
)

// StateKey 是 state.json 里本模块拥有的字段名（由 module.go 向 confighook 登记）。
//
// 为什么放 state.json：语言是**这台机器上这个人的偏好**，不是车队配置——与
// plugin-manager 的开关账本同一性质（`domain.State` 的注释：机器本地的运行时状态，
// 经常改，不进 dotfiles）。配置共享那一套「共享层 + 本地层」的分层在发行版里，
// 而「我要读哪门语言」是彻头彻尾的本地事。
const StateKey = "locale"

// persisted 是这一段存下来的形状。留成结构体而不是一个裸字符串：将来加
// 「CLI 语言与日志语言分开设」这类字段时，改这里是加字段，不是改格式。
type persisted struct {
	Lang string `json:"lang"`
}

// loadConfigured 读持久化的语言选择；没设过或读不动时返回空串（= 没意见）。
func loadConfigured() string {
	st := store.LoadState()
	if st == nil || st.ModuleConfig == nil {
		return ""
	}
	raw := st.ModuleConfig[StateKey]
	if len(raw) == 0 {
		return ""
	}
	var p persisted
	if err := json.Unmarshal(raw, &p); err != nil {
		// 解不出来就当没设：一条坏掉的偏好不该让 CLI 起不来。语言是**偏好**，
		// 不是配置正确性的一部分——这里 fail-open 是刻意的。
		return ""
	}
	return p.Lang
}

// saveConfigured 把语言写进 state.json。
func saveConfigured(tag string) error {
	st := store.LoadState()
	if st == nil {
		st = &domain.State{}
	}
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	raw, err := json.Marshal(persisted{Lang: tag})
	if err != nil {
		return err
	}
	st.ModuleConfig[StateKey] = raw
	return store.SaveState(st)
}
