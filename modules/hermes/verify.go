package hermes

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rzbdz/newgate/lib/i18n"
)

// This file is takeover's **second gate**: before writing to disk, let hermes' own YAML parser
// confirm this surgery broke nothing.
//
// # Why it must exist (the user, 2026-11-04: "surgery directly makes me worry about
// compatibility")
//
// The upside of byte surgery is that only the intended lines are touched; its risk is that
// **our understanding of YAML** differs from hermes' parser -- one mis-counted indent and we
// edit a different key, while the file looks perfectly normal. So instead of trusting "we think
// it is right", ask **the only party entitled to an opinion**:
//
//	load(original file) + this round's edits (applied by path)  ==  load(file after surgery)
//
// Only when both sides are value-for-value equal do we write. That one sentence pins two things
// at once: what we **meant to change** really did change (present on the right), and what we
// **did not mean to change** is untouched (the rest on the left is as it was). If they differ,
// abandon the whole thing, leave the file alone, and report the path of the first difference.
//
// # Where the parser comes from
//
// hermes is a Python program, and it reads config.yaml with the PyYAML in its venv
// (safe_load). As long as hermes is installed on this machine, that interpreter is there: it is
// the shebang of the `hermes` entry script. So no new dependency is introduced here, and no
// assumption is made that the system Python has PyYAML.
//
// When no interpreter can be found (the entry is not a Python script, the venv moved), it
// **neither lets it through nor gets stuck**: it falls back to this package's own read-back
// check (every key written must read back) and says so in the report -- the degradation is
// visible.

// verifyScript reads two files and a list of edits, and prints the first mismatch (nothing if
// they agree).
//
// It uses safe_load rather than hermes' load_config: the latter merges defaults and migrates
// old keys, so what is compared is no longer "what the file says". What we care about is
// precisely the file itself.
const verifyScript = `
import json, sys, yaml
base_path, new_path, edits_path, orig_path = sys.argv[1:5]
with open(base_path) as f: base = yaml.safe_load(f) or {}
with open(new_path) as f: new = yaml.safe_load(f) or {}
with open(orig_path) as f: orig = yaml.safe_load(f) or {}
with open(edits_path) as f: edits = json.load(f)
MISSING = object()
def get(doc, path):
    for k in path:
        if not isinstance(doc, dict) or k not in doc: return MISSING
        doc = doc[k]
    return doc
want = base
for e in edits:
    path = e["path"]
    val = get(orig, path) if e.get("orig") else e.get("value", MISSING)
    if val is None and not e.get("orig"):
        val = MISSING  # a null value in an edit means: remove the key
    if val is MISSING:
        parent = get(want, path[:-1])
        if isinstance(parent, dict):
            parent.pop(path[-1], None)
        continue
    node = want
    for k in path[:-1]:
        nxt = node.get(k)
        if not isinstance(nxt, dict):
            nxt = {}
            node[k] = nxt
        node = nxt
    node[path[-1]] = val
def diff(a, b, at):
    if isinstance(a, dict) and isinstance(b, dict):
        for k in list(a) + [k for k in b if k not in a]:
            if k not in a: return at + [str(k)], "unexpected key"
            if k not in b: return at + [str(k)], "key went missing"
            d = diff(a[k], b[k], at + [str(k)])
            if d: return d
        return None
    if a != b or type(a) != type(b):
        return at, "value differs: want %r, got %r" % (a, b)
    return None
d = diff(want, new, [])
if d: print(".".join(d[0]) + ": " + d[1])
`

// verified is one check's conclusion: Parser says who confirmed it (empty = no external parser
// confirmed).
type verified struct {
	Parser string
}

// verifyEdits checks that out is "src + edits". A mismatch returns an error (the file must not
// be written).
//
// orig is where FromOrig-style edits take their values (restore: the pre-takeover original;
// takeover: unused, pass an empty string).
func verifyEdits(src, out string, edits []edit, orig string) (verified, error) {
	// First gate: this package's own read-back. Every scalar written must read back, and every
	// one removed must be gone -- this gate depends on no external program, so it always runs.
	d := parseDoc(out)
	for _, e := range edits {
		if e.FromOrig {
			continue // the value lives in the original; can't compare on read-back, so
			// leave it to the parser below
		}
		if e.Value == nil {
			if _, ok := d.lookup(e.Path...); ok {
				return verified{}, i18n.E("{path} was supposed to be removed but is still there",
					i18n.A{"path": strings.Join(e.Path, ".")})
			}
			continue
		}
		if want, ok := scalarLit(e.Value); ok {
			got, ok := d.get(e.Path...)
			if !ok || got != unquote(want) {
				return verified{}, i18n.E("{path} does not read back as written",
					i18n.A{"path": strings.Join(e.Path, ".")})
			}
		}
	}

	py := hermesPython()
	if py == "" {
		return verified{}, nil
	}
	dir, err := ioutil.TempDir("", "newgate-hermes-verify-")
	if err != nil {
		return verified{}, nil
	}
	defer os.RemoveAll(dir)
	type jsonEdit struct {
		Path  []string `json:"path"`
		Value any      `json:"value"`
		Orig  bool     `json:"orig,omitempty"`
	}
	js := make([]jsonEdit, len(edits))
	for i, e := range edits {
		js[i] = jsonEdit{Path: e.Path, Value: e.Value, Orig: e.FromOrig}
	}
	eb, err := json.Marshal(js)
	if err != nil {
		return verified{}, err
	}
	files := map[string][]byte{"base.yaml": []byte(src), "new.yaml": []byte(out),
		"edits.json": eb, "orig.yaml": []byte(orig)}
	for name, b := range files {
		if err := ioutil.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return verified{}, nil
		}
	}
	// -I: isolated mode, reads neither the current directory nor the user site -- this step
	// should use nothing but PyYAML.
	cmd := exec.Command(py, "-I", "-c", verifyScript,
		filepath.Join(dir, "base.yaml"), filepath.Join(dir, "new.yaml"),
		filepath.Join(dir, "edits.json"), filepath.Join(dir, "orig.yaml"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// stdin is explicitly wired to /dev/null: the check above already guarantees this is a
	// python, but **no** child process should read from the inherited terminal (the stuck
	// incident of 2026-11-17 was a shell reading stdin, attached to a real terminal). This is
	// the second layer, at zero cost.
	cmd.Stdin = nil
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return verified{}, nil
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		return verified{}, nil
	}
	if err != nil {
		// The parser itself failing to start (PyYAML missing, say) != the file being broken:
		// degrade to this package's read-back check. But the **original file** failing to parse
		// is another matter -- it means it was already broken, and surgery should happen even
		// less.
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "yaml.") && strings.Contains(msg, "Error") {
			return verified{}, i18n.E("hermes' YAML parser rejects the result: {err}",
				i18n.A{"err": lastLine(msg)})
		}
		return verified{}, nil
	}
	if d := strings.TrimSpace(stdout.String()); d != "" {
		return verified{}, i18n.E("hermes' YAML parser disagrees with the planned change at {where}",
			i18n.A{"where": d})
	}
	return verified{Parser: py}, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// hermesPython is hermes' own interpreter: the shebang of the `hermes` entry script.
//
// Lookup order: the real entry (skipping the shim, the same criterion as Agent().FindReal) →
// resolve the link → read the first line. If any step fails, return an empty string (the
// caller degrades, see the file header).
func hermesPython() string { return pythonFor(realHermes()) }

// pythonFor is the same decision in **function form** (entry path in, interpreter out).
//
// Split out to be testable: hermesPython's first criterion is "the real hermes on PATH"
// (Agent().FindReal), and on a development machine with hermes installed that path always hits
// the real entry first -- a fake entry in a test sandbox never gets a turn. The decision itself
// (shebang → interpreter) and "where the entry is found" are two things; split apart, that
// criterion can be pinned directly.
func pythonFor(bin string) string {
	if bin == "" {
		return ""
	}
	f, err := os.Open(bin)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 256)
	n, _ := f.Read(head)
	line := string(head[:n])
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if !strings.HasPrefix(line, "#!") {
		return ""
	}
	fields := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(fields) == 0 {
		return ""
	}
	py := fields[0]
	// `#!/usr/bin/env python3` style: take the name after env and look it up on PATH.
	if filepath.Base(py) == "env" && len(fields) > 1 {
		p, err := exec.LookPath(fields[1])
		if err != nil {
			return ""
		}
		py = p
	}
	// **The shebang must really point at a python.**
	//
	// 2026-11-17 field evidence: hermes' official installer puts a **shell wrapper** at
	// `~/.local/bin/hermes` -- `#!/bin/sh` + `exec …/.hermes/bin/hermes` -- and it is not the
	// first hit on PATH; so this got `/bin/sh`, and what ran next was
	// `/bin/sh -I -c '<the whole python>' base new edits orig` -- `sh -c` treats the last four
	// as positional parameters, the script reads from stdin, and stdin is the inherited real
	// terminal.
	// Symptom: one takeover/sync **hangs** (after the 20s timeout it still waits for that child),
	// the background sync goroutine never reaches its next tick, and "new profiles sync within
	// ten seconds" becomes "never syncs". Silently, because the timeout branch is fail-open.
	//
	// The criterion uses the **name** rather than "run it and see": running it is the very thing
	// to avoid (it really starts a shell). Only something called python / python3 / python3.12
	// counts; both `venv/bin/python3` and `env python3` fall inside this rule.
	base := filepath.Base(py)
	if !strings.HasPrefix(base, "python") {
		return ""
	}
	if info, err := os.Stat(py); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return ""
	}
	return py
}
