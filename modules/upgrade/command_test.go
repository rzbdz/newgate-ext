package upgrade

import (
	"bufio"
	"context"
	"strings"
	"testing"
)

// fakeAction 记下自己被怎么调用过：只看了几次、真跑了几次。
type fakeAction struct {
	id       string
	kind     Kind
	upToDate bool
	looks    int
	runs     int
}

func (f *fakeAction) action() Action {
	return Action{
		ID:       f.id,
		Kind:     f.kind,
		Title:    func() string { return f.id },
		Describe: func() string { return "test" },
		Run: func(_ context.Context, req Request) (Result, error) {
			if f.upToDate {
				return Current("already current"), nil
			}
			if req.Check {
				f.looks++
				return Done("would do it"), nil
			}
			f.runs++
			return Done("did it"), nil
		},
	}
}

func newSession(input string, interactive bool) (*session, *strings.Builder) {
	var out strings.Builder
	return &session{
		ctx:         context.Background(),
		out:         &out,
		in:          bufio.NewReader(strings.NewReader(input)),
		interactive: interactive,
	}, &out
}

// 缺省：先看、再**分两次**问（二进制一次、数据一次），只做点了头的那一半。
func TestUpgradeAsksTwiceAndDoesOnlyWhatWasConfirmed(t *testing.T) {
	bin := &fakeAction{id: "binary", kind: KindProgram}
	data1 := &fakeAction{id: "data-a"}
	data2 := &fakeAction{id: "data-b"}
	s, out := newSession("n\ny\n", true)
	code := s.run([]Action{bin.action(), data1.action(), data2.action()}, upgradeArgs{})

	if bin.looks != 1 || bin.runs != 0 {
		t.Fatalf("binary looked %d ran %d; answered n, so look once and never run", bin.looks, bin.runs)
	}
	if data1.runs != 1 || data2.runs != 1 {
		t.Fatalf("data ran %d/%d; answered y once for both", data1.runs, data2.runs)
	}
	if n := strings.Count(out.String(), "[y/N]"); n != 2 {
		t.Fatalf("asked %d times, want 2 (binary, then the module data)\n%s", n, out.String())
	}
	if !strings.Contains(out.String(), "binary") || strings.Index(out.String(), "Replace the newgate binary") > strings.Index(out.String(), "Refresh these 2") {
		t.Fatalf("the binary question must come first:\n%s", out.String())
	}
	if code != 0 {
		t.Fatalf("exit %d; declining is a choice, not a failure", code)
	}
}

// 已经是最新的不追问：问一句「要升级吗」然后什么都不做，是在浪费确认。
func TestUpToDateIsNotAsked(t *testing.T) {
	bin := &fakeAction{id: "binary", kind: KindProgram, upToDate: true}
	s, out := newSession("", true)
	s.run([]Action{bin.action()}, upgradeArgs{})
	if strings.Contains(out.String(), "[y/N]") {
		t.Fatalf("asked about something that is already current:\n%s", out.String())
	}
}

// 读不到回答（EOF）= 不。
func TestNoAnswerMeansNo(t *testing.T) {
	bin := &fakeAction{id: "binary", kind: KindProgram}
	s, _ := newSession("", true)
	s.run([]Action{bin.action()}, upgradeArgs{})
	if bin.runs != 0 {
		t.Fatal("ran without an answer")
	}
}

// 没有终端又没给 -y：不执行，并以 1 退出（脚本要看得出「其实没升」）。
func TestNoTerminalWithoutYesRefuses(t *testing.T) {
	bin := &fakeAction{id: "binary", kind: KindProgram}
	s, _ := newSession("y\n", false)
	if code := s.run([]Action{bin.action()}, upgradeArgs{}); code != 1 || bin.runs != 0 {
		t.Fatalf("code %d runs %d; want a refusal", code, bin.runs)
	}
}

// -y / -f：不问，直接跑；-f 还带上 Force。
func TestYesAndForceSkipTheQuestions(t *testing.T) {
	for _, flag := range []string{"-y", "--yes", "-f", "--force"} {
		parsed, err := parseArgs([]string{flag})
		if err != nil {
			t.Fatal(err)
		}
		bin := &fakeAction{id: "binary", kind: KindProgram}
		data := &fakeAction{id: "data"}
		s, out := newSession("", false)
		if code := s.run([]Action{bin.action(), data.action()}, parsed); code != 0 {
			t.Fatalf("%s: exit %d", flag, code)
		}
		if bin.runs != 1 || data.runs != 1 || strings.Contains(out.String(), "[y/N]") {
			t.Fatalf("%s: runs %d/%d, output:\n%s", flag, bin.runs, data.runs, out.String())
		}
		if force := flag == "-f" || flag == "--force"; parsed.req.Force != force {
			t.Fatalf("%s: Force = %v", flag, parsed.req.Force)
		}
	}
}

// --check 不问也不跑。
func TestCheckNeverRuns(t *testing.T) {
	bin := &fakeAction{id: "binary", kind: KindProgram}
	s, out := newSession("y\ny\n", true)
	s.run([]Action{bin.action()}, upgradeArgs{req: Request{Check: true}})
	if bin.runs != 0 || strings.Contains(out.String(), "[y/N]") {
		t.Fatalf("--check ran or asked:\n%s", out.String())
	}
}

func TestSelectActionsWithVersion(t *testing.T) {
	bin := Action{ID: "binary", Versioned: true}
	data := Action{ID: "data"}
	got, skipped, err := selectActions([]Action{bin, data}, upgradeArgs{req: Request{Version: "v1"}})
	if err != nil || len(got) != 1 || got[0].ID != "binary" || len(skipped) != 1 {
		t.Fatalf("got %v skipped %v err %v", ids(got), skipped, err)
	}
	if _, _, err := selectActions([]Action{bin, data}, upgradeArgs{only: "data", req: Request{Version: "v1"}}); err == nil {
		t.Fatal("pinning a version on an unversioned action must fail")
	}
}
