package confighook

import (
	"io"
	"os"
	"os/exec"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// AgentInstaller is a client module's knowledge of "how to install my tool".
//
// # Why the install method belongs to the client module
//
// Which package, with which package manager, and what the binary is called after -- all
// **that client's business**, and it changes (the official package name changes, moving from
// npm to another channel). Written in the kernel, the kernel would know npm and that company's
// package name; hardcoded in one "installer" module, that module would know every client.
//
// The kernel side provides only **mechanism**: a port (contributors register in, getting a
// Release), a question (is it on this machine), and a command-line path (`newgate <agent> -y`).
//
// # The tone of best-effort
//
// Failing to install is not a fault: the user may have no npm, no sudo, or
// a company network that is down. So a failure is always reported as is (command,
// exit code, output), and never pretended to be a success -- "not installed but
// looks installed" leaves people debugging for hours on a tool that does not exist.
type AgentInstaller interface {
	// Command is this tool's install command (argv, including argv[0]).
	//
	// It is **for humans**: a prompt saying "install with `npm i -g
	// @openai/codex`?" is far more useful than "install?" -- the user
	// must be able to judge whether he is willing to run that command.
	Command() []string
	// Install runs it. out collects the command's output (the "best" of best-effort must be visible).
	Install(out io.Writer) error
}

// ExecInstaller is a ready-made AgentInstaller: run one external command.
//
// Why it is in the kernel rather than each client module writing its own exec:
// **how to run an external command** and "which command to run" are two things; the
// former is identical in every module (inherit the environment, attach the output,
// turn the exit code into an error), and the latter is each provider's knowledge.
// Copied N times, one change (adding a timeout later, say) means changing N places.
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
	// The environment is inherited as is: a package manager needs node/npm, HOME and proxy settings
	// -- building it a clean environment only turns "but I installed it" into an inexplicable failure.
	cmd.Env = os.Environ()
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return i18n.Ef(err, "the install command failed ({cmd}): {err}",
			i18n.A{"cmd": joinArgv(e), "err": err})
	}
	return nil
}

// joinArgv joins argv into one line for humans (space separated only, no
// shell escaping -- it is prompt text, not something to hand to a shell).
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
