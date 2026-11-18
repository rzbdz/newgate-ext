// Package serving is the edge for "**this process has started serving**":
// modules that want to do something in the serving process hang themselves here.
//
// # Why it is needed (rather than doing it in Start)
//
// Every `newgate …` process runs the modules' Start -- typing a command, entering
// a shell, and running a health check all do. So "start a listener", something **only the
// serving process should do**, done in Start would turn every CLI invocation into a server (port
// conflicts, zombie processes, `newgate status` incidentally opening a web UI). The lesson from
// modules/i18n is where this rule came from, and the web UI is its second victim: the path it
// wants to mount (porthub) may well not be installed, and then it has to listen on a port itself.
//
// # Who notifies, who listens
//
//   - **The notifier is the module that owns the port** (today: gateway's Serve, before entering
//     accept). It need not know who is listening -- that is precisely why this method exists:
//     the port's owner says "I started", and needs to know no one who wants to ride along.
//   - **The listeners are modules that want to work in the serving process**. They
//     register in Start, and the callback only runs at the moment of notification.
//
// # Why not the entry ledger
//
// "Is this invocation `__serve`" looks like it should ask entry, but entry is only
// asked once by the composition root **after** assembly, while Start runs **during**
// assembly -- the order does not allow it. More importantly, the test itself: a
// module reading os.Args to decide this is explicitly forbidden by the kernel (see the
// note in component/entry). So the question is changed here: do not ask "who am I",
// just wait for "someone started serving". That fact only the port's owner can state.
package serving

import (
	"errors"
	"sync"

	"github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Starter is "serving has started, please begin work".
//
// The returned stop is called when serving ends (usually when Serve returns). It **must
// be idempotent**: both the process exit path and the normal wrap-up may reach it.
type Starter func() (stop func(), err error)

// Service is the face the listeners see, and the face the notifier sees.
type Service interface {
	// OnServe registers a callback. It runs once, in the **serving process**, at the moment of Notify.
	OnServe(name string, start Starter) (component.Release, error)

	// Notify is called by the module that owns the port: it runs
	// all registered callbacks and returns a shutdown function.
	//
	// A callback failing does **not** fail the whole serving run (fail-open):
	// one UI failing to start should not drag down the data plane. Failures
	// show up in the returned error, and the caller writes them to the log.
	Notify() (stop func(), err error)
}

// Capability is the identity of this edge.
var Capability = component.NewCapability[Service]("serving")

// Registry is the callback ledger.
type Registry struct {
	mu       sync.Mutex
	starters []entry
	notified bool
}

type entry struct {
	name  string
	start Starter
	seq   int
}

func NewRegistry() *Registry { return &Registry{} }

func (r *Registry) OnServe(name string, start Starter) (component.Release, error) {
	if name == "" {
		return nil, i18n.E("serving: a listener registered with no name", nil)
	}
	if start == nil {
		return nil, i18n.E("serving: {name} registered a nil starter", i18n.A{"name": name})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := entry{name: name, start: start, seq: len(r.starters)}
	r.starters = append(r.starters, e)
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		for i, cur := range r.starters {
			if cur.seq == e.seq {
				r.starters = append(r.starters[:i:i], r.starters[i+1:]...)
				return nil
			}
		}
		return nil
	}, nil
}

// Notify runs all registered callbacks. **Idempotent**: called twice in the same process,
// the second does nothing (a listener that already came up should not be started again).
func (r *Registry) Notify() (func(), error) {
	r.mu.Lock()
	if r.notified {
		r.mu.Unlock()
		return func() {}, nil
	}
	r.notified = true
	starters := append([]entry(nil), r.starters...)
	r.mu.Unlock()

	var stops []func()
	var failures []error
	for _, e := range starters {
		stop, err := e.start()
		if err != nil {
			// One UI failing to start should not drag down the data
			// plane. Include the name so the log shows who it was.
			failures = append(failures, i18n.Ef(err, "serving: {name} could not start: {err}",
				i18n.A{"name": e.name, "err": err}))
			continue
		}
		if stop != nil {
			stops = append(stops, stop)
		}
	}
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			for i := len(stops) - 1; i >= 0; i-- {
				stops[i]()
			}
		})
	}
	return stop, errors.Join(failures...)
}
