// Command orderprobe 跑一遍**真的**装配解析，把每份规格书的启动顺序打出来。
//
// 它是 tools/distgen 的第二趟：第一趟把规格书编成 manifest/modules_gen.go，然后
// 跑这个程序（一个**新的进程**，所以读到的是刚写下去的那份清单），拿回顺序，
// 再写成 manifest/order_gen.go。
//
// # 为什么要新起一个进程，而不是 distgen 自己算
//
// distgen 进程里那份 manifest 是**编译进去的**——也就是上一次生成的结果。规格书
// 刚改过的时候（加了模块、换了 disable），拿旧清单算出来的顺序会悄悄地错，而
// 「顺序错了」的症状离「清单过期」非常远。新起一个进程就没有这个问题：它读到
// 的必然是磁盘上那份。
//
// # 它算的是「从头」那一份
//
// `Specs()` 交出来的是**朴素**的选择（没有顺序），所以这里 `Load()` 出来的就是
// 声明顺序，`Resolve` 会老老实实建边、排序——正是我们要冻下来的那个结果。
// 顺带这也是一条自检：每份规格书都真的能装起来，装不起来就生成失败（而不是等
// 运行期第一次敲命令）。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	modules "github.com/rzbdz/newgate/component"

	"github.com/rzbdz/newgate-ext/manifest"
)

func main() {
	specs := manifest.Specs()
	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)

	out := map[string][]string{}
	for _, name := range names {
		components, err := specs[name].Load()
		if err != nil {
			fail(name, err)
		}
		plan, err := modules.Resolve(staticLoader(components))
		if err != nil {
			fail(name, err)
		}
		out[name] = plan.Names()
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fail("(encoding)", err)
	}
}

func fail(name string, err error) {
	fmt.Fprintf(os.Stderr, "orderprobe: spec %s does not assemble: %v\n", name, err)
	os.Exit(1)
}

// staticLoader 交出一批已经造好的组件（同 app 包测试里那个同名助手）。
type staticLoader []modules.Component

func (s staticLoader) Load() ([]modules.Component, error) { return s, nil }
