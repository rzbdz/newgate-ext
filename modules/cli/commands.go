package cli

import (
	"fmt"

	"github.com/rzbdz/newgate/lib/i18n"
)

// This file also makes the UI's **own** batch of
// commands into Commands, registered in the same ledger.
//
// Why (2026-09-18): these commands still live in the UI (they need to read config,
// takeover state, and daemon lifecycle, and those dependencies have not moved out
// of the UI yet), but their **declaration** need not be tied to the UI. As Commands:
//
// - the giant switch in `run()` is gone, leaving only orchestration (argv0
//   dispatch, wrapped launch, ledger lookup, unknown-command fallback);
// - `--help` fills in automatically -- each command declares its own
//   HelpLine, and there is no longer a separate list to maintain in usageText
//   (the two tables got out of sync once: the command moved and help stayed);
// - every command became a unit that **can be moved as a whole**: the day its dependencies
//   also leave the UI, move this type along with its cmdXxx into the module that owns it.
//
// The test for keeping them here is in CLAUDE.md §4: delete it, does the business module
// still stand? If it does, it should not stay in the UI -- conversely, these commands
// stay in the UI **right now** because what they depend on is still in the UI's hands.

// Help line positions. The numbers leave gaps so others
// can jump in; within a section sorted by Rank then Usage.
const (
	rankTakeover = 10 // 接管
	rankRunOnce  = 20 // 跑一次
	rankRouting  = 30 // 路由与配置
	rankObserve  = 40 // 探测与观测
	rankMaint    = 50 // 维护
	rankSystem   = 60 // 界面自身（help / version）
)

type statusCommand struct{ s *service }

func (statusCommand) Names() []string { return []string{"status"} }
func (statusCommand) Help() HelpLine {
	return HelpLine{Section: SectionTakeover, Rank: rankTakeover,
		Usage: "status", Summary: i18n.T("who is going through newgate, and which profile each client uses", nil)}
}
func (c statusCommand) Run(_ Host, _ []string) int { return cmdStatus(c.s) }

// ---------- Probing and observation ----------

type doctorCommand struct{ s *service }

func (doctorCommand) Names() []string { return []string{"doctor"} }
func (doctorCommand) Help() HelpLine {
	return HelpLine{Section: SectionObserve, Rank: rankObserve,
		Usage: "doctor", Summary: i18n.T("health check", nil)}
}
func (c doctorCommand) Run(_ Host, _ []string) int { return cmdDoctor(c.s) }

type allLogsCommand struct{ s *service }

func (allLogsCommand) Names() []string { return []string{"alllogs", "all-logs"} }

// Unstyled: the diagnostic bundle is a **raw dump** (raw log
// text + raw config text), not a layout for human scanning.
func (allLogsCommand) Unstyled([]string) bool { return true }
func (allLogsCommand) Help() HelpLine {
	return HelpLine{Section: SectionObserve, Rank: rankObserve,
		Usage: "alllogs", Summary: i18n.T("full diagnostic bundle", nil)}
}
func (c allLogsCommand) Run(_ Host, _ []string) int { return cmdAllLogs(c.s) }

// ---------- The UI itself ----------

type versionCommand struct{}

func (versionCommand) Names() []string {
	return []string{"version", "--version", "-v"}
}
func (versionCommand) Help() HelpLine {
	return HelpLine{Section: SectionUI, Rank: rankSystem,
		Usage: "version", Summary: ""}
}
func (versionCommand) Run(_ Host, _ []string) int {
	fmt.Println(VersionLine())
	return 0
}

type helpCommand struct{ s *service }

func (helpCommand) Names() []string { return []string{"help", "--help", "-h"} }
func (helpCommand) Help() HelpLine {
	return HelpLine{Section: SectionUI, Rank: rankSystem,
		Usage: "help", Summary: i18n.T("this screen", nil)}
}
func (c helpCommand) Run(_ Host, _ []string) int {
	fmt.Print(usageText(c.s))
	return 0
}

// ownCommands are the UI's own commands.
//
// Registered into the same ledger (rather than left in run()'s switch): duplicate
// checking, dispatch, and help assembly take the same path, so run() is left with
// only orchestration. The day some command's dependencies leave the UI, delete it from
// here and move it to the module that owns it -- see the note at the top of the file.
func ownCommands(s *service) []Command {
	return []Command{
		statusCommand{s},
		doctorCommand{s},
		allLogsCommand{s},
		versionCommand{}, helpCommand{s},
	}
}

// registerOwnCommands hangs the UI's own commands in the ledger. A
// name clash is an assembly-time error -- two modules claiming the same
// command name must error on the spot, not first-come-first- served.
func (s *service) registerOwnCommands() error {
	for _, c := range ownCommands(s) {
		if _, err := s.RegisterCommand(c); err != nil {
			return err
		}
	}
	return nil
}
