// Package distinfo 是「这个二进制是哪份发行版、从哪来」的叶子包。
//
// 三个事实，都在构建期定下来：
//
//   - 选的是哪份规格书（spec）——由 build/build.sh 用
//     `-ldflags -X github.com/rzbdz/newgate-ext/manifest/distinfo.spec=<文件名>` 注入；
//   - 那份规格书叫什么、发在哪个 GitHub 仓库——由 tools/distgen 从 dist*.json
//     生成进 specs_gen.go；
//   - 编它的源码树在哪（source）——同样由 build.sh 注入。
//
// # 为什么是一个单独的叶子包
//
// 读它的是**模块**（self-update 要知道自己该下哪个产物、该去哪个仓库编），而
// manifest 包 import 了每一个模块——模块抬头 import manifest 就是编译环。这个包
// 不 import 本仓库的任何东西，谁都能读。
//
// # 为什么 spec 的注入点在这里而不是 main
//
// 2026-10-29 之前是 `-X main.spec`：main 读它、交给 manifest.Loader。模块从此读不到
// 「我是哪份规格书」，只能从版本号前缀里猜 distribution——而那正是产物名的一段，
// 猜错了就下错产物。现在只有一处注入、两边都读它（main 也从这里取）。
package distinfo

// 由 -ldflags -X 注入（见 build/build.sh）。空值 = `go run` / `go test`。
var (
	spec   string
	source string
)

// Info 是一份规格书的身份。
type Info struct {
	// Spec 是规格书文件名（dist.json / dist-dev.json）。
	Spec string
	// Distribution 是规格书里的 distribution：产物名与版本号的前缀。
	Distribution string
	// GitHub 是发布仓库（`owner/repo`），空 = 这份规格书没声明发在哪。
	GitHub string
}

// Spec 是本次构建选的规格书文件名；没注入时是 DefaultSpec。
func Spec() string {
	if spec == "" {
		return DefaultSpec
	}
	return spec
}

// Current 是本二进制那份规格书的身份。规格书名不在表里（不该发生：build.sh
// 只会注入存在的文件名）时只带回文件名，其余为空——调用方据此报「不知道」，
// 而不是猜一个产物名去下。
func Current() Info {
	if i, ok := specs[Spec()]; ok {
		return i
	}
	return Info{Spec: Spec()}
}

// Lookup 按文件名查一份规格书的身份。
func Lookup(name string) (Info, bool) {
	i, ok := specs[name]
	return i, ok
}

// Source 是编这个二进制的源码树（仓库根的绝对路径）；没注入时为空。
//
// 它只是一条**线索**：CI 上编的产物带的是 runner 的路径，拿到别的机器上它不存在；
// 本地编的产物带的是开发者的 checkout。self-update 用它判断「这台机器第一次装的
// 是不是自己编的」，并且一定会先核实那个目录真的在、真的是本仓库。
func Source() string { return source }
