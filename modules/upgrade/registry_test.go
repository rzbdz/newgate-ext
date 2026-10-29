package upgrade

import (
	"context"
	"testing"
)

func action(id string) Action {
	return Action{
		ID:       id,
		Title:    func() string { return id },
		Describe: func() string { return "test" },
		Run:      func(context.Context, Request) (Result, error) { return Done("ok"), nil },
	}
}

func TestRegistryLifecycleAndOrder(t *testing.T) {
	r := newRegistry()
	relB, err := r.Register(action("b"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register(action("a")); err != nil {
		t.Fatal(err)
	}
	got := r.Actions()
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("actions = %v, want sorted a, b", ids(got))
	}
	if err := relB(); err != nil {
		t.Fatal(err)
	}
	got = r.Actions()
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("after release = %v, want just a", ids(got))
	}
}

func TestRegistryRejectsBadActions(t *testing.T) {
	r := newRegistry()
	if _, err := r.Register(action("Bad ID")); err == nil {
		t.Fatal("an unusable id should be rejected")
	}
	if _, err := r.Register(Action{ID: "x"}); err == nil {
		t.Fatal("an action without Run/Title/Describe should be rejected")
	}
	if _, err := r.Register(action("dup")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register(action("dup")); err == nil {
		t.Fatal("a duplicate id should be rejected")
	}
}

func ids(as []Action) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.ID)
	}
	return out
}

func TestParseArgs(t *testing.T) {
	got, err := parseArgs([]string{"@v0.9.0", "binary", "--check", "--force"})
	if err != nil {
		t.Fatal(err)
	}
	if got.only != "binary" || got.req.Version != "v0.9.0" || !got.req.Check || !got.req.Force {
		t.Fatalf("parsed = %+v", got)
	}
	if _, err := parseArgs([]string{"@v1", "@v2"}); err == nil {
		t.Fatal("two versions should be rejected")
	}
	if _, err := parseArgs([]string{"@"}); err == nil {
		t.Fatal("an empty version should be rejected")
	}
	if _, err := parseArgs([]string{"a", "b"}); err == nil {
		t.Fatal("a second action name should be rejected")
	}
}
