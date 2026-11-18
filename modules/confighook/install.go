package confighook

import (
	"io"
	"os"
	"os/exec"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// AgentInstaller 是一个客户端模块「怎么把自家工具装上」的知识。
//
// # 为什么装法归客户端模块
//
// 装哪个包、用什么包管理器、装完二进制叫什么——全是**那一家客户端的事**，而且会变
// （官方换包名、从 npm 搬到别的渠道）。写进内核就是内核在认识 npm 与那家公司的包名；
// 写死在一个「安装器」模块里，则是让那个模块认识所有客户端。
//
// 内核这一侧只提供**机制**：一个端口（贡献者注册进来、拿得到 Release）、一句问句
// （这台机器上有没有）、以及一条命令行的路（`newgate <agent> -y`）。
//
// # best-effort 的语气
//
// 装不上不是故障：用户可能没有 npm、没有 sudo、或者公司网络不通。所以失败一律
// 原样报出来（命令、退出码、输出）、绝不假装成功——「没装成但看着像装成了」会让人
// 在一个不存在的工具上排查半天。
type AgentInstaller interface {
	// Command 是这个工具的安装命令（argv，含 argv[0]）。
	//
	// 它是**给人看的**：提示里写「是否用 `npm i -g @openai/codex` 安装？」比写
	// 「是否安装」有用得多——用户要能判断自己愿不愿意跑这条命令。
	Command() []string
	// Install 跑它。out 收命令的输出（best-effort 的「尽力」要看得见）。
	Install(out io.Writer) error
}

// ExecInstaller 是一个现成的 AgentInstaller：跑一条外部命令。
//
// 为什么放在内核而不是让每个客户端模块各写一遍 exec：**怎么跑一条外部命令**与
// 「跑哪条命令」是两件事，前者每个模块都一模一样（继承环境、接上输出、把退出码变成
// 错误），后者才是各家的知识。抄 N 遍的话，改一处（比如将来要加超时）就得改 N 处。
func ExecInstaller(argv ...string) AgentInstaller {
	return execInstaller(argv)
}

type execInstaller []string

func (e execInstaller) Command() []string { return append([]string(nil), e...) }

func (e execInstaller) Install(out io.Writer) error {
	if len(e) == 0 {
		return i18n.E("no install command was declared", nil)
	}
	cmd := exec.Command(e[0], e[1:]...)
	// 环境原样继承：包管理器要找 node/npm、要读 HOME 与代理设置——替它造一个干净
	// 环境只会让「我明明装了」变成一次无从解释的失败。
	cmd.Env = os.Environ()
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return i18n.Ef(err, "the install command failed ({cmd}): {err}",
			i18n.A{"cmd": joinArgv(e), "err": err})
	}
	return nil
}

// joinArgv 把 argv 拼成一行给人看（只有空格分隔，不做 shell 转义——它是提示文本，
// 不是给 shell 执行的东西）。
func joinArgv(argv []string) string {
	out := ""
	for i, a := range argv {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
