package probe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/gateway/dialect"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

type capabilityEntry struct {
	Supports uint32    `json:"supports"`
	Known    uint32    `json:"known"`
	Quirks   uint32    `json:"quirks"`
	Checked  time.Time `json:"checked_at"`
}

type capabilityCache struct {
	mu      sync.Mutex
	Targets map[string]capabilityEntry `json:"targets"`
}

// loadCapabilityCache 读回上一次探活学到的方言/quirk 结论。
//
// 读失败（文件坏、权限不对）返回错误但**照常给一份空缓存**：调用方据此决定
// 是「重新探一轮（花 token）」还是「就这样跑」。以前这个错误被 `_ =` 吞掉，
// 症状是「明明探过，每次还是要重探」——而唯一的线索（为什么读不出来）没了。
func loadCapabilityCache() (*capabilityCache, error) {
	c := &capabilityCache{Targets: map[string]capabilityEntry{}}
	raw, err := os.ReadFile(paths.ProbeCacheFile())
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil // 没探过，很正常
		}
		return c, i18n.Ef(err, "read capability cache {path}: {err}",
			i18n.A{"path": paths.ProbeCacheFile()})
	}
	if err := json.Unmarshal(raw, c); err != nil {
		return c, i18n.Ef(err, "parse capability cache {path}: {err}",
			i18n.A{"path": paths.ProbeCacheFile()})
	}
	if c.Targets == nil {
		c.Targets = map[string]capabilityEntry{}
	}
	return c, nil
}

func (c *capabilityCache) apply(t Target) bool {
	c.mu.Lock()
	entry, ok := c.Targets[t.String()]
	c.mu.Unlock()
	if !ok {
		return false
	}
	for _, cap := range []dialect.Cap{
		dialect.CapOpenAI, dialect.CapAnthropic, dialect.CapCountTokens,
	} {
		switch {
		case dialect.Cap(entry.Known)&cap == 0:
		case dialect.Cap(entry.Supports)&cap != 0:
			dialect.Mark(t.Provider, t.Model, cap)
		default:
			dialect.MarkUnsupported(t.Provider, t.Model, cap)
		}
	}
	if flags := quirk.Flag(entry.Quirks); flags != 0 {
		quirk.Default.Mark(t.Provider, t.Model, flags)
	}
	return true
}

// QuirksOf 把「我们此刻知道这个目标的哪些毛病」报成一份可以交给别的模块的形状。
//
// 谁需要它：数据面自己（转发时撞出来的 4xx 与我们**探**出来的结论一样有价值，
// 而探出来的那份会落盘，撞出来的那份以前只活在内存里——见 forward 的 Shutdown）。
//
// 报告的量本身就是「探索出来的」（`quirk.Flags`）——从已知的毛病位里逐位问，
// 而不是自己去读那张表。**这张表归 gateway/quirk 所有**，这里只借它的判据，
// 不复制它的一份内部形状：表改了这里跟着改，不会分家。
//
// 只管内存里那张表（`quirk.Default`），不读盘也不写盘——落盘是 Load/Save 那一对
// 的事，而「读一次盘来回答一个内存问题」在这里会变成热路径上的 IO（见
// forward.learnQuirks 的调用点：那在每一次上游 4xx 上）。
func QuirksOf(t Target) quirk.Flag {
	var flags quirk.Flag
	for _, f := range quirk.AllFlags() {
		if quirk.Default.Has(t.Provider, t.Model, f) {
			flags |= f
		}
	}
	return flags
}

// MergeQuirks 把一份「此刻已知的毛病」并进落盘缓存。
//
// 它是**并集、不是覆盖**：缓存里那些位可能来自一次真探活（那份结论更新，因为
// 探活打的是 provider 声明的协议），而并进来的这些来自转发路上真撞出来的报错
// ——两者都对同一个 (provider, model) 成立，谁都不该把对方抹掉。只增不减是这条
// 路唯一安全的写法：**少记一位**的后果是重启后又多撞一次 400（可恢复），**多抹
// 一位**的后果是这个补丁从此不再生效（不可恢复，而且没有任何症状指向它）。
//
// 没读过盘、盘上也没有这个目标时，会新建一条：`supports`/`known` 留 0 是**正确**
// 的——那两位说的是方言能力，而这里一个字节的结论都没有。丢了它不会让 apply 做
// 错事：`known` 为 0 时 apply 一个方言位都不标（见上面那个 switch 的第一支）。
//
// 只写这一份缓存，绝不碰 providers.json：那是用户的配置文件，我们不该悄悄往里
// 写东西（同一条判断见 gateway/quirk 文件头对「表为什么不落盘」的说明）。
//
// 返回这份缓存里剩下的目标数，供调用方写日志。
func MergeQuirks(entries map[Target]quirk.Flag) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	c, _ := loadCapabilityCache()
	for t, flags := range entries {
		if flags == 0 {
			continue
		}
		// 盘上已经有这一条就保着它的方言那两位（见上面），没有就新建。
		e := c.Targets[t.String()]
		e.Quirks |= uint32(flags)
		e.Checked = time.Now()
		c.Targets[t.String()] = e
	}
	if err := c.save(); err != nil {
		return 0, err
	}
	return len(c.Targets), nil
}

// PendingQuirks 是**已知、但还没落盘**的毛病。
//
// 为什么要有这一层：转发路上撞出来的毛病（learnQuirks）以前只活在内存里，
// 而这条路上有一个**一定会复发**的窗口——进程重启。重启后内存表是空的，于是
// 「换版/重启后的第一发」重新撞一遍同一个 400（用户 2026-09-22 报的就是它：
// Codex → GLM 的第一发，`stream disconnected before completion`）。
//
// 只记「还没落盘的」：已经写进缓存的目标不该被反复重写。盘上已经有的那几位在
// 这里算「已落盘」，所以正常跑起来之后待写集合是空的，落盘只在真学到新东西时
// 发生一次。
type PendingQuirks struct {
	mu      sync.Mutex
	entries map[Target]quirk.Flag
}

// Record 记一笔「这个目标此刻有这些毛病」。返回 true 表示这是新学到的
// （调用方据此决定要不要打日志，免得同一件事每个请求刷一行）。
func (p *PendingQuirks) Record(t Target, flags quirk.Flag) bool {
	if t.Provider == "" || t.Model == "" || flags == 0 {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries == nil {
		p.entries = map[Target]quirk.Flag{}
	}
	if p.entries[t]&flags == flags {
		return false
	}
	p.entries[t] |= flags
	return true
}

// Flush 把待写的那几笔落盘。
//
// 由数据面在停机时叫一次（见 forward.Server.Shutdown）。**必须是一次原子写**：
// 进程已经走到停机这一步，写到一半被打断的文件读回来是坏缓存——
// 而那正好发生在「换版之后」这个最需要它对的时刻（同一条理由见
// forward 里那份「停机落盘」的说明）。
func (p *PendingQuirks) Flush() error {
	p.mu.Lock()
	entries := p.entries
	p.entries = nil
	p.mu.Unlock()
	if len(entries) == 0 {
		return nil
	}
	_, err := MergeQuirks(entries)
	return err
}

// DiskQuirks 报告盘上此刻记着的、带毛病位的目标——启动时用它回答「缓存里有没有
// 值得装回来的东西」，不花 token（同一条见 LoadCachedCapabilities）。
func DiskQuirks() []Target {
	c, _ := loadCapabilityCache()
	var out []Target
	for raw, e := range c.Targets {
		if e.Quirks != 0 {
			out = append(out, splitTarget(raw))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func (c *capabilityCache) capture(t Target) {
	var supported, known dialect.Cap
	for _, cap := range []dialect.Cap{
		dialect.CapOpenAI, dialect.CapAnthropic, dialect.CapCountTokens,
	} {
		if ok, seen := dialect.Supports(t.Provider, t.Model, cap); seen {
			known |= cap
			if ok {
				supported |= cap
			}
		}
	}
	// 逐位问，不写死某一位：`Flag` 是位掩码，恢复侧（apply）读的是整张掩码，
	// 所以这里漏一位的症状是**那一位永远存不进去**（见 quirk.AllFlags 的说明）。
	var flags quirk.Flag
	for _, f := range quirk.AllFlags() {
		if quirk.Default.Has(t.Provider, t.Model, f) {
			flags |= f
		}
	}
	c.mu.Lock()
	c.Targets[t.String()] = capabilityEntry{
		Supports: uint32(supported), Known: uint32(known),
		Quirks: uint32(flags), Checked: time.Now(),
	}
	c.mu.Unlock()
}

func (c *capabilityCache) save() error {
	c.mu.Lock()
	raw, err := json.MarshalIndent(c, "", "  ")
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(paths.ProbeCacheFile()), 0o2770); err != nil {
		return err
	}
	tmp := paths.ProbeCacheFile() + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o660); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o660); err != nil {
		return err
	}
	return os.Rename(tmp, paths.ProbeCacheFile())
}

// LoadCachedCapabilities restores learned dialect/quirk facts without spending
// tokens. The daemon calls this on startup; probe calls it before deciding
// whether auxiliary checks are needed.
//
// 返回错误时**照常装回能装的部分**（坏文件 = 空缓存），只是把「为什么没装上」
// 交出来。调用方是守护进程，它接的是日志出口——不报的话，现象是「重启之后
// 每个上游都要重新撞一次 404 / 400 才学回来」，而原因（缓存读不出来）没人知道。
func LoadCachedCapabilities() error {
	c, err := loadCapabilityCache()
	for raw := range c.Targets {
		parts := splitTarget(raw)
		c.apply(parts)
	}
	return err
}

// splitTarget 是 Target.String() 的逆。切法归 dialect 定义（两边必须一致：
// 同一批键在缓存恢复和报告展示里必须是同一个 (provider, model)）。
func splitTarget(raw string) Target {
	provider, model, ok := dialect.SplitKey(raw)
	if !ok {
		return Target{}
	}
	return Target{Provider: provider, Model: model}
}
