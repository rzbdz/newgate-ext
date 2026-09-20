package proto

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	cfg "github.com/rzbdz/newgate/modules/config"
)

// ValidateSnapshot 校验一份快照的形状。**任何一个文件不过就整体拒绝**。
//
// # 为什么是"全有或全无"而不是"跳过坏的那个"
//
// 因为 cfg.Load 对坏掉的 profile 文件是**跳过**（LoadProfile 逐个读，
// 一个坏了不影响别的），不是整体失败。所以半套 materialize **不会报错**：
// 它会安静地跑在新旧混合的配置上——一半档位是新宿主的、一半是旧的，而
// 两边都"看起来正常"。这正是最难查的一类故障（路由到错的上游而不报错）。
//
// 返回的 warnings 是"能应用、但你应该知道"的事（悬空引用、没有密钥来源）。
// 它们与拒绝的区别是：这些文件的**语法**没问题，daemon 加载得动，只是在
// 语义上有疑点——语义问题不该让整队机器收不到配置更新（fail-open）。
func ValidateSnapshot(dir string, files map[string][]byte) (warnings []string, err error) {
	providers := map[string]cfg.Provider{}
	profiles := map[string]cfg.Profile{} // 档位名 → 解析后的 profile

	for _, rel := range sortedKeys(files) {
		if _, ok := ManagedPath(rel); !ok {
			return nil, fmt.Errorf("快照里有不在托管集合里的文件 %q（协议不允许发送托管集合之外的文件）", rel)
		}
		data := files[rel]
		switch {
		case rel == ProvidersName:
			var p cfg.Providers
			if err := json.Unmarshal(data, &p); err != nil {
				return nil, fmt.Errorf("%s 解析失败（不是合法的 provider 表）: %w", rel, err)
			}
			if p.Providers == nil {
				p.Providers = map[string]cfg.Provider{}
			}
			providers = p.Providers
			// 内联密钥：**拒绝**。
			//
			// 判据是"字面量非空"，不是"有 api_key 字段"——`api_key_env` 和留空
			// （密钥走密钥通道）都是正当的。为什么这件事值得拒绝整个快照：密钥
			// 只能走密钥通道是这个设计的地基，而 providers.json 在宿主侧是 git
			// 管的——内联一次就永久留在历史里。宿主上出现这种情况只有两种可能：
			// 版本太旧、或者有人手改了。两种都该被拦下来，而不是让每台副本安静地
			// 把明文密钥写进自己的 providers.json。
			var inline []string
			for name, prov := range p.Providers {
				if strings.TrimSpace(prov.APIKey) != "" {
					inline = append(inline, name)
				}
			}
			if len(inline) > 0 {
				sort.Strings(inline)
				return nil, fmt.Errorf(
					"%s 里 %s 带了内联 api_key：密钥只能走密钥通道（在宿主上 newgate config secrets），配置通道不含密——它在宿主侧是 git 管的，提交一次就永久进历史",
					ProvidersName, strings.Join(inline, "、"))
			}
		case rel == MetaFileName:
			var meta Meta
			if err := json.Unmarshal(data, &meta); err != nil {
				return nil, fmt.Errorf("%s 解析失败: %w", rel, err)
			}
		case strings.HasSuffix(rel, ".kv"):
			// 用 daemon 自己那个解析器（cfg.ParseProfileKV），不是另写一个：
			// 另写一个就成了第二份会漂的真相，"校验通过"也就没有意义了。
			if _, err := cfg.ParseProfileKV(string(data)); err != nil {
				return nil, fmt.Errorf("档位 %s 解析失败: %w", rel, err)
			}
		case strings.HasSuffix(rel, ".json"):
			var pr cfg.Profile
			if err := json.Unmarshal(data, &pr); err != nil {
				return nil, fmt.Errorf("档位 %s 解析失败: %w", rel, err)
			}
			name := profileNameOf(rel)
			if pr.Name == "" {
				pr.Name = name
			}
			profiles[name] = pr
		}
	}

	warnings = append(warnings, semanticWarnings(dir, providers, profiles)...)
	return warnings, nil
}

// semanticWarnings 是"语法没问题、但语义有疑点"的检查。
//
// 全部是警告不是拒绝：一个 provider 名字打错不该让整个车队收不到配置更新。
// 宿主侧的 publish 会**拒绝**这些（那时是发布者当场改，代价为零）；副本侧只报。
func semanticWarnings(dir string, providers map[string]cfg.Provider, profiles map[string]cfg.Profile) []string {
	var warns []string

	// 档位名集合 = 快照里的 + 本机已有的（本地层）。
	// 后者必须算进来：一个 profile `extends` 本机私有的一层是完全正当的用法，
	// 不算进来的话每轮都会报一条假警告——假警告多了，真警告就没人看了。
	known := map[string]bool{}
	for name := range profiles {
		known[name] = true
	}
	if local, err := ListManagedFiles(dir); err == nil {
		for _, rel := range local {
			if strings.HasPrefix(rel, MappingsDir+"/") {
				known[profileNameOf(rel)] = true
			}
		}
	}

	missingProvider := map[string][]string{} // provider 名 → 用到它的档位
	for _, name := range sortedProfileNames(profiles) {
		pr := profiles[name]
		check := func(where string, b cfg.Binding) {
			if b.IsRef() {
				return // 引用由 resolve 展开，展开不了会有它自己的诊断
			}
			if b.Provider == "" {
				return
			}
			if _, ok := providers[b.Provider]; !ok {
				missingProvider[b.Provider] = append(missingProvider[b.Provider], name+"."+where)
			}
		}
		for role, cands := range pr.Roles {
			for i, b := range cands {
				check(fmt.Sprintf("%s[%d]", role, i), b)
			}
		}
		if pr.Fallback != nil {
			check("fallback", *pr.Fallback)
		}
		if pr.Extends != "" && !known[pr.Extends] {
			warns = append(warns, fmt.Sprintf("档位 %s extends %q，但这份配置里没有那个档位", name, pr.Extends))
		}
	}
	for _, prov := range sortedKeysOf(missingProvider) {
		warns = append(warns, fmt.Sprintf("档位 %s 引用了 provider %q，但 provider 表里没有它",
			strings.Join(missingProvider[prov], "、"), prov))
	}

	// 没有密钥来源的 provider：只提醒，不拒绝——密钥可能就在密钥通道里，
	// 而这里看不到那一份（两通道独立）。
	for _, name := range sortedProviderNames(providers) {
		prov := providers[name]
		if strings.TrimSpace(prov.APIKey) == "" && strings.TrimSpace(prov.APIKeyEnv) == "" {
			warns = append(warns, fmt.Sprintf("provider %s 没有 api_key_env，密钥得来自密钥通道（宿主上 config secrets）", name))
		}
	}
	return warns
}

// Result 是一次落盘的结果。
type Result struct {
	Written   []string          // 真的改变了内容的文件
	Unchanged []string          // 内容一致、刻意没碰的文件
	Removed   []string          // 这份快照不再包含、因而被删掉的托管文件
	Manifest  map[string]string // 新的 applied 清单（相对路径 → sha256）
}

// Materialize 把一份**已校验**的快照落到配置目录。全有或全无。
//
// # 为什么是"写 + 删"而不是只写
//
// 只写的话，宿主把 `prod` 改名成 `production` 之后，副本上那个 `prod` 会
// **永远留着**：`@prod` 之类的引用继续解析得到东西，用户以为旧的已经下线了。
// 托管集合的意义就是"这组文件由宿主说了算"，所以清单里我们写过、而新快照不再
// 包含的文件要删掉。
//
// 界限也是明确的：**只删我们写过的**（applied 清单里的）。本地层的文件
// （mappings/local.json 那种）永远不会被这个函数碰。
//
// # 两阶段提交
//
// 先把所有要变的文件写成 `.tmp`，再逐个 rename。每个 rename 是原子的，而
// 两个 rename 之间的窗口是微秒级——watcher 一秒轮询一次，撞上的概率可以忽略。
// 一步写一个文件的话，中途失败就会留下半套（而那正是 ValidateSnapshot 的注释
// 里说的、cfg.Load 会安静接受的那种状态）。
//
// 落盘全程走 os.Root：payload 来自网络，文件名不该有能力把写入引到配置目录
// 之外（`../` 已经在 ManagedPath 里拦过一次，这里是第二道，而且它挡的还包括
// 符号链接——os.Root 的解析不会跟随路径中间指向外部的链接）。
func Materialize(dir string, files map[string][]byte, applied map[string]string) (Result, error) {
	res := Result{Manifest: map[string]string{}}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return res, fmt.Errorf("打不开配置目录 %s: %w", dir, err)
	}
	defer root.Close()

	// mappings/ 可能还不存在（全新机器、或从没写过档位的机器）。
	//
	// 传 0o770 而不是仓库别处那样写 0o2770：**os.Root.MkdirAll 会校验 mode 并
	// 拒绝 0o2770**（"unsupported file mode"）。原因是它按 FileMode 的位定义
	// 检查，而 setgid 在 Go 里是 os.ModeSetgid（1<<25），不是 0o2000——别处
	// 的 os.MkdirAll(d, 0o2770) 实际上只是靠 syscallMode() 取了低 9 位，setgid
	// 被静默丢掉。这里显式地只写权限位，免得读的人以为 setgid 生效了。
	if err := root.MkdirAll(MappingsDir, 0o770); err != nil {
		return res, err
	}

	type pending struct{ rel, tmp string }
	var pend []pending

	// ---- 阶段 1：准备。算出哪些真的变了，把它们的 .tmp 都写下来 ----
	for _, rel := range sortedKeys(files) {
		data := files[rel]
		sum := hashBytes(data)
		res.Manifest[rel] = sum

		if cur, err := root.ReadFile(rel); err == nil && hashBytes(cur) == sum {
			// 内容一致就不写。这不是优化而是**语义**：不碰 mtime = watcher
			// 的指纹不变 = 不会为一次空转触发全量 reload 和一行日志。
			res.Unchanged = append(res.Unchanged, rel)
			continue
		}
		tmp := tmpNameOf(rel)
		if err := root.WriteFile(tmp, data, 0o660); err != nil {
			return res, fmt.Errorf("写 %s 失败: %w", rel, err)
		}
		pend = append(pend, pending{rel: rel, tmp: tmp})
	}

	// ---- 阶段 2：提交。逐个 rename ----
	for _, p := range pend {
		if err := root.Rename(p.tmp, p.rel); err != nil {
			// 已经把 rename 过的留在新版本、没轮到的留在旧版本。说出来，别让
			// 调用方以为这是一次干净的成功。
			return res, fmt.Errorf("替换 %s 失败（配置可能处于新旧混合状态，下一次同步会重试）: %w", p.rel, err)
		}
		// rename 之后补 chmod：umask 会削掉组写位，而 cfg.writeJSON 的注释
		// 与 CLAUDE.md §3.1 都记着这个坑（root 写出的文件 daemon 写不了）。
		if err := root.Chmod(p.rel, 0o660); err != nil {
			return res, fmt.Errorf("修正 %s 权限失败: %w", p.rel, err)
		}
		res.Written = append(res.Written, p.rel)
	}

	// ---- 阶段 3：删除。清单里我们写过、但这份快照不再包含的 ----
	for _, rel := range sortedApplied(applied) {
		if _, keep := files[rel]; keep {
			continue
		}
		if _, ok := ManagedPath(rel); !ok {
			continue // 已不在托管集合里的老记录，不去碰
		}
		if err := root.Remove(rel); err != nil && !os.IsNotExist(err) {
			return res, fmt.Errorf("删除已下线的 %s 失败: %w", rel, err)
		}
		res.Removed = append(res.Removed, rel)
	}

	return res, nil
}

// ---------- 小工具 ----------

// profileNameOf 从 `mappings/x.json` / `mappings/x.kv` 取出档位名 x。
func profileNameOf(rel string) string {
	base := path.Base(rel)
	return strings.TrimSuffix(strings.TrimSuffix(base, ".json"), ".kv")
}

// tmpNameOf 是同目录临时文件名。必须同目录：跨文件系统的 rename 会退化成
// 一次非原子的 copy（cfg.writeJSON 的 .tmp 也是这个理由）。
func tmpNameOf(rel string) string {
	dir := path.Dir(rel)
	base := path.Base(rel)
	if dir == "." {
		return "." + base + ".tmp"
	}
	return dir + "/." + base + ".tmp"
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedApplied(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedProfileNames(m map[string]cfg.Profile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedProviderNames(m map[string]cfg.Provider) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysOf(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
