package kernel

import (
	"context"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/entry"
	app "github.com/rzbdz/newgate/app"
	modules "github.com/rzbdz/newgate/component"
)

// onlyEntry 是「一张只有入口账本的图」——内核唯一必要的那一个模块，没人申报入口。
type onlyEntry struct{}

func (onlyEntry) Load() ([]modules.Component, error) {
	return []modules.Component{entry.New()}, nil
}

// 没人认领这次调用时，Main 必须说一句人话退出（69），而不是崩、也不是 0。
//
// 这条以前住在 cmd/newgate 里，于是只有「真起一个进程」才能测。搬进 app 之后
// 它成了一个普通函数调用——**这就是把组合根逻辑放进内核的回报**：发行版的 main
// 也走同一条路径，两边不会再各自漂移出一套退出码。
func TestMainExitsWhenNoEntryClaims(t *testing.T) {
	t.Setenv("NEWGATE_HOME", t.TempDir()) // 装配留痕要往配置目录写日志

	if code := app.Main(context.Background(), app.Options{Loader: onlyEntry{}}); code != 69 {
		t.Errorf("只有入口账本、没人申报时退出码 = %d，想要 69", code)
	}
}
