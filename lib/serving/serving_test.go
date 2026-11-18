package serving

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// This edge's entire behavior is "when it runs" and "what happens when it breaks". Both must
// be pinned: running early means every CLI command opens a listener (exactly why it exists);
// breaking drags others down means one UI failing to start takes the data plane with it.

func TestRegistrationDoesNotStartAnything(t *testing.T) {
	r := NewRegistry()
	var started atomic.Int64
	if _, err := r.OnServe("web", func() (func(), error) {
		started.Add(1)
		return func() {}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := started.Load(); n != 0 {
		t.Fatalf("registration started serving %d times -- registering during Start, and Start runs once per command", n)
	}
	stop, err := r.Notify()
	if err != nil {
		t.Fatal(err)
	}
	if n := started.Load(); n != 1 {
		t.Fatalf("must run once on notify, actually %d", n)
	}
	stop()
}

func TestNotifyIsIdempotent(t *testing.T) {
	r := NewRegistry()
	var started atomic.Int64
	if _, err := r.OnServe("web", func() (func(), error) {
		started.Add(1)
		return func() {}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Notify(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Notify(); err != nil {
		t.Fatal(err)
	}
	if n := started.Load(); n != 1 {
		t.Errorf("notified twice in the same process but the listener must start only once (the second time would rebind the port): %d", n)
	}
}

// TestOneFailureDoesNotStopTheOthers: fail-open. One UI failing to
// start (port taken, bad permissions) must not put the whole serving
// process into a failed state -- the data plane keeps forwarding.
func TestOneFailureDoesNotStopTheOthers(t *testing.T) {
	r := NewRegistry()
	var okRan atomic.Int64
	if _, err := r.OnServe("broken", func() (func(), error) {
		return nil, errors.New("bind: address already in use")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.OnServe("web", func() (func(), error) {
		okRan.Add(1)
		return func() {}, nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Notify()
	if err == nil {
		t.Fatal("a failure must be reported (the caller writes the log), it just must not block others")
	}
	if !strings.Contains(err.Error(), "broken") || !strings.Contains(err.Error(), "address already in use") {
		t.Errorf("the error must name who and why: %v", err)
	}
	if okRan.Load() != 1 {
		t.Errorf("an earlier failure must not block later listeners: %d", okRan.Load())
	}
}

// TestStopIsReverseOrderAndIdempotent: stopping goes in **reverse** registration order
// (later started, earlier stopped, the same rule as the component graph's lifecycle),
// and a repeated call takes effect only once (the exit path may reach it twice).
func TestStopIsReverseOrderAndIdempotent(t *testing.T) {
	r := NewRegistry()
	var order []string
	for _, name := range []string{"first", "second"} {
		n := name
		if _, err := r.OnServe(n, func() (func(), error) {
			return func() { order = append(order, n) }, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	stop, err := r.Notify()
	if err != nil {
		t.Fatal(err)
	}
	stop()
	stop()
	if got := strings.Join(order, ","); got != "second,first" {
		t.Errorf("shutdown order is wrong (must be reverse and only once): %q", got)
	}
}

func TestReleaseOnlyRemovesItsOwn(t *testing.T) {
	r := NewRegistry()
	var ran atomic.Int64
	rel, err := r.OnServe("web", func() (func(), error) {
		ran.Add(1)
		return func() {}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rel(); err != nil {
		t.Fatal(err)
	}
	if err := rel(); err != nil {
		t.Fatalf("revoke must be idempotent: %v", err)
	}
	if _, err := r.Notify(); err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 0 {
		t.Error("must not still be notified after revoke")
	}
}

func TestRejectsNamelessOrNil(t *testing.T) {
	r := NewRegistry()
	if _, err := r.OnServe("", func() (func(), error) { return nil, nil }); err == nil {
		t.Error("a listener without a name must be rejected (the error must name who)")
	}
	if _, err := r.OnServe("x", nil); err == nil {
		t.Error("a nil callback must be rejected")
	}
}
