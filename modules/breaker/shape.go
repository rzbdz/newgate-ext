package breaker

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	modules "github.com/rzbdz/newgate/component"
)

// ShapeDetector decides "is this 4xx a **request shape** problem".
//
// Why the test is handed out (2026-09-17): the definition of a shape error belongs
// entirely to **the upstream itself**. The property "the same body is equally wrong
// whichever provider gets it" can only be judged by that upstream knowing its validation
// rules -- the two `must be passed back` sentences are DeepSeek's dialect, and hardcoding
// them in the health table would make core carry a string it should not know. And this
// test is a **decision point**: recognizing it means not delisting, and getting it wrong
// either delists by mistake (hurting users) or lets it through (hurting observability).
// It must live with the module that knows the truth and be tested by that module.
//
// core only offers the port and the policy (BucketShape: never delist, count only).
//
// Match must be a **pure function** and must not panic: it is called on every 4xx,
// and the fail-open backstop is done at the registry layer (see shapeRegistry.match).
type ShapeDetector interface {
	// Name goes into logs and metrics, to answer "which rule recognized it".
	Name() string
	// Match decides whether this response is that upstream's request-shape error.
	Match(status int, body []byte) bool
}

// shapeRegistry holds the registered shape detectors.
//
// Why it lives in table rather than a package-level variable: the health table can
// be created many times in tests, and a package-level registry would let them pollute
// each other (whoever registers first wins, and test order affects results). This one
// follows the instance, so registering and unregistering affect only its own copy.
type shapeRegistry struct {
	mu        sync.RWMutex
	detectors []ShapeDetector
	tokens    map[string]uint64
	next      uint64
}

// errShapeDetector covers the two kinds of registration-time
// rejection. Made variables so tests can use errors.Is.
var (
	errShapeDetectorName   = errors.New("breaker: shape detector needs a Name()")
	errShapeDetectorDupFmt = "breaker: duplicate shape detector %s"
)

func errShapeDetectorDuplicate(name string) error {
	return fmt.Errorf(errShapeDetectorDupFmt, name)
}

// Register adds a detector and returns a release handle belonging to this registration only.
//
// Registering a duplicate name errors outright: with two detectors both called
// deepseek active, logs and metrics could no longer tell who recognized it.
func (r *shapeRegistry) Register(d ShapeDetector) (modules.Release, error) {
	if d == nil || d.Name() == "" {
		return nil, errShapeDetectorName
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.detectors {
		if existing.Name() == d.Name() {
			return nil, errShapeDetectorDuplicate(d.Name())
		}
	}
	if r.tokens == nil {
		r.tokens = map[string]uint64{}
	}
	r.next++
	token := r.next
	// The tokens table is **not** for tolerating duplicate registration (that is already
	// rejected above), but to make a **stale Release harmless**: registering the same
	// key again after an unregister gets a new token, and that old Release arriving late
	// would delete the new registration. Comparing tokens blocks exactly that. (It is easy
	// to read this branch as unreachable -- that misses "unregister -> register again".)
	r.tokens[d.Name()] = token
	r.detectors = append(append([]ShapeDetector(nil), r.detectors...), d)
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.tokens[d.Name()] != token {
			return nil // 已经被后来的注册顶掉了，不动
		}
		delete(r.tokens, d.Name())
		for i, existing := range r.detectors {
			if existing.Name() == d.Name() {
				r.detectors = append(r.detectors[:i], r.detectors[i+1:]...)
				break
			}
		}
		return nil
	}, nil
}

// match asks all detectors: if any claims it, it is
// a shape error, and the claimer's name is returned.
//
// The name is returned so the log can say "which rule recognized it" -- shape
// detectors are each upstream's own test, and a log line that only says "a shape
// error was hit" is useless when two upstreams report 400 at the same time.
//
// Each detector is recovered separately: what is registered is someone else's code, and
// one bad detector must not crash the whole recording path (the same fail-open principle
// -- better to miss one shape error than to take the proxy down over one patch).
func (r *shapeRegistry) match(status int, body []byte) (string, bool) {
	r.mu.RLock()
	detectors := r.detectors
	r.mu.RUnlock()
	for _, d := range detectors {
		if shapeMatch(d, status, body) {
			return d.Name(), true
		}
	}
	return "", false
}

// names returns the names of the currently active detectors (sorted, for logs and test assertions).
func (r *shapeRegistry) names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.detectors))
	for _, d := range r.detectors {
		out = append(out, d.Name())
	}
	sort.Strings(out)
	return out
}

// shapeMatch wraps a recover layer. It is a separate top-level function so the defer's
// recovery range is exactly "this one call", rather than wrapping match's whole loop.
func shapeMatch(d ShapeDetector, status int, body []byte) (matched bool) {
	defer func() {
		if recover() != nil {
			matched = false
		}
	}()
	return d.Match(status, body)
}

// shapeOf asks "is this failure a request-shape problem", and which test recognized it.
//
// **With no detector registered it is always ("", false)**: that means
// 400 is never charged to anyone (see Classify: the Bucket of a non-shape 400
// is BucketNone). That is the safe direction -- missing one shape error is one
// count less, while a false positive lets a real availability failure through.
func (b *table) shapeOf(status int, body []byte) (string, bool) {
	return b.shapes.match(status, body)
}

// ShapeDetectors lists the currently active shape detector names, for diagnostics and logs.
func (b *table) ShapeDetectors() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.shapes.names()
}

// RegisterShapeDetector is the port implementation: hand the
// test to the module that knows that provider's validation rules.
func (b *table) RegisterShapeDetector(d ShapeDetector) (modules.Release, error) {
	return b.shapes.Register(d)
}
