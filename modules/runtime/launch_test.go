package runtime

import (
	"strings"
	"testing"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/runtime/agentstate"
	"github.com/rzbdz/newgate/lib/i18n"
)

func TestSplitLaunch(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		agent       string
		profile     string
		passthrough string
		wantErr     bool
		errID       string
	}{
		{"bare agent", []string{"claude"}, "claude", "", "", false, ""},
		{"profile after agent, eq form", []string{"claude", "--profile=ds"}, "claude", "ds", "", false, ""},
		{"profile after agent, space form", []string{"claude", "--profile", "ds"}, "claude", "ds", "", false, ""},
		{"profile before agent", []string{"--profile", "ds", "claude"}, "claude", "ds", "", false, ""},
		{"run explicit", []string{"run", "claude", "--profile=ds"}, "claude", "ds", "", false, ""},
		{"passthrough flags", []string{"claude", "--resume", "abc"}, "claude", "", "--resume|abc", false, ""},
		{"passthrough after profile", []string{"claude", "--profile", "ds", "-p", "hi"}, "claude", "ds", "-p|hi", false, ""},
		{"preset alias", []string{"--preset", "toy", "claude"}, "claude", "toy", "", false, ""},
		{"unknown flag before agent", []string{"--bogus", "claude"}, "", "", "", true,
			"Unknown option {option} (an unknown option before the tool counts as a typo, docs/08-operations.md rule 5)"},
		{"no agent", []string{"--profile", "ds"}, "", "", "", true,
			"Which agent to launch? e.g. newgate claude or newgate run claude"},
		{"unknown agent", []string{"nope", "--profile=ds"}, "", "", "", true,
			"Unknown agent {agent} (known: {list})"},
		{"conflicting profile", []string{"claude", "--profile=ds", "--preset=glm"}, "", "", "", true,
			"Both profile {first} and {second} were given, and they disagree"},
		{"profile missing value", []string{"claude", "--profile"}, "", "", "", true,
			"{option} needs a profile name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			agent, profile, passthrough, err := splitLaunch(agentstate.Catalog(), c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected an error, yet it succeeded (agent=%q profile=%q)", agent, profile)
				}
				// Assert **message identity** (language-free): when the language or wording changes,
				// it is the wording change that should go red here, not "the translation lagged".
				if c.errID != "" {
					if got := i18n.ID(err); got != c.errID {
						t.Fatalf("error identity = %q, should be %q (renders as %q)", got, c.errID, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("must not error: %v", err)
			}
			if agent != c.agent {
				t.Errorf("agent = %q, should be %q", agent, c.agent)
			}
			if profile != c.profile {
				t.Errorf("profile = %q, should be %q", profile, c.profile)
			}
			got := strings.Join(passthrough, "|")
			if got != c.passthrough {
				t.Errorf("passthrough = %q, should be %q", got, c.passthrough)
			}
		})
	}
}

// TestLaunchFamilyClaimsExactlyOneHelpLine: the wrapped-launch family takes one help line.
//
// "`run <agent> [args...]` appears once" **used to come from instance count**: before
// 2026-09-18 each agent had a launchCommand, returning empty Usage per agentID to claim
// no line. That required knowing all agents at registration (see launch.go: that path
// lost `newgate claude` once cli moved to the front). Now one command, one help line, and
// this test pins the **verb table**: looked up now, no agent names in help.
//
// Guarded here, not the UI: **this family contributed the extra line, not a dedup miss**.
// The UI follows "empty Usage = no line"; dedup there would make it know these commands.
func TestLaunchFamilyClaimsExactlyOneHelpLine(t *testing.T) {
	catalog := testCatalog{
		"claude":   {ID: "claude", Bin: []string{"claude"}},
		"opencode": {ID: "opencode", Bin: []string{"opencode"}},
	}
	cmds := launchCommands(nil, catalog)
	if len(cmds) != 1 {
		t.Fatalf("expected exactly 1 command, got %d", len(cmds))
	}

	var claimed []string
	for _, c := range cmds {
		doc, ok := c.(interface{ Help() cliapi.HelpLine })
		if !ok {
			t.Fatal("launchCommand must implement Documented")
		}
		line := doc.Help()
		if line.Usage == "" {
			continue // 按约定不占行
		}
		claimed = append(claimed, line.Usage)
		if line.Usage == "run <agent> [args…]" && line.Section != cliapi.SectionRunOnce {
			t.Errorf("the slot of the `run` line = %q, should be %q", line.Section, cliapi.SectionRunOnce)
		}
	}
	if len(claimed) != 1 {
		t.Fatalf("this family should have only one help line, got %d lines: %v", len(claimed), claimed)
	}
	if claimed[0] != "run <agent> [args…]" {
		t.Fatalf("that line should be `run <agent> [args...]`, got %q", claimed[0])
	}

	// The verb table **looks up the catalog**: agents grown after assembly are dispatchable at once.
	names := cmds[0].Names()
	if got := strings.Join(names, ","); got != "claude,opencode,run" {
		t.Fatalf("Names() = %q, should be `claude,opencode,run`", got)
	}
	catalog["glm"] = &agentapi.Agent{ID: "glm", Bin: []string{"glm"}}
	if got := strings.Join(cmds[0].Names(), ","); got != "claude,glm,opencode,run" {
		t.Fatalf("after registering a new agent Names() = %q - the verb table is a snapshot, not a live lookup", got)
	}

	// **No agent names in help**: those are client modules' keys, and listing them makes the
	// UI know the clients once more.
	for _, name := range []string{"claude", "opencode", "glm"} {
		if strings.Contains(claimed[0], name) {
			t.Errorf("the help line %q contains the agent name %q", claimed[0], name)
		}
	}
}
