package pluginmanager

import (
	"encoding/json"
	"time"
)

// StateKey 是 state.json 里本模块拥有的字段名（由 module.go 向 confighook 登记）。
//
// 它留在**机器本地**是对的：开关是现场调试动作，不是车队配置。共享配置层的
// 托管文件集合里不含 state.json，所以「A 机器关掉某个开关」不会漂到 B 机器。
const StateKey = "plugin_manager"

// Entry 是一次显式设定的记录。
type Entry struct {
	// Until 是这条设定的失效时刻（RFC3339，零值 = 不限时）。**懒过期**：没人
	// 在后台扫它，读的时候比一下当前时间就算数。daemon 因此不持 timer，重启
	// 也不残留任何东西。
	Until time.Time `json:"until,omitempty"`
}

// Config 是 state.json 里 plugin_manager 字段的形状。
//
// 两张表是**独立**的，而不是一张 map 加一个极性字段：
//
//   - Off 是「出厂开、用户关掉」的 kill switch；
//   - On 是「出厂关、用户打开」的 mode。
//
// 一条 Path 只会出现在其中一张里，由 CLI 按 Switch.Default 决定写哪张。这样做
// 的好处都在热路径上：Off() 和 On() 各自只看自己那张表，**都不需要查注册表**，
// 于是核心的转发循环里只有一次 map 查找。坏处是 JSON 里有两张表，但它是自解释的。
type Config struct {
	Off map[string]Entry `json:"off,omitempty"`
	On  map[string]Entry `json:"on,omitempty"`
}

// Parse 解析 state.json 里的原始字节。
//
// **永不失败**：坏 JSON、缺字段、类型不对，一律当「用户什么都没设过」。这是
// fail-open 的落点——读不出来就照常工作，绝不因为一段坏配置把某个功能关掉。
// 与 modules/claudecode 的 ParseNakedConfig 同款（那份注释记着两次因为过期判断
// 分叉而出的 bug，所以这里只有这一个解析入口，所有读点都走它）。
func Parse(raw []byte) Config {
	var c Config
	if len(raw) == 0 {
		return c
	}
	if json.Unmarshal(raw, &c) != nil {
		return Config{}
	}
	return c
}

// Marshal 序列化成写回 state.json 的字节。
func (c Config) Marshal() ([]byte, error) { return json.Marshal(c) }

// lookup 在给定的那张表里找一条设定，并做懒过期判断。
//
// 返回 (生效, 失效时刻)。过期的当没设过——**并且不删**：删除需要写盘，而热路径
// 是只读的。过期项留着的代价只是 JSON 里多一行，CLI 下次写入时会顺带清掉它
// （见 Config.Prune）。
func lookup(table map[string]Entry, path string) (bool, time.Time) {
	e, ok := table[path]
	if !ok {
		return false, time.Time{}
	}
	if !e.Until.IsZero() && !time.Now().Before(e.Until) {
		return false, time.Time{}
	}
	return true, e.Until
}

// Prune 去掉已过期的设定，供 CLI 每次写入前顺手清理（热路径不做这件事）。
func (c Config) Prune() Config {
	out := Config{}
	keep := func(dst *map[string]Entry, src map[string]Entry) {
		for path, e := range src {
			if !e.Until.IsZero() && !time.Now().Before(e.Until) {
				continue
			}
			if *dst == nil {
				*dst = map[string]Entry{}
			}
			(*dst)[path] = e
		}
	}
	keep(&out.Off, c.Off)
	keep(&out.On, c.On)
	return out
}
