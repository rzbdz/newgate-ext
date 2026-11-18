package proto

import (
	"context"
	"log"
	"math/rand"
	"strings"
	"time"

	"github.com/rzbdz/newgate/lib/i18n"
)

// Backoff: after a failure 30 -> 60 -> 120 -> 300 (capped), success returns to base.
//
// Base and cap differ: the normal interval (default 30s) may be shorter than the first
// backoff. The semantics differ - "how often to check for new config" vs "the peer is
// already failing, how soon to try again".
const (
	backoffBase = 30 * time.Second
	backoffMax  = 300 * time.Second
	jitterFrac  = 0.2
)

// puller is the one cyclic dependency (a test seam: the whole loop runs without a network).
type puller interface {
	Pull(ctx context.Context, force bool) (Outcome, error)
}

// Poller is the background pull in the daemon.
type Poller struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// StartPoller starts background pulling per the machine's local settings.
//
// Returning (nil, nil) is **normal**, not an error: this machine does not use config
// sharing (no role), or auto_pull is off. Most machines are like that - no startup error.
//
// # Why cli.Serve calls it rather than the module's Start
//
// Not just an import cycle, but a real bug: app.New() builds the whole graph on **every
// invocation**, including `newgate status` and worst, `newgate claude` (the PATH shim
// path: cmd/newgate/main.go does app.New, then dispatches). A poller in Start means
// every short-lived CLI, agent launch included, puts a network pull on agent startup.
// And the self-PID "am I the daemon" guard reads a wrong pid during **graceful handoff**
// (pidfile in transition, see cli.Serve), leaving the new daemon without a poller -
// the worst possible failure mode for background sync.
func StartPoller(ctx context.Context, version string, lg *log.Logger) (*Poller, error) {
	settings, err := LoadSettings()
	if err != nil {
		return nil, err
	}
	if !settings.IsClient() || !settings.Auto() {
		return nil, nil
	}
	rootKey, fromEnv, err := RootKeyFromEnv()
	if !fromEnv {
		if rootKey, err = LoadRootKey(); err != nil {
			// fail-open: the daemon forwards as usual, just no config pull. Say how to fix it.
			lg.Printf("[configshare] %s", i18n.T(
				"background sync did not start: {err} (on the host run newgate config secrets init to get the blob, then on this machine run newgate config trust <blob>)",
				i18n.A{"err": err}))
			return nil, nil
		}
	} else if err != nil {
		return nil, err
	}
	c, err := New(Config{
		Endpoint: settings.Endpoint,
		RootKey:  rootKey,
		Version:  version,
		Log:      lg,
	})
	if err != nil {
		return nil, err
	}
	interval := settings.Interval()
	p := runLoop(ctx, c, interval, lg, nil)
	lg.Printf("[configshare] %s", i18n.T(
		"background sync started (replica): {endpoint}, one round every {interval}, root key fingerprint {fingerprint}",
		i18n.A{"endpoint": settings.Endpoint, "interval": interval, "fingerprint": RootKeyFingerprint(rootKey)}))
	return p, nil
}

// runLoop starts the loop. probe is a test observation point (called per round); nil in prod.
func runLoop(ctx context.Context, pl puller, interval time.Duration, lg *log.Logger, probe func(round int, delay time.Duration)) *Poller {
	pctx, cancel := context.WithCancel(ctx)
	p := &Poller{cancel: cancel, done: make(chan struct{})}
	go p.loop(pctx, pl, interval, lg, probe)
	return p
}

// Stop stops background pulling and waits for it to finish.
//
// cancel aborts in-flight HTTP requests (they hang off pctx), so the wait is bounded:
// an unresponsive peer cannot stall shutdown. That matters in graceful handoff - the
// old daemon must exit after draining, not be dragged out by a stuck sync.
func (p *Poller) Stop() {
	if p == nil {
		return
	}
	p.cancel()
	<-p.done
}

func (p *Poller) loop(ctx context.Context, pl puller, interval time.Duration, lg *log.Logger, probe func(int, time.Duration)) {
	defer close(p.done)
	var rs roundState
	for round := 1; ; round++ {
		if ctx.Err() != nil {
			return
		}
		// Pull the first round immediately (no interval wait): a restarted daemon should
		// converge at once, not idle 30 seconds while users still see the old config.
		out, err := pl.Pull(ctx, false)
		if err != nil {
			// Even the ledger cannot be written (disk full, permissions): count a failure.
			out.Failed = true
			out.ErrText = i18n.T("cannot write the bookkeeping: {err}", i18n.A{"err": err.Error()})
		}
		rs.report(lg, out, interval)

		delay := nextDelay(interval, rs.fails, rand.Float64())
		if probe != nil {
			probe(round, delay)
		}
		if !wait(ctx, delay) {
			return
		}
	}
}

// roundState is what the loop remembers between rounds (failure count, last error text).
type roundState struct {
	fails    int
	lastErr  string
	lastWarn string
}

// report renders one round's result per the log policy and maintains the failure count.
//
// # Log policy (never silent, never a flood)
//
// Copies the watcher's "report only on state transitions" spirit:
//   - success (after failures) -> one "sync recovered" line;
//   - failure -> report on the 1st/2nd/5th/10th/25th/50th, then every 100th; **a
//     changed error string is reported at once** (a new error is new information);
//   - new config pulled -> one "config updated" line, then the watcher adds
//     "[config] config hot reload (generation N)" a second later. Both are true:
//     two layers that do not know each other (we wrote, the watcher saw it).
//   - idle -> **fully silent** (no decrypt when gen is unchanged, so no log either).
func (r *roundState) report(lg *log.Logger, out Outcome, interval time.Duration) {
	if out.Failed {
		r.fails++
		changed := out.ErrText != r.lastErr
		r.lastErr = out.ErrText
		if shouldLog(r.fails, changed) {
			lg.Printf("[configshare] ⚠ %s", i18n.T("sync failed (attempt {n}, retrying in {delay}): {err}",
				i18n.A{"n": r.fails, "delay": humanDur(nextDelay(interval, r.fails, 0.5)), "err": out.ErrText}))
		}
		return
	}
	if r.fails > 0 {
		lg.Printf("[configshare] %s", i18n.T("sync recovered (after {n} consecutive failures)", i18n.A{"n": r.fails}))
	}
	r.fails = 0
	r.lastErr = ""
	for _, note := range out.Notes {
		lg.Printf("[configshare] %s", note)
	}

	// Only report "updated" when something really hit the disk: a generation bump with
	// identical bytes (the host re-published the same content) is not an update.
	if out.Config.Changed {
		changed := len(out.Config.Written) + len(out.Config.Removed)
		lg.Printf("[configshare] %s", i18n.N(
			"configuration updated (gen {from} → {to}, {n} file){files}",
			"configuration updated (gen {from} → {to}, {n} files){files}",
			changed, i18n.A{"from": out.PrevGen, "to": out.Config.Gen, "files": fileSummary(out.Config)}))
		if out.Config.Archive != "" {
			lg.Printf("[configshare] %s", i18n.T("local changes archived to {dir} (restore from there)",
				i18n.A{"dir": out.Config.Archive}))
		}
	}
	if out.Secrets.Changed {
		lg.Printf("[configshare] %s", i18n.T("secrets updated (gen {gen})", i18n.A{"gen": out.Secrets.Gen}))
	}

	// Warnings are reported once, only when **content changed**. Recomputed every round,
	// so unconditional printing would repeat one line every 30s: not a flood rule.
	warns := strings.Join(out.Config.Warnings, " | ")
	if warns != "" && warns != r.lastWarn {
		for _, w := range out.Config.Warnings {
			lg.Printf("[configshare] %s", i18n.T("note: {msg}", i18n.A{"msg": w}))
		}
	}
	r.lastWarn = warns
}

// nextDelay computes the wait until the next round.
//
// interval is the normal cadence; after a failure the backoff series is used
// (30 -> 60 -> 120 -> 300 capped). Both get ±20% jitter.
func nextDelay(interval time.Duration, fails int, rnd float64) time.Duration {
	d := interval
	if fails > 0 {
		d = backoffBase
		for i := 1; i < fails && d < backoffMax; i++ {
			d *= 2
		}
		if d > backoffMax {
			d = backoffMax
		}
	}
	return jitter(d, rnd)
}

// jitter adds ±20% to a duration.
//
// Why it is mandatory: many machines' tickers **lock phase** at startup (same batch,
// same 30s), so they all hit the host every 30s and idle the other 30. Jitter spreads
// them out and turns the host's load from a spike into a flat line - which for a design
// where everyone keeps forwarding anyway is a real improvement to hosts' logs.
func jitter(d time.Duration, rnd float64) time.Duration {
	if d <= 0 {
		return d
	}
	// rnd in [0,1) -> factor in [0.8, 1.2)
	out := time.Duration(float64(d) * (1 + jitterFrac*(2*rnd-1)))
	if out <= 0 {
		return d
	}
	return out
}

// shouldLog is the log suppression policy (pure function, easy to test).
//
// The 1st, then 2/5/10/25/50, then every 100th; a changed error string logs at once.
// Logging the 1st is a hard requirement: a new problem **must** be stated in the first
// cycle, or "never silent" is an empty phrase (users would think all is well).
func shouldLog(fails int, errChanged bool) bool {
	if fails <= 1 || errChanged {
		return true
	}
	switch fails {
	case 2, 5, 10, 25, 50:
		return true
	}
	return fails > 50 && fails%100 == 0
}

// wait waits for a duration; returns false when ctx ends (time to pack up).
//
// A Timer rather than a Ticker: the interval changes per round (backoff), so a Ticker
// would need constant resets, while a Timer is naturally "wait this long this round".
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// fileSummary joins "which files moved this round" into a short summary. Added ones are
// written plainly, retired ones prefixed with `-`: "deleted" and "written" are not alike.
func fileSummary(ch Channel) string {
	names := append([]string(nil), ch.Written...)
	for _, r := range ch.Removed {
		names = append(names, "-"+r)
	}
	if len(names) == 0 {
		return ""
	}
	const maxShow = 6
	// Truncation reports the **total**, not the remainder ("and 8 more"): users want how
	// many moved this round, not how many were not listed. TestFileSummaryTruncates pins this.
	if len(names) > maxShow {
		return i18n.T(": {names} and {n} in total", i18n.A{"names": strings.Join(names[:maxShow], ", "), "n": len(names)})
	}
	return i18n.T(": {names}", i18n.A{"names": strings.Join(names, ", ")})
}

// humanDur renders a duration for humans ("30s" / "5m0s"), **truncated to whole
// seconds** - logs need no sub-second precision, and with ±20% jitter you get things
// like 27.4s, which reads as implying a precision that does not exist.
func humanDur(d time.Duration) time.Duration { return d.Truncate(time.Second) }
