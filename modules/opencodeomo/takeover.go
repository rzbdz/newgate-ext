package opencodeomo

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
)

const ProviderID = "newgate"

// Report 记录一次接管做了什么，给用户看。
type Report = agentapi.TakeoverReport

// 备份、还原、原子写这套 2026-09-21 **搬去了内核**（见 confighook/backup.go）：
// codex 的接管要写的是同一套东西，留在本模块里的代价只能是抄一遍，而抄来的那份
// 迟早漂移——漂移掉的是 `original/` 的写入防护，症状是「用户的原配置永久丢失」，
// 测试里一点声音都没有。
//
// 这里只剩「哪份文件、什么算已被接管」这两条**本家**的知识。

// originalPath / backup / writeAtomic 是本文件里对内核那三件的叫法，保留短名字：
// 调用点很多，而写成 agentapi.X 会把这一屏塞满包名。
func originalPath(target string) string { return agentapi.OriginalPath(target) }

// backup 存一份原件。判据是「内容里有没有我们写的 provider 键」——它由
// Confighook 决定，本模块只提供这个字符串。
func backup(target string) error { return agentapi.BackupFile(target, tainted) }

// tainted 是 opencode 那边的「已被接管」判据：文件里有 `"newgate"` 这个 provider
// 键（或指向它某个模型的 `"newgate/…"`）。
func tainted(b []byte) bool {
	return agentapi.ContainsMarker(b, `"`+ProviderID+`"`) ||
		agentapi.ContainsMarker(b, `"`+ProviderID+`/`)
}

func writeAtomic(path string, b []byte) error { return agentapi.WriteAtomic(path, b, 0o600) }

func marshal(v interface{}) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ---------- opencode.json ----------

// buildNewgateProvider 生成指向本地代理的 provider 块。
// 模型名就是语义档位——这是 docs/01-product.md 的核心：配置里不写真实模型名。
//
// extra 是模块槽位键（omo-sisyphus / cat-deep …）：omo 的配置文件会引用
// 这些 id，得先在 provider 的 models 里登记出来，否则 opencode 认不出这个模型。
// 键从哪来由模块决定（见 omo.go），这里不做任何 omo 相关的判断。
func buildNewgateProvider(port int, extra []string) map[string]interface{} {
	models := map[string]interface{}{}
	add := func(id string) {
		models[id] = map[string]interface{}{
			"name": "newgate " + id,
			"limit": map[string]interface{}{
				"context": 1000000,
				"output":  64000,
			},
		}
	}
	for _, r := range domain.Roles {
		add(r)
	}
	for _, k := range extra {
		if k != "" && !domain.IsRole(k) {
			add(k)
		}
	}
	return map[string]interface{}{
		"npm":  "@ai-sdk/openai-compatible",
		"name": "newgate (local proxy)",
		"options": map[string]interface{}{
			"baseURL": fmt.Sprintf("http://127.0.0.1:%d/v1", port),
			"apiKey":  "newgate-local",
		},
		"models": models,
	}
}

// ApplyOpencode 合并式改写：只增加 provider.newgate 并把 model/small_model 指过去，
// 用户原有的 provider 块、plugin、以及任何其它键全部原样保留。
func ApplyOpencode(target string, port int, extra []string) (*Report, error) {
	rep := &Report{File: target}
	raw, err := ioutil.ReadFile(target)
	if err != nil {
		return rep, err
	}
	if err := backup(target); err != nil {
		return rep, err
	}

	// 用 map 保留未知键
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return rep, i18n.Ef(err, "cannot parse {file}: {err}", i18n.A{"file": target})
	}

	provs := map[string]json.RawMessage{}
	if v, ok := root["provider"]; ok {
		if err := json.Unmarshal(v, &provs); err != nil {
			return rep, i18n.Ef(err, "the provider field is not an object: {err}", nil)
		}
	}
	np, err := marshal(buildNewgateProvider(port, extra))
	if err != nil {
		return rep, err
	}
	provs[ProviderID] = json.RawMessage(np)
	if b, err := marshal(provs); err == nil {
		root["provider"] = json.RawMessage(b)
	} else {
		return rep, err
	}

	// 顶层 model / small_model 指向语义档位
	root["model"] = json.RawMessage(`"` + ProviderID + `/normal"`)
	root["small_model"] = json.RawMessage(`"` + ProviderID + `/light"`)
	rep.Rewrites = append(rep.Rewrites,
		"model -> "+ProviderID+"/normal",
		"small_model -> "+ProviderID+"/light",
		i18n.T("provider.{id} injected (all pre-existing providers kept)",
			i18n.A{"id": ProviderID}))

	out, err := marshal(root)
	if err != nil {
		return rep, err
	}
	return rep, writeAtomic(target, out)
}

// ---------- oh-my-openagent.json ----------

// ApplyOpenagent 把 omo 的 intra-agent 槽位接到 newgate 上。
//
// 与老版本的区别：**保留槽位身份**。以前每个 `"model": "厂商/模型"` 都被换成
// 按体格算出来的 `newgate/normal`，sisyphus 和 librarian 从此长得一模一样；
// 现在换成按槽位起的键 `newgate/omo-sisyphus` / `newgate/cat-deep`，体格
// （它现在算哪一档、建议算哪一档）记在同目录的 omo-slots.json 里。
//
// 三件事，每一件都不改写用户看不出来的东西：
//
//	槽位键：agents.<名> → omo-<名>，categories.<名> → cat-<名>（omo.go）
//	注册表：键 → 现状档位 / 建议档位 / 原模型名，写 omo-slots.json
//	链的归属：注册表里的 default 就是这个键在 profile 里没写时的缺省
//
// `fallback_models` 收敛成一条（同一个键）：链已经由 newgate 在服务端兜底，
// 留在配置里的多条 fallback 只会让一次失败重试同一件事。原列表记进注册表
// （was_fallbacks），原文件也还在 backups/original/。
//
// 幂等：已经是 newgate/xxx 的槽位不会被再翻译一遍；重新接管时用注册表和
// 备份里的原始文件找回「原来是什么模型」，所以反复接管不会丢信息。
func ApplyOpenagent(target string, port int) (*Report, error) {
	rep := &Report{File: target}
	raw, err := ioutil.ReadFile(target)
	if err != nil {
		return rep, err
	}
	if err := backup(target); err != nil {
		return rep, err
	}

	var root map[string]interface{}
	if err := json.Unmarshal(raw, &root); err != nil {
		return rep, i18n.Ef(err, "cannot parse {file}: {err}", i18n.A{"file": target})
	}

	slots, err := DiscoverSlots(target)
	if err != nil {
		return rep, err
	}
	prev := ReadOmoSlots()
	orig := map[string]Slot{}
	for _, s := range OriginalSlots() {
		orig[s.Kind+"/"+s.Name] = s
	}

	reg := &OmoSlots{}
	if prev != nil {
		reg.Mode = prev.Mode
		reg.Overrides = prev.Overrides
	}
	byKey := map[string]OmoSlot{}
	for _, sl := range slots {
		key := sl.Key()
		origModel, origVariant := "", ""
		if o, ok := orig[sl.Kind+"/"+sl.Name]; ok {
			origModel, origVariant = o.Model, o.Variant
		}

		// was = 接管前的真实模型名。三处信息按可信度排序：备份里的原始文件
		// （从没被我们碰过）> 上次接管记下的 > 当前文件（只有第一次接管时
		// 它还是真名字，之后就是我们写的 newgate/xxx 了）。
		was := ""
		if origModel != "" && !isTakenOver(origModel) {
			was = origModel
		} else {
			if p, ok := prev.SlotOf(key); ok {
				was = p.Was
			}
			if was == "" && !isTakenOver(sl.Model) {
				was = sl.Model
			}
		}

		variant := firstNonEmpty(sl.Variant, origVariant)
		current, note := currentTier(sl.Model, key, prev, was)
		suggested, swhy := Suggest(was, variant)
		entry := OmoSlot{
			Key: key, Kind: sl.Kind, Name: sl.Name,
			Was: was, WasFallbacks: sl.Fallbacks, Variant: variant,
			Current: current, Suggested: suggested, Default: current,
			Why: joinWhy(note, swhy),
		}
		reg.Slots = append(reg.Slots, entry)
		byKey[key] = entry
		rep.Rewrites = append(rep.Rewrites, i18n.T("{section}.{name} → {provider}/{key} (now {now}{suggested})",
			i18n.A{"section": sl.Kind + "s", "name": sl.Name, "provider": ProviderID,
				"key": key, "now": current, "suggested": suggestTag(suggested, current)}))
	}
	// 注册表里的 default 以 overrides/mode 为准（写文件的人看得见最终归属）
	for i := range reg.Slots {
		reg.Slots[i].Default = reg.SlotBinding(reg.Slots[i].Key)
		if reg.Slots[i].Default == "" {
			reg.Slots[i].Default = reg.Slots[i].Current
		}
		byKey[reg.Slots[i].Key] = reg.Slots[i]
	}
	if err := WriteOmoSlots(reg); err != nil {
		return rep, i18n.Ef(err, "cannot write the slot registry: {err}", nil)
	}
	rep.Rewrites = append(rep.Rewrites,
		i18n.N("slot registry → {file} ({n} key; default decides which tier each key follows)",
			"slot registry → {file} ({n} keys; default decides which tier each key follows)",
			len(reg.Slots), i18n.A{"file": SlotsFile(), "n": len(reg.Slots)}))

	// 改写：只动 agents.*.model / categories.*.model 和它们的 fallback_models
	fallbackIsObject := map[string]bool{}
	for _, s := range slots {
		fallbackIsObject[s.Key()] = s.FallbackObjects
	}
	for kind, plural := range map[string]string{"agent": "agents", "category": "categories"} {
		section, ok := root[plural].(map[string]interface{})
		if !ok {
			continue
		}
		for name, rawNode := range section {
			node, ok := rawNode.(map[string]interface{})
			if !ok {
				continue
			}
			key, ok := byKey[slotKey(kind, name)]
			if !ok {
				continue // DiscoverSlots 跳过的（没有 model 字段）
			}
			node["model"] = ProviderID + "/" + key.Key
			if _, has := node["fallback_models"]; has {
				// 原样保留元素形态（对象 or 字符串），只把内容收敛成同一个键。
				if fallbackIsObject[key.Key] {
					node["fallback_models"] = []interface{}{
						map[string]interface{}{"model": ProviderID + "/" + key.Key}}
				} else {
					node["fallback_models"] = []interface{}{ProviderID + "/" + key.Key}
				}
			}
		}
	}

	out, err := marshal(root)
	if err != nil {
		return rep, err
	}
	return rep, writeAtomic(target, out)
}

// currentTier 「现状」= 不启用建议时这个槽位落在哪一档——也就是老版本的行为。
//
//	newgate/<档位>  老版本接管过的文件：原样保留，用户现在的行为不变
//	newgate/<键>    新版本接管过的：沿用注册表里记的现状
//	其余（真名字）  第一次接管：按体格归类，模糊命中要说不出来路
func currentTier(model, key string, prev *OmoSlots, was string) (tier, note string) {
	if strings.HasPrefix(model, ProviderID+"/") {
		rest := strings.TrimPrefix(model, ProviderID+"/")
		if domain.IsRole(rest) {
			return rest, i18n.T("keeping the tier an older takeover wrote", nil)
		}
		if p, ok := prev.SlotOf(key); ok && p.Current != "" {
			return p.Current, ""
		}
	}
	name := firstNonEmpty(was, model)
	t, exact := ClassifyModel(name)
	if !exact {
		return t, i18n.T("model name {model} matched no rule; falling back to mid",
			i18n.A{"model": name})
	}
	return t, ""
}

func slotKey(kind, name string) string {
	if kind == "category" {
		return OmoCatPrefix + name
	}
	return OmoAgentPrefix + name
}

func isTakenOver(model string) bool {
	return strings.HasPrefix(model, ProviderID+"/") || strings.HasPrefix(model, "@")
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func joinWhy(note, why string) string {
	switch {
	case note == "":
		return why
	case why == "":
		return note
	default:
		return i18n.T("{note}; {why}", i18n.A{"note": note, "why": why})
	}
}

func suggestTag(suggested, current string) string {
	if suggested == "" || suggested == current {
		return ""
	}
	return i18n.T(", {tier} suggested", i18n.A{"tier": suggested})
}

// ---------- 编排 ----------

func IsTakenOver(target string) bool {
	b, err := ioutil.ReadFile(target)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), `"`+ProviderID+`/`) ||
		strings.Contains(string(b), `"`+ProviderID+`"`)
}

// ApplyAll 接管所有目标文件。找不到的文件跳过并记录，不视为错误。
func ApplyAll(port int) ([]*Report, error) {
	// 先发现模块槽位键（omo 的 intra-agent）。opencode.json 的 provider 块
	// 要把这些 id 先登记出来，改 omo 配置时引用它们才认得出。哪些键、叫什么
	// 由模块决定（omo.go），这里只是把两边对起来。
	var reps []*Report
	var extra []string
	if t := omoTargetPath(); t != "" {
		slots, err := DiscoverSlots(t)
		if err != nil {
			// 发现失败就说出来，别让接管「成功」了却什么都没改
			reps = append(reps, &Report{File: t, Skipped: err.Error()})
		}
		for _, s := range slots {
			extra = append(extra, s.Key())
		}
	}
	if reg := ReadOmoSlots(); reg != nil {
		for _, s := range reg.Slots {
			extra = append(extra, s.Key)
		}
	}

	for _, t := range TargetFiles() {
		if _, err := os.Stat(t); os.IsNotExist(err) {
			reps = append(reps, &Report{File: t, Skipped: i18n.T("the file does not exist", nil)})
			continue
		}
		var rep *Report
		var err error
		if strings.Contains(filepath.Base(t), "openagent") {
			rep, err = ApplyOpenagent(t, port)
		} else {
			rep, err = ApplyOpencode(t, port, extra)
		}
		if err != nil {
			return reps, i18n.Ef(err, "taking over {file} failed: {err}", i18n.A{"file": t})
		}
		reps = append(reps, rep)
	}
	return reps, nil
}

// RestoreAll 从 original/ 还原。这是逃生舱：还原后删掉 newgate 也能正常用。
func RestoreAll() ([]string, error) {
	var done []string
	for _, t := range TargetFiles() {
		orig := originalPath(t)
		b, err := ioutil.ReadFile(orig)
		if err != nil {
			continue // 没备份说明没接管过
		}
		if err := writeAtomic(t, b); err != nil {
			return done, i18n.Ef(err, "restoring {file} failed: {err}", i18n.A{"file": t})
		}
		done = append(done, t)
	}
	// 槽位键随释放失效；用户手写的 overrides 留着。清不掉要说出来——留着
	// 的键下一次接管会被当成用户的选择（见 ClearOmoSlots 的说明）。
	if err := ClearOmoSlots(); err != nil {
		return done, i18n.Ef(err, "restore finished, but the slot table was not cleared (the next takeover will reuse the old assignment): {err}", nil)
	}
	return done, nil
}
