package webdashboard

import (
	"fmt"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/store"
)

// `newgate web`：告诉用户这个界面在哪儿。
//
// 为什么要一条命令：它的地址是**两个事实相乘**——端口（守护进程起的那个）与前缀
// （porthub 上挂的那个）。两样都不在用户的脑子里，而猜错的症状是「打不开」，看
// 不出是端口不对还是路径不对。这条命令把乘积算出来，顺手说清「现在这个进程里它
// 到底挂上了没有」——那两件事不一样，见下面。
//
// 它只打印，不起浏览器：v1 不替用户决定用哪个浏览器、也不去 xdg-open 一台可能
// 没有图形界面的机器（跳板机上那种调用会挂住）。要开复制那一行就行。
type webCommand struct{ self *instance }

var (
	_ cliapi.Command    = (*webCommand)(nil)
	_ cliapi.Documented = (*webCommand)(nil)
)

func (c *webCommand) Names() []string { return []string{"web", "ui"} }

func (c *webCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionUI,
		Usage:   i18n.T("web", nil),
		Summary: i18n.T("where the browser interface is served, and how to reach it", nil),
	}
}

func (c *webCommand) Run(host cliapi.Host, _ []string) int {
	// 「这个构建里没有共享端口」与「守护进程没在跑」是**两件事**，说清楚哪一件，
	// 用户才知道下一步该做什么（换一份产物 / 起服务）。
	if !c.self.mounted {
		return host.Die(69, i18n.T("this build cannot serve a web interface: nothing provides a shared port "+
			"(the porthub module is not installed)", nil))
	}

	// 端口来自 state.json——守护进程起监听时写下的那个。它是 CLI 侧唯一知道的
	// 端口真相（`newgate status` 报的也是它）。
	port := domain.ProxyPort
	if st := store.LoadState(); st != nil && st.Port != 0 {
		port = st.Port
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s/", port, Prefix)

	fmt.Println(style.Title("newgate web", "127.0.0.1"))
	fmt.Println(style.Rule(72))
	fmt.Println(style.Item(style.OK, url))
	if !host.DaemonRunning() {
		// 界面是守护进程发出来的：CLI 进程里那个挂载只是账本上的一行，没有
		// socket 在听。不说这一句，用户会以为命令本身就是把服务起起来。
		fmt.Println(style.Hint(i18n.T("the daemon is not running — the dashboard is served by it, "+
			"so start it first: newgate start", nil)))
	}
	fmt.Println(style.Hint(i18n.T("the interface is on loopback only; nothing listens on other interfaces", nil)))
	return 0
}
