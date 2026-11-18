// Package cli is the command-line frontend.
//
// Layering rules are in docs/03-architecture.md and docs/08-operations.md: the three shells
// CLI / TUI / Web are **not allowed** to implement business logic themselves, only to call
// the layer below. Once a shell is allowed "just write this one feature yourself", the
// drift across the three begins, and it is irreversible -- the user finds out that "things
// deletable on the Web cannot be deleted from the CLI", and stops trusting any of them.
package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/buildinfo"
	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate/lib/i18n"
)

// usageText assembles `newgate --help`.
//
// **The UI itself knows no command and no module**: every command line is declared by the
// module that owns that capability, via HelpLine; this only assembles and lays out. The previous
// version hardcoded a cmd(...) list here, and the result was that the UI was still hardcoding
// commands after they moved back into modules -- "moving" accomplished nothing, and every new
// module meant coming back to edit the UI (exactly what this refactor was meant to tear down).
//
// Position is decided by the Rank each command declares; the order of sections is decided by
// the slot rules (see extension.Section and PlanSections), and within a section sorted by Rank
// then Usage. Equal Rank falls back to Usage, guaranteeing a consistent order on every run.
func usageText(service *service) string {
	// Collection: whoever injected a command declares what
	// it looks like in help and which **slot** it goes in.
	//
	// The UI here **knows no command and no module**: it only knows a set of generic slot
	// keys plus one rule -- "custom sections first-come-first-served, the losers go to others"
	// (extension.PlanSections). Installing a new module never requires changing one line here.
	type entry struct {
		declared Section
		line     HelpLine
	}
	var entries []entry
	if service != nil {
		for _, c := range service.commands.All() {
			doc, ok := c.(Documented)
			if !ok {
				continue // optional interface: undeclared means no line, but it still works
			}
			line := doc.Help()
			if line.Usage == "" {
				continue
			}
			entries = append(entries, entry{line.Section, line})
		}
	}
	var glossary []GlossaryLine
	if service != nil {
		glossary = service.glossaryLines()
	}

	// The left column's width is **computed from data**, no longer hardcoded (2026-09-20).
	//
	// The previous version was `commandWidth = 38` / `termWidth = 12` -- those two numbers
	// were **measured values from the English version** (which modules were installed, how long
	// term lines were), and they stop holding in another language or with a new module: too narrow
	// and the right column is squeezed into a vertical line, too wide and the description is pushed
	// off the page. So take the widest display width of the actual content (CJK is double width; use
	// style.VisibleWidth), then clamp it into a sensible range -- the two ends of that range are the
	// two bad cases above, and style.MaxColumns is still the hard ceiling that cannot be crossed.
	usages := make([]string, 0, len(entries))
	for _, e := range entries {
		usages = append(usages, e.line.Usage)
	}
	terms := make([]string, 0, len(glossary))
	for _, line := range glossary {
		terms = append(terms, line.Term)
	}
	commandWidth := clamp(widest(usages), 24, 40)
	termWidth := clamp(widest(terms), 8, 18)

	var b strings.Builder
	b.WriteString(style.Bold("newgate"))
	b.WriteString(" ")
	b.WriteString(i18n.T("— the semantic model layer proxy for AI CLIs", nil))
	b.WriteString("\n")

	// Pad the left column by display width (CJK double width), and make the right column always
	// dim -- when scanning you read the command name first, and the description only when needed.
	cmd := func(left, right string) {
		// When Usage is wider than the left column it **takes a line of its own**. The
		// previous version jammed it onto the same line as summary, and verify's long flags
		// turned into `...--json]one per ...` -- two things, one machine-readable and one
		// human-readable, glued together (screenshot, 2026-10-28). usage is the command's
		// syntax; it should not be truncated for layout, nor squeeze out the right column.
		if style.VisibleWidth(left) > commandWidth {
			for _, line := range wrapWords(left, style.MaxColumns-2) {
				b.WriteString("  " + style.Cyan(line) + "\n")
			}
			if right != "" {
				for _, line := range wrapWords(right, style.MaxColumns-4) {
					b.WriteString("    " + style.Dim(line) + "\n")
				}
			}
			return
		}
		// After padding the left column to a fixed width, **always** leave one more cell: when usage
		// exactly fills commandWidth, without this cell right would sit flush against the last character.
		prefix := "  " + style.Cyan(style.Pad(left, commandWidth)) + " "
		if right == "" {
			b.WriteString(prefix)
			b.WriteString("\n")
			return
		}
		lines := wrapWords(right, style.MaxColumns-2-commandWidth-1)
		for i, line := range lines {
			if i == 0 {
				b.WriteString(prefix)
				b.WriteString(style.Dim(line))
				b.WriteString("\n")
			} else {
				b.WriteString(strings.Repeat(" ", 2+commandWidth+1))
				b.WriteString(style.Dim(line))
				b.WriteString("\n")
			}
		}
	}

	// Planning is done in **declaration order** (= command registration
	// order), because custom sections are first-come-first-served.
	declared := make([]Section, 0, len(entries))
	for _, e := range entries {
		declared = append(declared, e.declared)
	}
	plan := PlanSections(declared)

	// Rows in the same displayed section are sorted by Rank then Usage; the order of sections
	// is decided by plan and does not compete on Rank -- otherwise anyone could move a whole
	// section to the front by lowering their own Rank, and the slot table would be pointless.
	grouped := map[Section][]HelpLine{}
	for _, e := range entries {
		slot := plan.Slot(e.declared)
		grouped[slot] = append(grouped[slot], e.line)
	}
	for _, slot := range plan.Order() {
		lines := grouped[slot]
		if len(lines) == 0 {
			continue
		}
		sort.Slice(lines, func(i, j int) bool {
			if lines[i].Rank != lines[j].Rank {
				return lines[i].Rank < lines[j].Rank
			}
			return lines[i].Usage < lines[j].Usage
		})
		// The heading is the slot's **display name**, not its key: the key is the identity (a stable
		// ASCII marker), and what varies by language is this layer (see extension.Section.Display).
		b.WriteString("\n")
		b.WriteString(style.Bold(slot.Display()))
		b.WriteString("\n")
		for _, line := range lines {
			cmd(line.Usage, line.Summary)
		}
	}

	// The glossary is **the UI's own** thing: the dictionary users look at when they do not
	// know a term. It is not the command list (that belongs to modules), so it stays here.
	// But **every row in the table belongs to a module** -- the previous version of the UI
	// still hardcoded `profile` and that config-file list (providers.json / mappings/*.kv
	// / state.json), and both are config's vocabulary; how would the UI know them. Now
	// everything is submitted via cliapi.Glossarist (see modules/config/diagnostics.go).
	//
	// The right column wraps together with the left: definitions are written by modules
	// and their length is not under the UI's control, and a one-line layout will eventually
	// be burst past the 75-column limit (style.MaxColumns) by some long definition.
	b.WriteString("\n")
	b.WriteString(style.Bold(i18n.T("Glossary", nil)))
	b.WriteString("\n")
	term := func(left, right string) {
		// Same reason as the command column: when the term exactly fills termWidth (four
		// Chinese characters = 8 columns, exactly the lower bound), without this cell
		// the definition would sit flush against the term (`config fileproviders.json`).
		prefix := "  " + style.Pad(style.Cyan(left), termWidth) + " "
		lines := wrapWords(right, style.MaxColumns-2-termWidth-1)
		for i, line := range lines {
			if i == 0 {
				b.WriteString(prefix)
				b.WriteString(style.Dim(line))
				b.WriteString("\n")
			} else {
				b.WriteString(strings.Repeat(" ", 2+termWidth+1))
				b.WriteString(style.Dim(line))
				b.WriteString("\n")
			}
		}
	}
	for _, line := range glossary {
		term(line.Term, line.Definition)
	}
	return b.String()
}

// wrapWords wraps by **word boundaries**; only when a single word is longer than
// the whole line does it fall back to splitting characters by display width.
//
// Why not style.Wrap: that is character-level (CJK has no spaces, and character level
// is the only viable general approach), but using it on help's right column would break
// `<tag>` into `<ta` / `g>`, and `--provider` in two -- those are **machine markers**,
// and broken they are neither greppable nor copyable. Here it splits by spaces first:
// English/flags stay whole, a Chinese sentence is one word, and only when over-wide
// does it hand over to style.Wrap for a width cut. The trade-offs on each side hold.
func wrapWords(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var lines []string
	var cur strings.Builder
	curW := 0
	add := func(word string) {
		ww := style.VisibleWidth(word)
		if curW > 0 && curW+1+ww <= width {
			cur.WriteByte(' ')
			cur.WriteString(word)
			curW += 1 + ww
			return
		}
		if curW > 0 {
			lines = append(lines, cur.String())
			cur.Reset()
			curW = 0
		}
		if ww <= width {
			cur.WriteString(word)
			curW = ww
			return
		}
		parts := style.Wrap(word, width)
		for i, part := range parts {
			if i == len(parts)-1 {
				cur.WriteString(part)
				curW = style.VisibleWidth(part)
				continue
			}
			lines = append(lines, part)
		}
	}
	for _, word := range strings.Fields(s) {
		add(word)
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func runCLI(service *service, args []string) int {
	args = splitAtVerb(service, args)
	if shouldAuditLayout(service, args) {
		return auditLayout(args, func() int { return run(service, args) })
	}
	return run(service, args)
}

func run(service *service, args []string) int {
	if len(args) == 0 {
		fmt.Print(usageText(service))
		return 0
	}

	// Action-type options, may appear anywhere
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--set-profile" || strings.HasPrefix(args[i], "--set-profile="):
			name := optValue(args, i, "--set-profile")
			if name == "" {
				return die(64, i18n.T("--set-profile needs a profile name", nil))
			}
			// The shape of this option is recognized here (argv parsing is
			// the UI's job), but the **semantics belong to config**: normalize
			// it into the shape config's command recognizes and hand it over.
			command, ok := service.moduleCommand("--set-profile")
			if !ok {
				return die(64, i18n.T("--set-profile is not implemented (this assembly has no config module)", nil))
			}
			rest := []string{name}
			if agent := findFlag(args, "--agent", "--tool", "--target"); agent != "" {
				rest = append(rest, "--agent", agent)
			}
			return command.Run(moduleCLIHost{verb: name}, rest)
		}
	}

	// Ledger lookup: commands injected by modules and the UI's own commands take the same path.
	//
	// **The UI here knows no command** -- it only knows "someone has hung something
	// in this ledger". The previous version was a switch of several hundred lines
	// here (later detectLaunch + cmdLaunch), each case a "the UI knows some module",
	// and that is why that batch of commands could never move back into their modules.
	//
	// Leading options must be tolerated too: in `newgate --profile ds claude` the
	// first token is newgate's own option, and the command to execute is further back.
	// **Finding it is argv parsing**, so it belongs to the UI; what it is called,
	// and how the remaining arguments are parsed, belongs to the command itself.
	if name := lookupName(args); name != "" {
		if command, ok := service.moduleCommand(name); ok {
			return command.Run(moduleCLIHost{verb: name}, dropName(args, name))
		}
	}
	return die(64, i18n.T("unknown command {cmd} (see newgate --help)", i18n.A{"cmd": args[0]}))
}

// VersionLine is the output of `newgate version` (layout and values are both in lib/buildinfo).
func VersionLine() string { return buildinfo.VersionLine() }

// dropName removes **that verb** from argv (the first
// token equal to name) and returns its arguments.
//
// Why not `args[1:]`: leading options must be tolerated too (`newgate --profile
// ds claude`), and there the verb is not at index 0. The contract says "args
// has no command name", so it must be stripped by name, not by position.
func dropName(args []string, name string) []string {
	for i, a := range args {
		if a == name {
			return append(append([]string{}, args[:i]...), args[i+1:]...)
		}
	}
	return args
}

// splitAtVerb splits `<verb>@<value>` into the two tokens `<verb> @<value>`.
//
// Why the UI recognizes this shape: `newgate upgrade@v0.9.0` is what users type directly
// (the same muscle memory as `pip install x==1`, `go install x@v1`), and the dispatch key
// is the verb itself -- without splitting it is an "unknown command upgrade@v0.9.0". Once split,
// `@v0.9.0` is handed to the command as a **plain positional argument**, and what it means
// (version, tag, branch) is for the command to interpret; the UI knows no value and does not
// know which command takes it -- a command that does not recognize `@…` errors by its own rules.
//
// It only splits when "the whole string is not a command but the part before @ is": a command
// whose name itself contains @ (none today) dispatches as is, and is not stolen by this sugar.
func splitAtVerb(service *service, args []string) []string {
	if service == nil || len(args) == 0 {
		return args
	}
	name := lookupName(args)
	verb, value, ok := strings.Cut(name, "@")
	if !ok || verb == "" || value == "" {
		return args
	}
	if _, exact := service.moduleCommand(name); exact {
		return args
	}
	if _, known := service.moduleCommand(verb); !known {
		return args
	}
	out := make([]string, 0, len(args)+1)
	for i, a := range args {
		if a == name {
			out = append(out, verb, "@"+value)
			return append(out, args[i+1:]...)
		}
		out = append(out, a)
	}
	return args
}

// lookupCommand looks up the ledger by the same set of name-resolution rules.
func lookupCommand(service *service, args []string) (Command, bool) {
	if service == nil || len(args) == 0 {
		return nil, false
	}
	return service.moduleCommand(lookupName(args))
}

// lookupName finds **which command should handle** this argv: skip newgate's
// own leading options and their values, take the first positional; if there
// is none, fall back to args[0] (so the unknown command error carries it).
//
// Only the "followed by a value" option shapes like `--profile` /
// `--preset` / `--agent` are recognized. That list is short and stable:
// they are newgate's own global options, not a command's arguments.
func lookupName(args []string) string {
	skipNext := false
	for _, a := range args {
		switch {
		case skipNext:
			skipNext = false
		case a == "--profile" || a == "--preset" || a == "--agent":
			skipNext = true
		case strings.HasPrefix(a, "-"):
			// Other options (--force / -v / --json …) are parsed by the command itself; skip them.
		default:
			return a
		}
	}
	return args[0]
}

// ---------- Little helpers ----------

// die is the UI's own error outlet (layout in lib/style, shared by all modules).
func die(code int, msg string) int { return style.Die(code, msg) }

// clamp squeezes a width into [lo, hi] -- both column widths of `--help` are computed
// from data, and the computed value needs a sensible range around it (see usageText).
func clamp(w, lo, hi int) int {
	if w < lo {
		return lo
	}
	if w > hi {
		return hi
	}
	return w
}

// widest is the widest **display width** among a set of
// strings (CJK double width; the test is in lib/style).
//
// Why not len(): the byte count of the Chinese for "takeover" is 6 while its
// column width is 4; a column width computed by bytes skews the layout, and
// that is half of where "it looks wrong after switching language" comes from.
func widest(ss []string) int {
	w := 0
	for _, s := range ss {
		if v := style.VisibleWidth(s); v > w {
			w = v
		}
	}
	return w
}

func optValue(args []string, i int, name string) string {
	if strings.Contains(args[i], "=") {
		return strings.SplitN(args[i], "=", 2)[1]
	}
	if i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func findFlag(args []string, names ...string) string {
	for i, a := range args {
		for _, n := range names {
			if a == n && i+1 < len(args) {
				return args[i+1]
			}
			if strings.HasPrefix(a, n+"=") {
				return strings.SplitN(a, "=", 2)[1]
			}
		}
	}
	return ""
}
