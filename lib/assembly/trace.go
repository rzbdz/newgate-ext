// Package assembly turns the component graph's assembly trace into a log record.
//
// Why it is here and not in the kernel: the kernel only offers the fact
// outlet (component.SetTrace). "Report it or not, where, in what format, and
// whether to dedupe repeated identical assemblies" is a product decision. This
// used to live in the kernel's app package together with the distribution's
// log path, which made the framework unusable without a distribution.
//
// The kernel only offers the fact outlet (component.SetTrace); it knows nothing
// of logs, files, or env vars. "Report, where, when" is a product decision.
package assembly

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"sync"
	"time"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// TraceTo writes the assembly process (scan -> declare -> compose -> start -> stop)
// to these writers and returns a finalizer (call it before exit). No writer = off.
//
// It is the only primitive in this family; everything else (child processes
// included) starts from it, so it depends on no concrete destination and need not
// be a *log.Logger: assembly output is timestamped anyway, and it is an io.Writer.
func TraceTo(writers ...io.Writer) func() {
	live := make([]io.Writer, 0, len(writers))
	for _, w := range writers {
		if w != nil {
			live = append(live, w)
		}
	}
	if len(live) == 0 {
		modules.SetTrace(nil)
		return func() {}
	}
	lg := make([]*log.Logger, 0, len(live))
	for _, w := range live {
		lg = append(lg, log.New(w, "", log.LstdFlags))
	}
	modules.SetTrace(func(format string, args ...any) {
		for _, l := range lg {
			l.Printf(format, args...)
		}
	})
	return func() { modules.SetTrace(nil) }
}

// TraceToLogFile appends the assembly process to newgate's log file; with
// `NEWGATE_TRACE=1` it **also** writes to stderr, so a local run can see the order
// first-hand (that must not be the default -- CLI output is for humans, and the answer
// to `newgate tier normal` must not be buried under dozens of assembly log lines).
//
// # Why every process records it, not just the daemon
//
// The assembly trace is the only first-hand material for "which modules this build actually
// started, in what order, and whose soft dependency missed" -- it exists only during
// assembly, and afterwards the Manager cannot answer that (it only holds the result).
//
// The case that needs it most is **graceful handover**: the old process is still serving and
// the new one assembles in the background, and the user never sees it. And the graceful path
// of `newgate restart` has the **old process** hand the socket to the new one, so the old
// binary cannot brand the new one with "you are the daemon" -- the test must be something
// the process knows **itself**. Using "`__serve` is in argv" as the test copies gateway's
// verb names into main; using an env var makes it fail on exactly the upgrade that needs it
// most. So no distinction is made here: **whoever assembles the graph leaves this record**.
//
// # One assembly is written once (2026-09-20)
//
// The cost of "no distinction" above is far larger than the numbers the comment used
// to give: with 24 components in the full graph, every `newgate …` command (`status`,
// `--help`, `newgate claude` on the agent launch path) wrote **104 lines** into the log,
// and those 104 lines were **identical word for word** -- the declaration order is fixed
// at compile time, the topological order is a stable sort, and the latency digits are the
// only thing that changes. One development afternoon measured 38087 log lines, of which
// real proxy traffic was **23 lines**: this log is the product's main debugging surface
// (`newgate logs`, the alllogs diagnostic bundle), and 99.9% of it was assembly echo.
//
// So it now writes the **whole record only when the assembly
// changed**, and writes **not a single character** when it did not:
//
//	2026/09/20 18:48:07 assembly: fingerprint 3f9a1c2b4d5e — this block is new
//
// (Tightened again on 2026-09-21: previously a line saying "same as before" was left when nothing
// changed. That line was still echo -- one `newgate status` added a "nothing changed" line to
// the daemon log, and every line in this log should be a transition. The reasoning is in flush.)
//
// The test is a hash of that text **with timestamps and durations stripped**. What it buys is
// exactly the reason it was wanted: the whole record from an upgrade is still there (old and new
// binaries ship different things -> different fingerprint -> the block is written out), while
// ten thousand commands with the same binary leave only "I assembled once, and it was this one".
// This is the same policy as watcher and configshare -- **report only on state transitions**.
//
// Two trade-offs come with it, both spelled out:
//
//   - **The record is buffered and written at the end**, so if the process
//     dies **mid-assembly** this block is not in the log (it used to write as it assembled,
//     so a death left half of it). To see that half, use `NEWGATE_TRACE=1` -- that path still
//     streams everything out, unbuffered and unjudged, and it is the escape hatch for this
//     rule. The cost is that such a run **writes no fingerprint** (it takes the old path,
//     with no marker line at the end), so **the very next run rewrites the whole block**, and
//     after that it is back to one line. This is deliberate: the point of the escape hatch
//     is "the old behavior, not one byte changed", and it is a debug switch, not the norm.
//   - The fingerprint is looked up **only in the current log file** (reads the last 8KB, no
//     separate state file). So the first run after a log rotation rewrites the whole block -- which
//     is right: a new log should carry this record itself instead of inheriting one it never had.
//
// # If it cannot be opened, skip silently
//
// A typical scene: root's CLI hits a `0640 root:developer` file created by claude
// (CLAUDE.md §3.1). Not reporting or blocking here is a deliberate trade-off -- the
// trace is diagnostic material, and every `newgate status` should not spit a permission
// warning at the terminal, while that permission problem is already reported loudly
// on other paths (thinkcache writes disabled, control token cannot be written out).
func TraceToLogFile(logFile string) func() {
	// **O_RDWR, not O_WRONLY**: the previous record must be read back
	// here (lastFingerprint). Opened write-only, ReadAt always gives EBADF and the look-back
	// always comes back empty -- so every run counts as the "first", writes the whole block,
	// and the thing this file exists to fix is still there. Hit for real on 2026-09-20: the
	// unit test opened the file with O_RDWR itself, so it could not see it (only after adding
	// TestRepeatedInvocationsOfTheRealThing did it show). O_APPEND keeps writes append-only.
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o660)
	if err != nil {
		if os.Getenv("NEWGATE_TRACE") != "" {
			return TraceTo(os.Stderr)
		}
		return TraceTo()
	}
	// NEWGATE_TRACE means "I want to watch it run right now": no buffering, write as it assembles.
	if os.Getenv("NEWGATE_TRACE") != "" {
		stop := TraceTo(f, os.Stderr)
		return func() {
			stop()
			_ = f.Close()
		}
	}
	rec := newAssemblyRecord(f, lastFingerprint(f))
	modules.SetTrace(rec.add)
	return func() {
		rec.flush()
		modules.SetTrace(nil)
		_ = f.Close()
	}
}

// fingerprintMarker is the **machine anchor** of the marker line
// written to the log. It deliberately bypasses i18n: the last record is found by
// it (see lastFingerprint), and a translated marker would never match again under a
// Chinese locale -- every command would rewrite the whole block, exactly what this
// file exists to fix. The one-sentence explanation after it is the translated part.
var fingerprintMarker = regexp.MustCompile(`assembly: fingerprint ([0-9a-f]{12})`)

// tailWindow is how much of the log tail is read when looking back for the last record.
//
// Why 8KB: the full record for 24 components is about 10KB, and the
// marker is written at the **very end**, so the window only has to
// cover "around the last line" -- it need not cover the whole block.
const tailWindow = 8 << 10

// lastFingerprint finds the most recent recorded fingerprint
// in the current log's tail; with none, it returns empty.
//
// Only this one log file is read, not the `.1` generations: after
// a rotation the file holds no record, so the next run rewrites the whole
// block -- the new log carrying the record itself is exactly what is wanted.
func lastFingerprint(f *os.File) string {
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	size := st.Size()
	off := int64(0)
	if size > tailWindow {
		off = size - tailWindow
	}
	buf := make([]byte, size-off)
	// ReadAt does not move the file offset, so this look-back does not affect later appends.
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return ""
	}
	if off > 0 {
		// The window may cut through a line, so the first segment is half a line; drop it.
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			return ""
		}
		buf = buf[i+1:]
	}
	all := fingerprintMarker.FindAllSubmatch(buf, -1)
	if len(all) == 0 {
		return ""
	}
	return string(all[len(all)-1][1])
}

// The fingerprint strips the two parts that change every time: the
// timestamp prefix and the durations. What remains (modules, order, whether
// a soft dependency hit) is "what this build's assembly looks like".
//
// **A duration must be recognized by shape, not by word**: the trace is translated (component
// calls i18n.T), and the Chinese line reads like the localized "start 4/23 locale done (took
// 3.489ms)" -- matching only the English "(took …)" strips nothing, so every line's duration enters
// the fingerprint, **every run is a new fingerprint**, and the whole block is rewritten every
// time. Measured in production on 2026-09-20: the same binary wrote four different fingerprints
// in a row (e5fe…/c683…), and this dev box's sandbox is English, so it never showed up.
//
// Units are not translated (`ms` is still `ms` in a Chinese
// line), so "number + unit" is a language-independent test.
var (
	traceStamp = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)
	// Durations are stripped by **shape**: digits immediately followed by a unit. Units
	// are not translated (the Chinese line is "... done (took 3.489ms)" with `ms` still
	// `ms`), so this test is language-independent -- while matching only the English "(took
	// …)" misses every translation, whose symptom is rewriting the whole block on every run
	// in another language (measured in production 2026-09-20: the same binary wrote four
	// different fingerprints in a row; the sandbox was English, so it did not show up).
	//
	// Parentheses and commas **do not matter**: they sit in the same place every time
	// and are stable; all that is wanted here is stability, not looks. That is also
	// why this regex needs no Chinese punctuation at all -- one less thing to maintain.
	traceDuration = regexp.MustCompile(`\d+(?:\.\d+)?(?:ns|µs|us|ms|s)\b`)
)

// normalizeTraceLine strips what in an assembly line has nothing to do with "what was installed".
func normalizeTraceLine(l string) string {
	l = traceStamp.ReplaceAllString(l, "")
	return traceDuration.ReplaceAllString(l, "(d)")
}

// assemblyFingerprint is the hash of the whole record (timestamps and durations removed).
func assemblyFingerprint(lines []string) string {
	h := sha256.New()
	for _, l := range lines {
		_, _ = io.WriteString(h, l)
		_, _ = h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// assemblyRecord buffers an assembly record and at the end decides: write the block or nothing.
type assemblyRecord struct {
	mu    sync.Mutex
	out   io.Writer
	prev  string   // the fingerprint recorded last time (empty = no record in this log)
	lines []string // timestamped raw text: used when writing the whole block out
	plain []string // timestamps and durations stripped: used to compute the fingerprint
}

func newAssemblyRecord(out io.Writer, prev string) *assemblyRecord {
	return &assemblyRecord{out: out, prev: prev}
}

// add is the component.SetTrace callback; every event during assembly passes through here.
//
// The timestamp is applied right here (not added at the end): the **order and latency**
// of those 24 `start n/24 … (took …)` lines is exactly where the record's value lies, and
// stamping them all with one time after buffering would flatten them into one instant.
func (r *assemblyRecord) add(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, stamp()+msg)
	r.plain = append(r.plain, normalizeTraceLine(msg))
}

// flush writes it out. The block is only written when the fingerprint changed, else one line.
func (r *assemblyRecord) flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) == 0 {
		return
	}
	// The comparison is against the **12 chars written to the log**, not the full 64-bit hash:
	// prev is read back from the log, so comparing full length never matches and the whole block
	// is written every time -- the thing this file exists to fix is still there, and the log
	// looks perfectly normal (caught by TestFullRecordIsWrittenOnlyWhenItChanges on 2026-09-20).
	fp := assemblyFingerprint(r.plain)[:12]
	if fp == r.prev {
		// **Write not a single character** (2026-09-21, the
		// user: do not let this show up in the log again).
		//
		// This used to leave one line: "same as the existing record". It was meant to fix
		// the log drowning in assembly echo, but the line it left was **still echo**: every `status`,
		// `doctor`, `tier` … stuffed a "nothing changed" line into the daemon log. And this log
		// is the product's main debugging surface (`newgate logs`, the alllogs bundle); every line
		// in it should be **a transition** -- the policy is stated at the top of this file ("report
		// only on state transitions"), and that line was precisely not a transition, a contradiction.
		//
		// Writing nothing does not make anything unfindable: the block for an assembly
		// that **did change** is in the log, with its fingerprint; to see the current
		// run live there is `NEWGATE_TRACE=1`. And `lastFingerprint` relies on that
		// marker line -- it is still there, so the next run still recognizes "unchanged".
		return
	}
	for _, l := range r.lines {
		fmt.Fprintln(r.out, l)
	}
	fmt.Fprintf(r.out, "%sassembly: fingerprint %s — %s\n", stamp(), fp, i18n.T(
		"the full assembly record above is new; it is written again only when the graph changes", nil))
}

// stamp is the log.LstdFlags format, exactly what the TraceTo path writes.
func stamp() string { return time.Now().Format("2006/01/02 15:04:05 ") }
