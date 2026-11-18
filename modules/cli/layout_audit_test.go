package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/lib/style"
)

// fakeDoc is a command contributed by a module, used to bring "help lines declared by modules"
// into the width guard too. Real contributors (naked / plugin / omo) are in their own modules, and
// cli's tests cannot import them (that would be a cycle), so one with the same shape is made here.
type fakeDoc struct {
	name string
	line HelpLine
}

func (f fakeDoc) Names() []string        { return []string{f.name} }
func (f fakeDoc) Run(Host, []string) int { return 0 }
func (f fakeDoc) Help() HelpLine         { return f.line }

// usageFixture installs a service with module contributions: one joins an
// existing section, one opens a new section. The names are deliberately long --
// the previous version was burst past the layout by one 19-character module name.
//
// The ledger is on the service, used for real (no hand-written stub): that way --help's
// assembly path is the same code as production, including command-name duplicate checking.
func usageFixture(t *testing.T) *service {
	t.Helper()
	s := &service{}
	// All names are distinct: the ledger checks duplicates by
	// Names (a clash errors on the spot, one of its invariants).
	for i, line := range []HelpLine{
		{Section: SectionMaintenance, Usage: "claudecode-deepseek-thing on|off [duration]", Summary: "a very long module name, used to stretch the left column"},
		{Section: SectionModules, Usage: "plugin [module[.path]] [on|off] [duration]", Summary: "list every module by category; toggle a module or one switch point"},
	} {
		if _, err := s.RegisterCommand(fakeDoc{name: fmt.Sprintf("fake%d", i), line: line}); err != nil {
			t.Fatalf("registration failed: %v", err)
		}
	}
	return s
}

// TestUsageNeverExceedsLayoutWidth guards two things: the CLI's own help does not
// overflow, and **the lines modules contribute do not either**. The latter is the key one
// -- contributed lines are written by others, and the CLI must hold the line at render
// time, otherwise one module writing its Usage a bit longer bursts the whole --help.
func TestUsageNeverExceedsLayoutWidth(t *testing.T) {
	for _, text := range []string{usageText(nil), usageText(usageFixture(t))} {
		for i, line := range strings.Split(text, "\n") {
			if width := style.VisibleWidth(line); width > style.MaxColumns {
				t.Fatalf("usage line %d is %d columns: %q", i+1, width, line)
			}
		}
	}
}

// TestUsageIncludesModuleContributions guards "commands
// moved back into modules, and help moved with them".
//
// The previous version deleted naked / plugin from usageText's hardcoded list but
// rendered module contributions nowhere else, and the result was that two commands **vanished
// completely** from --help -- the command was there, but invisible to the user is as good as
// nonexistent. This test blocks exactly that: a contributed Usage must appear in the final text.
func TestUsageIncludesModuleContributions(t *testing.T) {
	text := usageText(usageFixture(t))
	for _, want := range []string{"claudecode-deepseek-thing", "plugin [module"} {
		if !strings.Contains(text, want) {
			t.Fatalf("--help has no %q:\n%s", want, text)
		}
	}
	// A self-opened section must have its own heading, not be swallowed into the
	// previous section. The heading asserted is the **display name** (the source
	// language is that English sentence): the slot key is the ASCII identity,
	// and copy goes through the catalog -- see extension.Section.Display.
	if !strings.Contains(text, SectionModules.Display()+"\n") {
		t.Fatalf("the section a module opens for itself was not rendered:\n%s", text)
	}
}
