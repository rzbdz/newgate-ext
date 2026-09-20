package archdiagram

import (
	"fmt"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/store"
)

// `newgate arch`：告诉用户这张图在哪儿。
//
// 与 `newgate web` 同一条理由：地址是端口与前缀相乘出来的，两个都不在用户脑子里，
// 而猜错的症状是「打不开」——看不出是端口不对还是路径不对。
//
// 它顺带说清**这次扫得到扫不到 import**：那张图有一半要源码树（见 handler.go）。
// 不说的话，部署出去的机器上打开一看只有装配图，用户会以为图坏了。
type archCommand struct{ svc *service }

var (
	_ cliapi.Command    = (*archCommand)(nil)
	_ cliapi.Documented = (*archCommand)(nil)
)

func (archCommand) Names() []string { return []string{"arch", "architecture"} }

func (archCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionUI,
		Usage:   i18n.T("arch", nil),
		Summary: i18n.T("where the architecture diagram is served (dev builds)", nil),
	}
}

func (c archCommand) Run(host cliapi.Host, _ []string) int {
	port := domain.ProxyPort
	if st := store.LoadState(); st != nil && st.Port != 0 {
		port = st.Port
	}
	fmt.Println(style.Title("newgate arch", "127.0.0.1"))
	fmt.Println(style.Rule(72))
	fmt.Println(style.Item(style.OK, fmt.Sprintf("http://127.0.0.1:%d%s/", port, Prefix)))
	if root := c.svc.root; root == "" {
		fmt.Println(style.Hint(i18n.T("no source tree found here, so only the assembly graphs will be drawn — "+
			"the import scan needs one (set {env} to point at it)", i18n.A{"env": SrcEnv})))
	} else {
		fmt.Println(style.Hint(i18n.T("imports are scanned from {root}", i18n.A{"root": root})))
	}
	if !host.DaemonRunning() {
		fmt.Println(style.Hint(i18n.T("the daemon is not running — this diagram is served by it, "+
			"so start it first: newgate start", nil)))
	}
	return 0
}
