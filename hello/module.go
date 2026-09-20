// Package hello 是**外部模块**的最小样例：它证明 modules-ext 这条通路是通的。
//
// 它只做一件事：Start 时往装配日志打一行 hello。零依赖、零提供——正因为如此，
// 它能在任何一张图里装上去，也就成了「external 模块能被装进来吗」这个问题最干净
// 的判据。真要有依赖，它和 modules/ 里的模块写法完全一样（见 README）。
package hello

import (
	"context"
	"log"

	modules "github.com/rzbdz/newgate/go/component"
)

// TypeExample 是分类标签。词汇表归产品层（见 component.Type 的注释），
// 这里给出取值：`newgate plugin` 会按它分组显示，装不认识的值也不会报错。
const TypeExample = "example"

// Greeting 是这一行日志的内容。导出是为了让测试能引用同一份字面量——
// 断言写死字符串的话，改文案要改两处，而它们是同一个事实。
const Greeting = "hello from modules-ext"

// New 声明这个组件。零依赖：它谁也不读、谁也不注册，所以装配顺序对它没有要求。
func New() modules.Component {
	return modules.Component{
		Name: "hello",
		Type: TypeExample,
		Start: func(context.Context, modules.Context) error {
			log.Printf("[hello] %s", Greeting)
			return nil
		},
	}
}
