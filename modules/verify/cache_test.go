package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate/testing/testkit"
)

func TestCacheRoundTripAndCrossProcessRead(t *testing.T) {
	testkit.Sandbox(t)
	c := newCacheStore()
	e := Entry{
		Provider: "relay", Model: "model", Predicted: "model", Match: MatchOK,
		Probability: ptr(0.91), CheckedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := c.put(e); err != nil {
		t.Fatal(err)
	}
	// 另一个实例（= 另一个进程）必须能立刻读到：cache 不把文件读进内存就不再回看。
	other := newCacheStore()
	got, ok, err := other.get("relay", "model")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Predicted != "model" || got.Match != MatchOK {
		t.Fatalf("got %+v", got)
	}
	if got.Probability == nil || *got.Probability != 0.91 {
		t.Fatalf("probability = %v", got.Probability)
	}

	// 并集：第二条不该把第一条抹掉。
	if err := other.put(Entry{Provider: "relay", Model: "two", Match: MatchMismatch}); err != nil {
		t.Fatal(err)
	}
	list, err := c.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list = %d entries, want 2", len(list))
	}
	if list[0].Model != "model" || list[1].Model != "two" {
		t.Errorf("list order = %s, %s", list[0].Model, list[1].Model)
	}
}

func TestCacheCorruptIsReported(t *testing.T) {
	testkit.Sandbox(t)
	c := newCacheStore()
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.list(); err == nil {
		t.Fatal("a corrupt cache should be reported, not silently treated as empty")
	}
	if err := c.put(Entry{Provider: "p", Model: "m"}); err == nil {
		t.Fatal("put must not clobber a corrupt file silently")
	}
}

func TestCachePermission(t *testing.T) {
	testkit.Sandbox(t)
	c := newCacheStore()
	if err := c.put(Entry{Provider: "p", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(c.path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o660 {
		t.Errorf("cache mode = %o, want 660 (shared deployment group)", got)
	}
	if strings.HasSuffix(info.Name(), ".tmp") {
		t.Error("the cache was left under its temporary name")
	}
}

func ptr[T any](v T) *T { return &v }
