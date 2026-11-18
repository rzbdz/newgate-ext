package gateway

import (
	"testing"

	entryapi "github.com/rzbdz/newgate/component/entry"
)

// TestServeEntryClaims pins the daemon entry's claim criteria.
//
// Half of this table is "claims", half is **"must not claim"**, and the latter is what
// matters: over-claiming shows up as typing a nonexistent command and starting a daemon (only
// discovered once the port is taken or nothing can connect), while under-claiming shows up as
// a pure dashboard build **never starting** (`no entry claimed this call`, measured
// 2026-09-20).
//
// The headless column is "the assembly has no terminal UI", computed by Start from what is
// installed (see module.go) - it decides what this process is when there is no sub-command.
func TestServeEntryClaims(t *testing.T) {
	cases := []struct {
		name     string
		headless bool
		args     []string
		want     bool
	}{
		{"with a UI: an explicit __serve claims", false, []string{"__serve", "--port", "8899"}, true},
		{"with a UI: no args does not claim (that is for the UI to answer)", false, nil, false},
		{"with a UI: --port alone does not claim", false, []string{"--port", "8899"}, false},
		{"with a UI: subcommands do not claim", false, []string{"status"}, false},

		{"headless: no args claims (this process is the daemon)", true, nil, true},
		{"headless: --port alone claims", true, []string{"--port", "8899"}, true},
		{"headless: --port=N claims", true, []string{"--port=8899"}, true},
		{"headless: an explicit __serve still claims", true, []string{"__serve", "--port", "8899"}, true},

		// The three below are "must not claim": with no UI, typing something else should get
		// the plain-language "no entry claimed this call", not a daemon quietly started.
		{"headless: unknown subcommands do not claim", true, []string{"frobnicate"}, false},
		{"headless: --help does not claim (do not swallow it into starting the service)", true, []string{"--help"}, false},
		{"headless: flags belonging to others do not claim", true, []string{"--json"}, false},
		{"headless: a subcommand among its own flags does not claim", true, []string{"--port", "8899", "status"}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := serveEntry{headless: c.headless}
			if got := e.Claims(entryapi.Process{Argv0: "newgate", Args: c.args}); got != c.want {
				t.Errorf("Claims(%v) headless=%v = %v, want %v", c.args, c.headless, got, c.want)
			}
		})
	}
}

// TestServeEntryNameIsStable: the name goes into the log (the composition root's
// `[entry] resolve: … → <Name>`), and it is the only thing that identifies it in diagnostics,
// so it must not follow the copy.
func TestServeEntryNameIsStable(t *testing.T) {
	if got := (serveEntry{}).Name(); got != "__serve" {
		t.Errorf("the entry name should be __serve (matching the args daemon.Spawn builds), got %q", got)
	}
}
