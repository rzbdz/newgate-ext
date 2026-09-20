package webdashboard

import (
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

// 没装 porthub 时的退路：界面**自己监听一个端口**。
//
// 为什么会走到这条路上：共享端口（porthub）是一个可以被摘掉的模块。摘掉它之后
// 网关自己服务端口，界面上就再也没有落脚的地方——而这个界面本身没有任何理由
// 跟着消失。所以它退回共享端口之前的样子：自己一个端口。
//
// **它只在服务进程里起来**（见 core/lib/serving）：模块的 Start 每一条 `newgate …`
// 命令都会跑，在那里起监听等于敲一次 `newgate status` 就开一台服务器。判据不能
// 靠猜 argv（内核明文禁止模块自己读 os.Args），只能等拥有端口的那位说一句
// 「我要开始服务了」。
//
// 代价说清楚：这条路丢掉的正是当初要共享端口的那个理由——防火墙、反代、ACL 都
// 是按端口配的，多一个端口就要多配一处。所以它是**退路**，不是常态。

// FallbackPort 是这条路默认监听的端口（网关默认 8899 的邻居）。
//
// 用环境变量 NEWGATE_WEB_PORT 覆盖：它只在这条退路上有意义，而配一个只有退路
// 才读的字段进 state.json，等于让所有部署都背着一个用不到的键。
const FallbackPort = 8898

// localhostOnly 是本界面永远绑的地址。
//
// 与共享端口**同一条边界**：那个端口绑的就是 loopback（网关自己的选择），这里
// 不能更宽——它同样能改配置、能拨运行期开关（见 bff.go 里那两道门）。
const localhostOnly = "127.0.0.1"

// standalonePort 是这次退路要用的端口号。`newgate web` 也读它——命令告诉用户
// 「上哪儿找」的那个号，必须与真正绑的那个是同一处算出来的。
func standalonePort() int {
	v := os.Getenv("NEWGATE_WEB_PORT")
	if v == "" {
		return FallbackPort
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 || n > 65535 {
		// 说了不听会让人以为改生效了，进而以为是别的地方坏了。
		log.Printf("[web] %s is not a usable port number, using %d", strconv.Quote(v), FallbackPort)
		return FallbackPort
	}
	return n
}

// standaloneAddr 是这次要绑的地址。
func standaloneAddr() string {
	return net.JoinHostPort(localhostOnly, strconv.Itoa(standalonePort()))
}

// serveStandalone 起监听，返回停机函数。
//
// 它把界面挂在 `/ui` 前缀下——**与共享端口那条路一模一样**。这不是洁癖：前端的
// 资源路径是构建期定的（vite 的 base），换个前缀就白屏，而白屏是最难排查的一种
// 「坏了」。两条路给出同一个 URL 形状，用户和 `newgate web` 都不用记两套。
func serveStandalone(handler http.Handler) (func(), error) {
	addr := standaloneAddr()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle(Prefix+"/", http.StripPrefix(Prefix, handler))
	// 根路径指过去：用户多半会先敲一下 `http://127.0.0.1:8898/`，回一句 404
	// 会让人以为端口不对。
	mux.Handle("/", http.RedirectHandler(Prefix+"/", http.StatusFound))

	srv := &http.Server{
		Handler: mux,
		// 这个界面是本机自己用的，慢客户端唯一可能的来源是刻意为之的连接。
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[web] the interface stopped listening: %v", err)
		}
	}()
	// 写进守护进程的日志：它俩共用同一个 stdout（daemon.Spawn 把日志文件接在
	// 子进程的 stdout 上），所以这一行和网关那些行落在一起，`newgate logs` 看得见。
	log.Printf("[web] no shared port in this build — the interface listens on http://%s%s/",
		addr, Prefix)
	return func() { _ = srv.Close() }, nil
}
