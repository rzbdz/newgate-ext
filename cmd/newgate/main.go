// Command newgate 是**这个发行版**的二进制：内核的组合根 + 本发行版装的那些模块。
//
// 它与内核自带的那个 cmd/newgate 只差一件事：**装哪张图**。图由仓库根的规格书
// （dist.json / dist-simple-cli.json）决定，构建期用 -ldflags -X main.spec=<文件名>
// 选一份；没注入时走 manifest.DefaultSpec。
//
// 组合根的逻辑（留痕 → 起图 → 问入口 → 交出去）全在内核的 app.Main 里，本文件只
// 负责说「哪张图、什么版本」。两边各抄一份那段逻辑就会开始漂移，而漂移的症状是
// 「发行版里装配留痕没了 / 退出码不一样」——最难注意到的那类差异。
package main

import (
	"context"
	"os"

	"github.com/rzbdz/newgate-ext/manifest"
	app "github.com/rzbdz/newgate/app"
)

// spec 是本次构建选的规格书文件名，由 -ldflags -X main.spec=… 注入（见 build/build.sh）。
// 空值 = 用 manifest.DefaultSpec（`go run ./cmd/newgate` 与测试走这条）。
var spec string

// 版本三件套同样由 -ldflags 注入，命名与内核 Makefile 保持一致。
var (
	version    = "dev"
	buildTime  = "unknown"
	commitTime = "unknown"
)

func main() {
	os.Exit(app.Main(context.Background(), app.Options{
		Loader:     manifest.Loader{Spec: spec},
		Version:    version,
		BuildTime:  buildTime,
		CommitTime: commitTime,
	}))
}
