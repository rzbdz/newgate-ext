package gateway

import (
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/metrics"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// What the gateway contributes to the web UI: **counters**.
//
// Why the gateway reports them instead of the UI reading metrics.Default: those counters'
// names, groupings, and the wording of each line's "what this number means" are all data-plane
// semantics (see metrics/hints.go). If the UI read that global table itself, it would have to
// know **machine markers** like `[shape-400]` -- those are grep anchors, not copy for humans.
//
// This and the CLI side share a source: `newgate metrics` uses the same groupings and wording
// (the same metrics.Group / metrics.Hint), so the two UIs cannot tell different stories.

type seriesEntry struct {
	Name  string `json:"name"`
	Value uint64 `json:"value"`
	Hint  string `json:"hint,omitempty"`
}

type seriesGroup struct {
	ID       string        `json:"id"`
	Label    string        `json:"label"`
	Counters []seriesEntry `json:"counters"`
}

type seriesData struct {
	Groups []seriesGroup `json:"groups"`
	Total  uint64        `json:"total"`
}

// gatewayConcepts is the gateway's presentation surface at this moment: counters, the tail of
// the proxy log, and the roster of the special_treatment plugin layer.
//
// It is called when **someone comes to look at the UI**, not at assembly -- so the counters
// are "now", and so is the log tail, rather than what they were when this process started
// (see the package comment in lib/view).
//
// Why the gateway reports the log instead of letting the UI tail the file itself: **what the
// lines are** is data-plane semantics (`-> provider/model`, `X-Newgate-Route`, the upstream's
// original text lines). If the UI read the file itself, it would have to know those lines --
// and they are grep anchors, not copy for humans (the same reason as on the metrics side).
func gatewayConcepts() ([]view.Concept, error) {
	return []view.Concept{
		{
			ID: "gateway.metrics", Kind: view.KindSeries, Title: i18n.T("Counters", nil),
			Data: seriesGroups(),
			// All three are a read from memory (counter snapshot, log tail, plugin table +
			// state.json), so all declare Live: the UI asks every few seconds and sees "now".
			Live: true,
		},
		logConcept(),
		specialConcept(),
	}, nil
}

// specialConcept is the special_treatment plugin layer: which patches are touching requests,
// why they exist, and whether they are in effect right now.
//
// **Why it must be registered before the cli block returns**: this layer **modifies the
// user's request**, and `newgate st` exists precisely to answer "is newgate breaking my
// request" (see the comment in command_special.go). In an assembly with only dashboard
// (dist-dashboard has no cli) that command does not exist at all -- without this card, that
// question has no answer whatsoever in that distribution.
//
// **It is read-only on purpose, but the price must be spelled out**: in an assembly with cli,
// turning things on and off is `newgate st on|off`; here the job is only "see clearly". And in
// **the assembly without cli that command does not exist either** -- the only way to change
// anything there is to edit `ModuleConfig["gateway"]` by hand on the code card of
// `config.file.state.json`. That gap is real: writability needs a CAS write
// (store.WriteIfUnchanged + mapping StaleError to view.Conflict), the same trade-off as
// pluginmanager keeping the footgun read-only (see the comment there); ship read-only first,
// but do not say "read-only" as if it could be "changed elsewhere" -- in that distribution
// elsewhere does not exist.
//
// The data is all in memory (plugin table + state.json), so it is as cheap as the counters and
// can be asked along with the UI's few-second refresh -- unlike the config contributor, which
// must re-read and re-parse every profile (see the per-source refresh in web-dashboard).
func specialConcept() view.Concept {
	st := store.LoadState()
	rows := []view.Row{}
	for _, p := range special.Plugins() {
		mark, word := specialState(st, p.Name())
		rows = append(rows, view.Row{Cells: map[string]view.Cell{
			"state":  {Text: word, Tone: specialTone(mark)},
			"plugin": {Text: p.Name()},
			// Take only the first line of Why: the full explanation is often several lines, and
			// spreading it into the table drowns it -- the CLI makes the same trade-off
			// (`newgate st <plugin>` is where the full text lives).
			"why": {Text: strings.SplitN(p.Why(), "\n", 2)[0]},
		}})
	}
	return view.Concept{
		ID: "gateway.special", Kind: view.KindTable,
		Title: i18n.T("Upstream quirk patches", nil),
		// Live: the switches can be flipped elsewhere (`newgate st off`, editing state.json),
		// and this card's value is exactly "which patches are touching my request now" --
		// freezing at the moment the page was opened is not an answer.
		Live: true,
		Data: view.Table{
			Columns: []view.Column{
				{ID: "state", Label: i18n.T("State", nil)},
				{ID: "plugin", Label: i18n.T("Plugin", nil)},
				{ID: "why", Label: i18n.T("Why it exists", nil)},
			},
			Rows: rows,
		},
	}
}

type logData struct {
	Path string `json:"path"`
	// Are Lines **newest first** or oldest first? Oldest first (the last line written is at
	// the bottom): a log is read downward, and putting the newest line on top makes every
	// refresh look like a jump.
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated,omitempty"`
	Missing   bool     `json:"missing,omitempty"`
}

// logTail is how many lines to read. A few hundred is enough to see "what just happened",
// and this view is asked again every few seconds -- shipping a whole 16MB log over is making
// the browser carry the entire history on every refresh.
const logTail = 400

// logRead is the maximum number of bytes to read at a time (reading backwards from the end of
// the file).
//
// No seeking by line count: line length has no upper bound (one upstream error quote can be
// very long), and walking back line by line would mean reading the whole file. 256KB is ample
// headroom for "the last 400 lines", and it is an **upper bound** -- this function does not get
// slower when the log grows to 16MB.
const logRead = 256 << 10

func logConcept() view.Concept {
	path := paths.LogFile()
	lines, truncated, err := tailLines(path, logTail, logRead)
	data := logData{Path: path, Lines: lines, Truncated: truncated}
	// The log file not existing yet (this process has not written anything) is not an error,
	// it is **a normal first day**. Keeping the two apart is what stops the UI from drawing
	// "no log yet" as "something failed".
	data.Missing = err != nil
	if data.Missing {
		data.Lines = nil
	}
	return view.Concept{
		ID: "gateway.log", Kind: view.KindLog, Title: i18n.T("Proxy log", nil),
		// Read-only: a log is **what happened**, not configuration. This concept has no Apply.
		Data: data,
		Live: true,
	}
}

// tailLines reads the last n lines of a file, reading at most limit bytes.
//
// The returned truncated means "the file is bigger than limit, there is more before this" --
// without saying so, the user would think the log is only this long and hunt for a cause in
// output that has no beginning.
func tailLines(path string, n, limit int) ([]string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	size := st.Size()
	read := size
	truncated := false
	if read > int64(limit) {
		read = int64(limit)
		truncated = true
	}
	buf := make([]byte, read)
	if _, err := f.ReadAt(buf, size-read); err != nil && err != io.EOF {
		return nil, false, err
	}
	// The first "line" from a mid-file read is likely half a line (cut off by limit); drop it.
	text := string(buf)
	if truncated {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, truncated, nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
		truncated = true
	}
	for i, line := range lines {
		lines[i] = redactLine(line)
	}
	return lines, truncated, nil
}

// secretInLine is the catch-all criterion for "there is a credential in this line".
//
// Credentials should never appear in logs, but the log is **written by others**: a provider
// URL carrying a key, some module printing whole request headers, a future plugin recording
// the body -- none of these are things the gateway can guarantee will not happen. And this
// view ships the log to the browser (loopback is still readable by **any process on this
// machine**, see the note on secretKeys in config/view.go). So there is one catch-all here:
// anything whose shape is recognizable becomes ***.
//
// Better to over-kill (mask a legitimate long string) than to let one through -- a few missing
// characters in the log are much lighter than a key showing up in the browser.
var secretInLine = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{12,}`),
	regexp.MustCompile(`(?i)\b(api[_-]?key|token|authorization|bearer)\b\s*[:=]?\s*[A-Za-z0-9_\-.]{12,}`),
}

func redactLine(line string) string {
	for _, re := range secretInLine {
		line = re.ReplaceAllStringFunc(line, func(m string) string {
			// Keep the "which field" segment: wiping everything leaves the investigator not even
			// knowing there was something here.
			if i := strings.IndexAny(m, ":="); i >= 0 {
				return m[:i+1] + "***"
			}
			return "***"
		})
	}
	return line
}

// metricGroups groups counters by Group: id handles sorting and dedup, label is only for
// printing.
func seriesGroups() seriesData {
	// Take the snapshot once: it is a map swapped in atomically; taking it twice can yield two
	// different moments (requests run in between) and the numbers within a group stop adding
	// up.
	snap := metrics.Default.Snapshot()
	byID := map[string]*seriesGroup{}
	for _, name := range metrics.SortedKeys(snap) {
		id, label := metrics.Group(name)
		g := byID[id]
		if g == nil {
			g = &seriesGroup{ID: id, Label: label}
			byID[id] = g
		}
		g.Counters = append(g.Counters, seriesEntry{Name: name, Value: snap[name], Hint: metrics.Hint(name)})
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	data := seriesData{Groups: make([]seriesGroup, 0, len(ids))}
	for _, id := range ids {
		data.Groups = append(data.Groups, *byID[id])
		for _, c := range byID[id].Counters {
			data.Total += c.Value
		}
	}
	return data
}
