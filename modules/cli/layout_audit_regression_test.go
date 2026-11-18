package cli

import (
	"os"
	"strings"
	"sync"
	"testing"
)

// TestLayoutAuditLeavesRawOutputAlone 锁住「谁不排版谁自己说」这条：界面不再
// 维护一张命令名名单（见 cliapi.Unstyled），所以这里对着**真账本**验。
func TestLayoutAuditLeavesRawOutputAlone(t *testing.T) {
	t.Setenv("NEWGATE_LAYOUT_AUDIT", "1")

	var s service
	// tui 是模块注入的命令：它自己声明 Unstyled，界面不必认识它。
	if err := s.registerOwnCommands(); err != nil {
		t.Fatal(err)
	}
	if shouldAuditLayout(&s, []string{"alllogs"}) {
		t.Fatal("alllogs 是原始转储，不该审计版式")
	}
	if !shouldAuditLayout(&s, []string{"doctor"}) {
		t.Fatal("doctor 的输出是版式，必须审计")
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
