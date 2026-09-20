package opencodeomo

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	configapi "github.com/rzbdz/newgate/modules/config"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/resolve"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/modules/gateway/controlplane"
)

type omoCommand struct{}

var _ cliapi.Command = (*omoCommand)(nil)

func (omoCommand) Names() []string { return []string{"omo", "slots"} }

func (omoCommand) Run(host cliapi.Host, args []string) int {
	return cmdOmo(host, args)
}

// cmdOmo 管 omo（opencode 的 oh-my-openagent 插件）的 intra-agent 槽位。
//
// 槽位键（omo-sisyphus / cat-deep …）是**模块贡献的动态角色**：接管时由
// omo 模块注册给框架（runtime/injection/omo.go），框架把它们和档位一视同仁
// 地解析。所以这个命令只做三件事——看一眼现状、改缺省归属、解释解析结果。
//
// 用户不需要记键名：不带参数就是列表。
func cmdOmo(host cliapi.Host, args []string) int {
	if len(args) == 0 {
		return omoList()
	}
	switch args[0] {
	case "ls", "list":
		return omoList()
	case "use", "set":
		if len(args) < 3 {
			return host.Die(64, "用法：newgate omo use <槽位键> <@别的键|档位|provider/模型>")
		}
		return omoUse(host, args[1], args[2], true)
	case "unset", "reset":
		if len(args) < 2 {
			return host.Die(64, "用法：newgate omo unset <槽位键>")
		}
		return omoUse(host, args[1], "", false)
	case "mode":
		if len(args) < 2 {
			return omoMode(host, "")
		}
		return omoMode(host, args[1])
	case "explain":
		if len(args) < 2 {
			return host.Die(64, "用法：newgate omo explain <槽位键>")
		}
		return omoExplain(host, args[1])
	}
	return host.Die(64, "用法：newgate omo [ls | use <键> <归属> | unset <键> | mode current|suggested | explain <键>]")
}

func omoRegistry() *OmoSlots {
	reg := ReadOmoSlots()
	if reg == nil {
		fmt.Println(style.Item(style.Skip, "没有槽位登记表 "+SlotsFile()))
		fmt.Println(style.Hint("接管一次即生成：newgate on opencode"))
		os.Exit(0)
	}
	return reg
}

// omoList 槽位清单使用纵向卡片。键、槽位和模型标识符都是不可分割的信息，
// 不能为了六列表格从中间硬切；属性放在后续缩进行。
func omoList() int {
	reg := omoRegistry()
	fmt.Println(style.Title("newgate omo",
		fmt.Sprintf("%d 个槽位键 · 模式 %s", len(reg.Slots), omoModeName(reg))))
	fmt.Println(style.Rule(78))
	fmt.Println(style.Hint("current=接管现状 · suggested=建议 · 切换：newgate omo mode <模式>"))

	fmt.Println()
	for _, s := range reg.Slots {
		eff := reg.SlotBinding(s.Key)
		if _, ok := reg.Overrides[s.Key]; ok {
			eff = style.Cyan(eff) + style.Dim("*")
		}
		was := dash(s.Was)
		if s.Variant != "" {
			was += style.Dim("(" + s.Variant + ")")
		}
		sug := style.Dim(dash(s.Suggested))
		if s.Suggested != "" && s.Suggested != s.Current {
			sug = style.Yellow(s.Suggested)
		}
		fmt.Print(omoSlotCard(s.Key, s.Kind+"/"+s.Name, was, s.Current, sug, eff))
	}
	if len(reg.Overrides) > 0 {
		fmt.Println(style.Hint("* 有覆盖（newgate omo use 写入），优先级最高"))
	}

	var diff []OmoSlot
	for _, s := range reg.Slots {
		if s.Suggested != "" && s.Suggested != s.Current {
			diff = append(diff, s)
		}
	}
	if len(diff) > 0 {
		fmt.Print(style.Section(fmt.Sprintf("建议与现状不同（%d 个）", len(diff))) +
			style.Dim("   newgate omo mode suggested 全部采纳") + "\n")
		for _, s := range diff {
			fmt.Println(style.Item(style.Skip, s.Key))
			line := s.Current + " → " + style.Yellow(s.Suggested)
			if s.Why != "" {
				line += " · " + style.Dim(s.Why)
			}
			fmt.Println(style.Hint(line))
		}
	}
	fmt.Println()
	fmt.Println(style.Hint("profile 里直接写键名同样有效：omo-sisyphus=@normal, terra/medium"))
	return 0
}

func omoSlotCard(key, slot, was, current, suggested, effective string) string {
	var out strings.Builder
	identifierLine := func(label, value string) {
		out.WriteString("    " + style.Dim(label) + " " + value + "\n")
	}
	out.WriteString("  " + style.Mark(style.Skip) + " " + key + "\n")
	identifierLine("槽位", slot)
	identifierLine("接管前", was)
	identifierLine("现状", current)
	identifierLine("建议", suggested)
	identifierLine("生效", effective)
	return out.String()
}

func omoModeName(reg *OmoSlots) string {
	if reg.Mode == "suggested" {
		return "suggested"
	}
	return "current"
}

// omoDiffCount 建议档位与现状不同的键数（有覆盖的不算——那些已经定了）。
func omoDiffCount(reg *OmoSlots) int {
	if reg == nil {
		return 0
	}
	n := 0
	for _, s := range reg.Slots {
		if _, over := reg.Overrides[s.Key]; over {
			continue
		}
		if s.Suggested != "" && s.Suggested != s.Current {
			n++
		}
	}
	return n
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// omoUse 写/删一个覆盖。value 为空表示删除。
func omoUse(host cliapi.Host, key, value string, set bool) int {
	reg := ReadOmoSlots()
	if reg == nil {
		return host.Die(65, "没有 omo 槽位登记表；先 newgate on opencode")
	}
	if _, ok := reg.SlotOf(key); !ok {
		return host.Die(64, fmt.Sprintf("没有这个槽位键 %q（newgate omo 看列表）", key))
	}
	if set {
		if err := validateBindingValue(value); err != nil {
			return host.Die(64, err.Error())
		}
		if reg.Overrides == nil {
			reg.Overrides = map[string]string{}
		}
		reg.Overrides[key] = value
	} else {
		delete(reg.Overrides, key)
	}
	if err := WriteOmoSlots(reg); err != nil {
		return host.Die(70, "写注册表失败: "+err.Error())
	}
	if set {
		fmt.Println(style.Item(style.OK, key+" 缺省归属 → "+style.Cyan(value)))
	} else {
		fmt.Println(style.Item(style.OK, key+" 覆盖已删除，回到 "+omoModeName(reg)))
	}
	host.NotifyProxy()
	fmt.Println(style.Hint("1 秒内自动生效；profile 里显式写了这个键则以 profile 为准"))
	return 0
}

func validateBindingValue(v string) error {
	if strings.HasPrefix(v, "@") {
		key := strings.TrimPrefix(v, "@")
		if key == "" || strings.ContainsAny(key, "/ ") {
			return fmt.Errorf("引用写成 @键名（如 @normal），得到 %q", v)
		}
		return nil
	}
	if strings.Contains(v, "/") {
		if _, err := domain.ParseBindingString(v); err != nil {
			return err
		}
		return nil
	}
	if !domain.IsKnownRole(v) {
		return fmt.Errorf("%q 既不是档位（heavy/normal/mid/light/vision）、"+
			"也不是已注册的动态角色键、也不是 provider/模型", v)
	}
	return nil
}

func omoMode(host cliapi.Host, mode string) int {
	reg := omoRegistry()
	if mode == "" {
		fmt.Println(style.Title("newgate omo mode", omoModeName(reg)))
		fmt.Println(style.Rule(64))
		t := style.NewTable("模式", "含义")
		t.Row("current", style.Dim("每个键按接管时的现状（默认，行为不变）"))
		t.Row("suggested", style.Dim("每个键按建议，由模型体格与 variant 强度推出"))
		fmt.Print(t.String())
		fmt.Println(style.Hint("切换：newgate omo mode current|suggested"))
		return 0
	}
	switch mode {
	case "current", "suggested":
	default:
		return host.Die(64, "模式只认 current / suggested")
	}
	reg.Mode = mode
	if err := WriteOmoSlots(reg); err != nil {
		return host.Die(70, "写注册表失败: "+err.Error())
	}
	fmt.Println(style.Item(style.OK, "模式 → "+style.Cyan(mode)))
	n := 0
	for _, s := range reg.Slots {
		if _, ok := reg.Overrides[s.Key]; ok {
			continue
		}
		if s.Suggested != "" && s.Suggested != s.Current {
			n++
		}
	}
	if mode == "suggested" {
		fmt.Println(style.Hint(fmt.Sprintf("%d 个键的归属随之改变（有覆盖的不受影响）；不满意：newgate omo mode current", n)))
	} else {
		fmt.Println(style.Hint("已回到接管时的现状"))
	}
	host.NotifyProxy()
	return 0
}

// omoExplain 把一个槽位键解析成实际的 fallback 链——「为什么不是我想的那个」
// 只能靠这个回答（docs/04-configuration.md）。
func omoExplain(host cliapi.Host, key string) int {
	reg := ReadOmoSlots()
	if reg == nil {
		return host.Die(65, "没有 omo 槽位登记表；先 newgate on opencode")
	}
	// 先装快照：角色键是 store.Load 里跟着刷新的（core/roleprov），
	// 不先装一次的话 IsKnownRole 会说不认识我们自己刚注册的键。
	snap, err := store.Load()
	if err != nil {
		return host.Die(65, err.Error())
	}
	if !domain.IsKnownRole(key) {
		return host.Die(64, fmt.Sprintf("%q 不是已知角色键（newgate omo ls 看清单）", key))
	}
	st := snap.State
	heads := map[string][]string{st.DefaultProfile: {"默认"}}
	for agent, p := range st.Active {
		heads[p] = append(heads[p], agent)
	}
	names := make([]string, 0, len(heads))
	for h := range heads {
		names = append(names, h)
	}
	sort.Strings(names)

	sub := "模块动态角色，非 omo 槽位"
	if sl, ok := reg.SlotOf(key); ok {
		sub = sl.Kind + "/" + sl.Name
	}
	fmt.Println(style.Title("newgate omo "+key, sub))
	fmt.Println(style.Rule(64))

	if sl, ok := reg.SlotOf(key); ok {
		t := style.NewTable("字段", "值")
		t.Row("接管前", style.Dim(dash(sl.Was)+" "+dash(sl.Variant)))
		t.Row("现状", sl.Current)
		t.Row("建议", dash(sl.Suggested))
		t.Row("生效", style.Cyan(reg.SlotBinding(key)))
		fmt.Print(t.String())
		if sl.Why != "" {
			fmt.Println(style.Hint("建议依据 " + sl.Why))
		}
	}

	// 熔断表快照直接读控制面叶子（见 cliapi.Host 的说明：LiveRouting 不再走界面）。
	_, doc := controlplane.State()
	available, rank := doc.Available(), doc.Rank()
	for _, head := range names {
		fmt.Print(style.Section("链头 "+head) + style.Dim("   "+strings.Join(heads[head], ", ")) + "\n")
		steps, skips := resolve.BuildChain(key, snap.Profiles, snap.Providers, resolve.Opts{
			Active:    head,
			Available: available,
			Rank:      rank,
			MaxSteps:  st.Chain.Attempts(),
		})
		if len(steps) == 0 {
			fmt.Println(style.Item(style.Bad, "无可用候选"))
		} else {
			configapi.PrintChain(steps)
		}
		if len(skips) > 0 {
			configapi.PrintSkips(skips)
		}
	}
	return 0
}
