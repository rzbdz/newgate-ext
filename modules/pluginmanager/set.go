package pluginmanager

import (
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate/lib/i18n"
)

// SetSwitch 把一条开关点的期望态写进 state（**不落盘**：落盘与「要不要通知数据面」
// 是调用方的事，命令行与 web 界面在这两件事上不同）。
//
// 它是 `newgate plugin <path> on|off` 与浏览器界面共用的**那一份**语义，因为这条
// 语义里全是容易写错第二遍的地方：
//
//   - 出厂态决定走哪张表：Default=true 的是 kill switch（用户的「关」写进 Off 表），
//     Default=false 的是 mode（用户的「开」写进 On 表）；
//   - **动作等于出厂态 = 撤销这条设定**（对一个出厂开着的开关点说 on，就是把它从
//     Off 表里拿掉），而不是再写一条「打开」——写反了，出厂态一改，用户那条历史
//     动作的意思就跟着反了；
//   - footgun 不接受「永久」，而且它自己声明的 TTL 就是不给时长时的兜底时限
//     （注册期强制 > 0，见 Switch.TTL）。
//
// 判据是「两个界面会不会给出不同答案」：上面三条任何一条写岔，用户就会看到 CLI
// 说关着、浏览器说开着。所以它们只在这里写一次。
func SetSwitch(st *domain.State, sw Switch, on bool, ttl time.Duration, forever bool) error {
	if sw.Danger == DangerFootgun && forever {
		return i18n.E(
			"plugin: {path} is a footgun, forever is not accepted (give a duration, e.g. 5m)",
			i18n.A{"path": sw.Path})
	}
	cfg := Parse(rawConfig(st)).Prune()
	wantOff := sw.Default != on
	if wantOff {
		cfg.Off = setEntry(cfg.Off, sw.Path, effectiveTTL(sw, ttl, forever))
	} else {
		cfg.Off = dropEntry(cfg.Off, sw.Path)
	}
	if !wantOff && !sw.Default {
		cfg.On = setEntry(cfg.On, sw.Path, effectiveTTL(sw, ttl, forever))
	} else {
		cfg.On = dropEntry(cfg.On, sw.Path)
	}
	raw, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	st.ModuleConfig[StateKey] = raw
	return nil
}

// effectiveTTL 这次该记多长时限：用户给了就用用户的，没给就用开关点声明的默认值。
func effectiveTTL(sw Switch, ttl time.Duration, forever bool) time.Time {
	if forever {
		return time.Time{}
	}
	if ttl == 0 {
		ttl = sw.TTL
	}
	if ttl == 0 {
		return time.Time{}
	}
	return time.Now().Add(ttl)
}
