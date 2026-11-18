package porthub

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func body(s string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(s)) })
}

// hit runs a real dispatch: look up the table -> on
// a hit, hand over. Returns the body ("" on miss).
func hit(t *testing.T, r *Registry, path string) string {
	t.Helper()
	h, ok := r.Lookup(path)
	if !ok {
		return ""
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Body.String()
}

func TestLookupMatchesPrefixOnSegmentBoundary(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Mount("/ui", "web", body("ui")); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"/ui":        "ui",
		"/ui/":       "ui",
		"/ui/api/x":  "ui",
		"/ui/deep/x": "ui",
		"/uix":       "", // byte-prefix matching would wrongly claim a service here
		"/u":         "",
		"/":          "",
		"/v1/models": "",
	}
	for path, want := range cases {
		if got := hit(t, r, path); got != want {
			t.Errorf("Lookup(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestLongestPrefixWins: with both `/ui` and `/ui/api` mounted, the latter catches deeper paths.
func TestLongestPrefixWins(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Mount("/ui", "outer", body("outer")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Mount("/ui/api", "inner", body("inner")); err != nil {
		t.Fatal(err)
	}
	if got := hit(t, r, "/ui/api/snapshot"); got != "inner" {
		t.Errorf("/ui/api/snapshot has owner %q, want inner", got)
	}
	if got := hit(t, r, "/ui/page"); got != "outer" {
		t.Errorf("/ui/page has owner %q, want outer", got)
	}
}

// TestReservedPrefixesAreRefused: the symptom of a reserved prefix being taken is
// "a request meant for this machine gets forwarded upstream as data-plane traffic",
// so it must error out on the spot instead of letting a later caller override.
func TestReservedPrefixesAreRefused(t *testing.T) {
	r := NewRegistry()
	for _, p := range []string{"/v1", "/v1/models", "/a", "/a/claude", "/__newgate", "/__newgate/config"} {
		_, err := r.Mount(p, "sneaky", body("x"))
		if err == nil {
			t.Errorf("mounting %q must be rejected", p)
			continue
		}
		// Assert the English original (the source language, stable in CI), not the
		// translation -- translations change, and the test should not change with them.
		if !strings.Contains(err.Error(), "reserved prefix") {
			t.Errorf("the error from mounting %q must explain why, got: %v", p, err)
		}
	}
}

func TestDuplicatePrefixIsRefused(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Mount("/ui", "web", body("a")); err != nil {
		t.Fatal(err)
	}
	_, err := r.Mount("/ui", "other", body("b"))
	if err == nil {
		t.Fatal("mounting the same prefix twice must error -- otherwise only one of the two services receives requests, and silently")
	}
	if !strings.Contains(err.Error(), "web") {
		t.Errorf("the error must name the one mounted first (who holds it), got: %v", err)
	}
}

func TestReleaseRemovesTheMount(t *testing.T) {
	r := NewRegistry()
	rel, err := r.Mount("/ui", "web", body("ui"))
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if got := hit(t, r, "/ui"); got != "" {
		t.Errorf("must not match after Release, actually %q", got)
	}
	rel() // idempotent
	// After release the same prefix can be mounted by someone else.
	if _, err := r.Mount("/ui", "other", body("other")); err != nil {
		t.Fatalf("must remount after release: %v", err)
	}
	if got := hit(t, r, "/ui"); got != "other" {
		t.Errorf("after remount owner is %q, want other", got)
	}
}

// TestReleaseDoesNotStealSomeoneElsesMount: when the earlier
// mount is released, it must not delete the later one.
func TestReleaseDoesNotStealSomeoneElsesMount(t *testing.T) {
	r := NewRegistry()
	first, err := r.Mount("/ui", "first", body("first"))
	if err != nil {
		t.Fatal(err)
	}
	first() // release itself
	if _, err := r.Mount("/ui", "second", body("second")); err != nil {
		t.Fatal(err)
	}
	first() // release again (idempotent, and must not delete second's)
	if got := hit(t, r, "/ui"); got != "second" {
		t.Errorf("the latecomer's mount was deleted by mistake: %q", got)
	}
}

func TestRejectsMalformedPrefixAndNilHandler(t *testing.T) {
	r := NewRegistry()
	for _, p := range []string{"", "ui", "/"} {
		if _, err := r.Mount(p, "web", body("x")); err == nil {
			t.Errorf("prefix %q must be rejected", p)
		}
	}
	if _, err := r.Mount("/ui", "web", nil); err == nil {
		t.Error("a nil handler must be rejected")
	}
}

func TestMountsSnapshotIsSorted(t *testing.T) {
	r := NewRegistry()
	for _, p := range []string{"/svc-z", "/svc-a", "/svc-m"} {
		if _, err := r.Mount(p, "owner-"+p, body(p)); err != nil {
			t.Fatal(err)
		}
	}
	got := r.Mounts()
	want := []string{"/svc-a", "/svc-m", "/svc-z"}
	for i, m := range got {
		if m.Prefix != want[i] {
			t.Fatalf("Mounts()[%d] = %q, want %q (diagnostics output must be stable, sorted by prefix)", i, m.Prefix, want[i])
		}
	}
}

// TestRootIsThePortsDispatch pins "who owns the port": the handler
// Root hands out is that port's dispatch -- mounted services win,
// and anything unclaimed falls back to the fallback (the data plane).
func TestRootIsThePortsDispatch(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Mount("/ui", "web", body("web")); err != nil {
		t.Fatal(err)
	}
	root, err := r.Root("datapath", body("datapath"))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"/ui/x":        "web",
		"/v1/messages": "datapath", // nothing mounted: fallback
		"/":            "datapath",
	}
	for path, want := range cases {
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Body.String(); got != want {
			t.Errorf("root(%q) = %q, want %q", path, got, want)
		}
	}
	if got := r.FallbackOwner(); got != "datapath" {
		t.Errorf("FallbackOwner() = %q, want datapath", got)
	}
}

// TestRootRejectsNilFallback: a nil fallback errors out **at install time**, rather
// than blowing up on the hot path when a request arrives (the scene then is a 500 plus a
// panic stack, and nobody would connect that to one call at startup missing an argument).
func TestRootRejectsNilFallback(t *testing.T) {
	if _, err := NewRegistry().Root("datapath", nil); err == nil {
		t.Error("a nil fallback must be rejected -- an error from mounting a nil handler shows up on every request")
	}
}

// TestMountsIncludeTheFallback: diagnostic output must fully answer "what
// all is running on this port", so the fallback shows up in the snapshot as
// a `/` entry (it catches exactly all paths not claimed by a longer prefix).
func TestMountsIncludeTheFallback(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Mount("/ui", "web", body("web")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Root("datapath", body("dp")); err != nil {
		t.Fatal(err)
	}
	got := r.Mounts()
	if len(got) != 2 || got[0].Prefix != "/" || got[0].Owner != "datapath" || got[1].Prefix != "/ui" {
		t.Fatalf("Mounts() = %+v, want [/ -> datapath, /ui -> web]", got)
	}
}

// TestConcurrentMountAndLookup is the race protection point for "two
// frontends run at once": dispatch looks the table up on every request, while
// mounts/releases happen in a module's Start/Stop. Only meaningful under -race.
func TestConcurrentMountAndLookup(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		prefix := fmt.Sprintf("/svc%d", i)
		go func() {
			defer wg.Done()
			rel, err := r.Mount(prefix, "owner", body(prefix))
			if err != nil {
				t.Errorf("Mount(%s): %v", prefix, err)
				return
			}
			rel()
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = r.Lookup(prefix + "/x")
			}
		}()
	}
	wg.Wait()
}
