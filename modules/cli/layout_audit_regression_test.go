package cli

import (
	"os"
	"strings"
	"sync"
	"testing"
)

// TestLayoutAuditLeavesRawOutputAlone pins "whoever is not laid out says
// so themselves": the UI no longer maintains a list of command names
// (see cliapi.Unstyled), so this is checked against the **real ledger**.
func TestLayoutAuditLeavesRawOutputAlone(t *testing.T) {
	t.Setenv("NEWGATE_LAYOUT_AUDIT", "1")

	var s service
	// tui is a command injected by a module: it declares Unstyled itself, and the UI need not know it.
	if err := s.registerOwnCommands(); err != nil {
		t.Fatal(err)
	}
	if shouldAuditLayout(&s, []string{"alllogs"}) {
		t.Fatal("alllogs is a raw dump, its layout should not be audited")
	}
	if !shouldAuditLayout(&s, []string{"doctor"}) {
		t.Fatal("the output of doctor is layout, it must be audited")
	}
}

func TestAuditStreamDrainsArbitrarilyLongLine(t *testing.T) {
	srcR, srcW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.CreateTemp(t.TempDir(), "layout-audit-*")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan []widthViolation, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go auditStream(&wg, srcR, dst, "stdout", results)

	line := strings.Repeat("x", 1024*1024+1) + "\n"
	if _, err := srcW.WriteString(line); err != nil {
		t.Fatal(err)
	}
	_ = srcW.Close()
	wg.Wait()
	if _, err := dst.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	_ = dst.Close()

	if string(copied) != line {
		t.Fatalf("copied %d bytes, want %d", len(copied), len(line))
	}
	if violations := <-results; len(violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(violations))
	}
}
