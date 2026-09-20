package proto

import (
	"io/ioutil"
	"path"
	"sort"
	"strings"
)

// 托管文件集合——**这是唯一的契约**。
//
// 只有这里列出的文件会被搬来搬去。`~/.config/newgate/` 下其余的一切都是
// **机器本地**：state.json、health.json、thinkcache.bin、dump/、backups/、
// probe-capabilities.json、omo-slots.json、bin/、.newgate.pid、.newgate.lock、
// newgate.log，以及 secrets.json（它另走密钥通道，见 secrets.go）。
//
// # 为什么必须是显式 allowlist，不能扫目录
//
// 两个已核实的坑，都会让"扫目录"这个偷懒做法出事：
//
//  1. **`newgate profile kv --write` 会留下 `name.json.bak`**（它转换时把旧
//     json 改名）。而 store.readProfileFile 只认 `.kv` 和 `.json`，所以那个
//     `.bak` 是死文件——扫目录会把它搬到每台机器上，变成一堆既不被读、
//     又会被下一轮漂移守卫当成"本地改动"的垃圾。
//  2. **`omo-slots.json` 必须留在本地**。它被 roleprov.WatchFiles() 盯着，
//     由 `newgate on opencode` 按**本机**情况生成（每个 intra-agent 稳定分到
//     哪个键）。它进共享会与本地接管互相打架：A 机的槽位分配搬到 B 机，B 机
//     的 opencode 就会引用一批本机不存在的键——症状是 unknown-role，不是崩溃，
//     所以会很难查。
const (
	// ProvidersName 是托管集合里的 provider 表。
	ProvidersName = "providers.json"
	// MappingsDir 是托管集合里的档位目录。
	MappingsDir = "mappings"
)

// ManagedPath 判断一个**相对配置目录的斜杠路径**是否属于托管集合，并且是
// 安全的（不会跑出配置目录）。返回规范化后的路径。
//
// 这个函数是唯一的判据：宿主侧生成快照、副本侧校验快照、漂移检测枚举本地文件
// 都走它。三处共用一条规则，才不会出现"发得出去、收不下来"这类错位。
func ManagedPath(rel string) (string, bool) {
	clean, ok := cleanRel(rel)
	if !ok {
		return "", false
	}
	if clean == ProvidersName || clean == MetaFileName {
		return clean, true
	}
	// mappings/<名字>.<json|kv>
	if !strings.HasPrefix(clean, MappingsDir+"/") {
		return "", false
	}
	name := strings.TrimPrefix(clean, MappingsDir+"/")
	if name == "" || strings.Contains(name, "/") {
		return "", false // mappings 下不再有子目录
	}
	// 点开头的是临时/隐藏文件（我们自己的原子写会造 `.<名字>.tmp`）。
	if strings.HasPrefix(name, ".") {
		return "", false
	}
	switch {
	case strings.HasSuffix(name, ".json"):
		// 注意 `.json.bak` 不以 `.json` 结尾，天然被排除——这正是上面坑 1 要的。
		return clean, true
	case strings.HasSuffix(name, ".kv"):
		return clean, true
	}
	return "", false
}

// cleanRel 把一个相对路径规范化，并拒绝一切可能跑出配置目录的形状。
//
// 落盘最终还有 os.Root 把关（materialize.go），这里是第一道：坏路径要在
// **校验阶段**就被拦下（那时还什么都没写），而不是等到写的时候才发现。
func cleanRel(rel string) (string, bool) {
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", false
	}
	if strings.Contains(rel, `\`) {
		return "", false // 反斜杠在 Linux 上不是分隔符，出现就是有人在试探
	}
	if path.IsAbs(rel) {
		return "", false
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	// path.Clean 之后还带 // 或结尾 / 的原文一律不接受（避免 "mappings//x.json"
	// 这种"看着一样、字符串不同"的键在清单里产生两条记录）。
	if clean != rel {
		return "", false
	}
	return clean, true
}

// ListManagedFiles 枚举**本机现存**的托管文件（相对斜杠路径，已排序）。
//
// 给漂移检测用：它要能看见"托管集合里多出来的本地文件"（= 本地层）。
func ListManagedFiles(dir string) ([]string, error) {
	var out []string
	if _, ok := ManagedPath(ProvidersName); ok {
		if fileExists(dir + "/" + ProvidersName) {
			out = append(out, ProvidersName)
		}
	}
	if _, ok := ManagedPath(MetaFileName); ok {
		if fileExists(dir + "/" + MetaFileName) {
			out = append(out, MetaFileName)
		}
	}
	ents, err := ioutil.ReadDir(dir + "/" + MappingsDir)
	if err != nil {
		if isNotExist(err) {
			sort.Strings(out)
			return out, nil
		}
		return nil, err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if rel, ok := ManagedPath(MappingsDir + "/" + e.Name()); ok {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out, nil
}

// shadowOf 返回会**盖住**某个托管文件的本地文件的相对路径。
//
// 只有一种形状：store.readProfileFile 先看 `name.kv`，再看 `name.json`——所以
// 一个非托管的 `mappings/x.kv` 会让托管的 `mappings/x.json` **写了也不生效**。
// 这是最阴的一种漂移：远端明明同步成功了，本机行为不变，且没有任何报错。
func shadowOf(rel string) string {
	if !strings.HasPrefix(rel, MappingsDir+"/") || !strings.HasSuffix(rel, ".json") {
		return ""
	}
	return strings.TrimSuffix(rel, ".json") + ".kv"
}
