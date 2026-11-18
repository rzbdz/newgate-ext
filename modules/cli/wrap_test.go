package cli

import (
	"strings"
	"testing"
)

func TestWrapWordsKeepsTokensWhole(t *testing.T) {
	lines := wrapWords("界面读的是哪份消息目录；lang <tag> 切换它", 32)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "<tag>") {
		t.Fatalf("token was split:\n%s", joined)
	}
	for _, line := range lines {
		if strings.Contains(line, "<ta") && !strings.Contains(line, "<tag>") {
			t.Fatalf("line ends with a split token: %q", line)
		}
	}
}

func TestWrapWordsBreaksLongCJK(t *testing.T) {
	lines := wrapWords("一二三四五六", 4)
	if len(lines) != 3 {
		t.Fatalf("lines = %d (%q), want 3", len(lines), lines)
	}
	for _, line := range lines {
		if len([]rune(line)) != 2 {
			t.Fatalf("line %q is not two runes", line)
		}
	}
}

func TestWrapWordsEnglish(t *testing.T) {
	got := strings.Join(wrapWords("one two three", 9), "|")
	if got != "one two|three" {
		t.Fatalf("wrapWords = %q", got)
	}
}
