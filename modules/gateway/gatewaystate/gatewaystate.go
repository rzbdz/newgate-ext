// Package gatewaystate 是 gateway 在 state.json 里自己那一段。
//
// 为什么单开一个叶子包（2026-09-18）：网关的开关（schema 修补、special 层总开关、
// 单插件开关）原来声明成 `domain.State` 上的 typed 字段。那等于把**网关的开关词汇
// 写进了共享配置**——同一个 struct 里躺着所有模块的键，加一个网关开关就要动 config。
// 现在它们住在 `state.json` 的 `ModuleConfig["gateway"]` 里，与
// `modules/claudecode` 的 `classifier_naked`、`modules/pluginmanager` 的
// `plugin_manager` 完全同款：**谁的状态谁自己解析，core 不认识任何具体键**。
//
// 为什么是叶子：`gateway/special`（插件层）和 `gateway/forward`（转发热路径）都要
// 读它，而 forward 不能反向 import gateway 根包。放在这里，两个方向都安全——
// 与 modules/gateway/controlpath 同一条规矩。
package gatewaystate

import (
	"encoding/json"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
)

// Key 是 state.json 的 ModuleConfig 里属于 gateway 的字段名。
const Key = "gateway"

// Config 是 gateway 的运行期开关。
//
// 每个字段都是「可缺席」的：SchemaRepair / SpecialTreatment / Fallback 用指针以
// 区分「没配」和「显式关闭」，SpecialOff 空切片与 nil 等价。缺席 = 出厂态（开）。
type Config struct {
	// ParseErr 是解析 ModuleConfig[Key] 时出的错。非 nil 意味着**下面所有字段都
	// 是回退出来的**（读过老键，或落回出厂态），而不是用户当前的选择。展示层
	// 必须把它说出来——见 status.go。
	ParseErr     error
	SchemaRepair *bool `json:"schema_repair,omitempty"`
	// SpecialTreatment special_treatment 插件层总开关（默认开）。
	// 插件只对认领的上游生效（gateway/special），所以开着不影响别人。
	SpecialTreatment *bool `json:"special_treatment,omitempty"`
	// SpecialOff 单独关掉的插件名。排查「是不是 newgate 改坏了请求」时
	// 关掉某一个比关掉整层更精确。名字见 `newgate st`。
	SpecialOff []string `json:"special_treatment_off,omitempty"`

	// Fallback fallback 链总开关（默认开）。关 = 一次请求只走链头，
	// 不换人、不等超时预算。意义在转发侧：**它是全局的**，与
	// `--profile=xx` 那条「这次调用只走这个 profile」是两回事，后者不需要
	// 这个开关（URL 路径已经把 profile 钉住了，见 forward.handleProxy）。
	//
	// 为什么默认开：fallback 是网关存在的理由之一，出厂关掉等于把产品
	// 阉了。指针的语义与 SchemaRepair 同：nil = 没表过态 = 开。
	Fallback *bool `json:"fallback,omitempty"`

	// Debug 全量请求日志（转发侧逐条 dump）。**必须限时**：一发请求的
	// body 可以到 8KB 以上（opencode 的 system prompt 单独就 ~97KB），
	// 忘了关会把磁盘写满。DebugUntil 空 = 显式永久开。
	//
	// 用指针是为了让「没设过」与「显式关掉」可区分——迁移期逐字段回退老键
	// 靠的就是这个区分（nil 才回退）。
	Debug *bool `json:"debug,omitempty"`
	// DebugUntil RFC3339。Debug 为真且这里过了期，就等于没开（懒过期）。
	DebugUntil string `json:"debug_until,omitempty"`
}

// legacy 是迁移前的老键名：这三个开关曾经声明成 domain.State 上的 typed 字段，
// 存在 state.json 的**顶层**。2026-09-18 起改由本模块自己托管。
//
// 为什么必须读老键：store 对「顶层不认识的键」是**无损保留**的（见 store.LoadState
// / SaveState），所以老键既不会自动消失、也不会被新的读路径看到。不读它，一个
// 显式 `special_treatment: false` 过的机器在升级后会**静默变回全开**——用户关掉的
// 东西自己回来了，而且没有任何提示。
var legacy = struct{ SchemaRepair, SpecialTreatment, SpecialOff, Debug, DebugUntil string }{
	"schema_repair", "special_treatment", "special_treatment_off", "debug", "debug_until",
}

// Parse 解析 gateway 那一段。**永不失败**：没有这一段、或者 JSON 坏掉，
// 都当「出厂态」——全开、没有单独关掉的插件（fail-open：读不出来就别关人东西）。
//
// 迁移期**逐字段**回退老键：只在新段里**没设过**那个字段时才读老键。
//
// 为什么不是「新段存在就整段不读老键」：那在「新段已经写了、老键还没删」的窗口里
// 会静默丢设置。实测踩过——分两步搬（先搬 schema-repair、后搬 debug）时，第一次
// 写入就建好了新段，于是后来搬的 debug 永远读不到老值，state.json 顶层留着一个
// 看着像生效的 `debug: true` 而网关已经不读它了。
//
// 为什么逐字段回退不会让旧值复活：老键在**第一次写入时就被删掉**（见 update），
// 回退窗口只存在于升级那一次；而且已设过的字段不参与回退，用户把开关拨回出厂态
// 之后（字段被显式写成非 nil）也不会被老值翻回去。
func Parse(st *domain.State) Config {
	if st == nil {
		return Config{}
	}
	var c Config
	if raw := st.ModuleConfig[Key]; len(raw) > 0 {
		// **解析失败要记下来，不能吞**（2026-09-18）：坏掉的这一段会让下面所有
		// 字段退回出厂态，而不设过的字段出厂态是「全开」——也就是说用户
		// `newgate st off deepseek` 关掉的补丁会被**静默重新打开**，而 status
		// 上那行显示的正是同一个 Parse，看起来一切正常。
		//
		// 行为不变（仍然 fail-open 到出厂态，绝不让一段坏 JSON 把网关拦下来），
		// 但错误随 Config 传出去，由 status / doctor 报成一条 warn。
		c.ParseErr = json.Unmarshal(raw, &c)
	}
	leg := legacyConfig(st)
	if c.SchemaRepair == nil {
		c.SchemaRepair = leg.SchemaRepair
	}
	if c.SpecialTreatment == nil {
		c.SpecialTreatment = leg.SpecialTreatment
	}
	if len(c.SpecialOff) == 0 {
		c.SpecialOff = leg.SpecialOff
	}
	if c.Debug == nil {
		c.Debug = leg.Debug
	}
	if c.DebugUntil == "" {
		c.DebugUntil = leg.DebugUntil
	}
	return c
}

func legacyConfig(st *domain.State) Config {
	var c Config
	if raw := st.ModuleConfig[legacy.SchemaRepair]; len(raw) > 0 {
		var v bool
		if json.Unmarshal(raw, &v) == nil {
			c.SchemaRepair = &v
		}
	}
	if raw := st.ModuleConfig[legacy.SpecialTreatment]; len(raw) > 0 {
		var v bool
		if json.Unmarshal(raw, &v) == nil {
			c.SpecialTreatment = &v
		}
	}
	if raw := st.ModuleConfig[legacy.SpecialOff]; len(raw) > 0 {
		var v []string
		if json.Unmarshal(raw, &v) == nil {
			c.SpecialOff = v
		}
	}
	if raw := st.ModuleConfig[legacy.Debug]; len(raw) > 0 {
		var v bool
		if json.Unmarshal(raw, &v) == nil {
			c.Debug = &v
		}
	}
	if raw := st.ModuleConfig[legacy.DebugUntil]; len(raw) > 0 {
		var v string
		if json.Unmarshal(raw, &v) == nil {
			c.DebugUntil = v
		}
	}
	return c
}

// Marshal 编码成要写进 ModuleConfig 的字节。
func (c Config) Marshal() ([]byte, error) { return json.Marshal(c) }

// 下面这些是**读侧**的便捷函数，直接收 `*domain.State`：调用点遍布热路径，
// 让它们各自 Parse 一次比要求每个调用方先解好再传进来清楚得多（小 JSON，
// 与 claudecode 每请求调 ParseNakedConfig 同一量级）。

// RepairEnabled 给缺 required 的 tool schema 补 "required": []。
// 按 JSON Schema 规范这是语义无操作，所以默认开。
func RepairEnabled(st *domain.State) bool {
	return Parse(st).repairOn()
}

// SpecialEnabled special_treatment 层是否启用。默认开。
func SpecialEnabled(st *domain.State) bool {
	return Parse(st).specialOn()
}

// PluginOff 某个插件是否被单独关掉。
func PluginOff(st *domain.State, name string) bool {
	return Parse(st).pluginOff(name)
}

// FallbackOff fallback 链是否被全局关掉（`newgate fallback off`）。默认 false。
//
// 名字是「关」而不是「开」：转发侧唯一的用法是
// `if tgt.Profile != "" || FallbackOff(st) { 截断链 }`，
// 一个肯定式的 `FallbackEnabled` 会让每个调用点都写成 `!FallbackEnabled(st)`，
// 而否定散落在调用点上正是这种开关最容易看反的地方。
func FallbackOff(st *domain.State) bool {
	return Parse(st).fallbackOff()
}

// DebugActive 全量请求日志现在开着吗。**懒过期**：Debug 为真但到点了就算没开，
// 不需要谁去把它关掉——转发路径上的每一次判断都会自然得到 false。
//
// 解析不出时间戳时按「开着」算：宁可多记一段日志，也不能因为一个手改坏的
// 时间戳把用户特意打开的东西静默关掉。
func DebugActive(st *domain.State) bool {
	return Parse(st).debugActive()
}

func (c Config) debugActive() bool {
	if c.Debug == nil || !*c.Debug {
		return false
	}
	if c.DebugUntil == "" {
		return true // 显式永久开
	}
	t, err := time.Parse(time.RFC3339, c.DebugUntil)
	if err != nil {
		return true
	}
	return time.Now().Before(t)
}

// DebugUntilDisplay 到期时刻的原文，供 status 展示（空 = 不限时）。
func DebugUntilDisplay(st *domain.State) string { return Parse(st).DebugUntil }

func (c Config) repairOn() bool  { return c.SchemaRepair == nil || *c.SchemaRepair }
func (c Config) specialOn() bool { return c.SpecialTreatment == nil || *c.SpecialTreatment }

func (c Config) fallbackOff() bool { return c.Fallback != nil && !*c.Fallback }

func (c Config) pluginOff(name string) bool {
	for _, n := range c.SpecialOff {
		if n == name {
			return true
		}
	}
	return false
}

// ---------- 写侧 ----------

// SetSchemaRepair 开关 schema 修补。
func SetSchemaRepair(on bool) error {
	return update(func(c *Config) { c.SchemaRepair = &on })
}

// SetSpecialTreatment 开关整个 special_treatment 层。
func SetSpecialTreatment(on bool) error {
	return update(func(c *Config) { c.SpecialTreatment = &on })
}

// SetFallback 开关 fallback 链。关 = 所有 gateway 只走链头。
func SetFallback(on bool) error {
	return update(func(c *Config) { c.Fallback = &on })
}

// SetDebug 开关全量请求日志。untilRFC3339 空 = 不限时。
func SetDebug(on bool, untilRFC3339 string) error {
	return update(func(c *Config) {
		c.Debug = &on
		c.DebugUntil = ""
		if on {
			c.DebugUntil = untilRFC3339
		}
	})
}

// SetSpecialPlugin 单独开关一个插件。关 = 记进 SpecialOff，开 = 从里面删掉。
func SetSpecialPlugin(name string, on bool) error {
	return update(func(c *Config) {
		out := make([]string, 0, len(c.SpecialOff))
		for _, n := range c.SpecialOff {
			if n != name {
				out = append(out, n)
			}
		}
		if !on {
			out = append(out, name)
		}
		if len(out) == 0 {
			out = nil
		}
		c.SpecialOff = out
	})
}

// update 读一份 state、改 gateway 那一段、写回去。读-改-写整体一次，
// 免得两个 setter 交叉覆盖（原来在 store 里也是这个形状）。
func update(mutate func(*Config)) error {
	st := store.LoadState()
	c := Parse(st)
	mutate(&c)
	raw, err := c.Marshal()
	if err != nil {
		return err
	}
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	st.ModuleConfig[Key] = raw
	// 老键显式删掉。store 的无损保留意味着不删就永远躺在 state.json 里，
	// 下一个人看到会以为它还在生效（实测：迁移后顶层仍留着 schema_repair: true）。
	// 新值已经落在 Key 那一段里，删掉不会丢信息。
	delete(st.ModuleConfig, legacy.SchemaRepair)
	delete(st.ModuleConfig, legacy.SpecialTreatment)
	delete(st.ModuleConfig, legacy.SpecialOff)
	delete(st.ModuleConfig, legacy.Debug)
	delete(st.ModuleConfig, legacy.DebugUntil)
	return store.SaveState(st)
}
