package hermes

import (
	"bytes"
	"io/ioutil"
	"os"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate/lib/i18n"
)

// This file is **hermes' takeover**: rewriting `$HERMES_HOME/config.yaml` to point at the local
// proxy.
//
// The same mechanism as codex (MechConfig): hermes' models and providers honor only that YAML,
// with no environment-variable entry. What to write is in plan.go; how to write it (byte
// surgery + an agreement check by hermes' own parser) is in yamledit.go and verify.go. Here it
// is only the order:
//
//	read the original → compute the edits → surgery → check → back up → read once more
//	before writing (nobody else changed it) → atomic write

// Takeover is the takeover handed to confighook. No fields: every value needed is computed at
// the moment of Apply (same reason as codex's Takeover).
type Takeover struct{}

var _ agentapi.ConfigTakeover = Takeover{}

func (Takeover) Targets() []string { return []string{ConfigPath()} }

// IsTakenOver's criterion is whether the `providers.newgate` line exists, not "newgate appears
// somewhere in the file": the latter would be hit by a name the user chose themselves (same
// reason as codex's IsTakenOver).
func (Takeover) IsTakenOver(target string) bool {
	b, err := ioutil.ReadFile(target)
	if err != nil {
		return false
	}
	return hasMarker(b)
}

func hasMarker(b []byte) bool {
	_, ok := parseDoc(string(b)).lookup("providers", ProviderID)
	return ok
}

// Apply performs one complete takeover. When the file is missing it is **not created** (hermes
// writes one itself on first run; missing = hermes never ran on this machine, and that is
// something to say out loud).
func (Takeover) Apply(port int) ([]*agentapi.TakeoverReport, error) {
	if _, err := os.Stat(ConfigPath()); err != nil {
		return []*agentapi.TakeoverReport{(&result{File: ConfigPath(), Missing: true}).report()}, nil
	}
	in, err := currentInputs(port)
	if err != nil {
		return nil, err
	}
	rep, err := rewriteFile(ConfigPath(), true, func(d *doc) ([]edit, catalog) { return fullEdits(d, in) })
	if rep != nil && err == nil && !rep.Missing {
		noteApply(rep)
	}
	return []*agentapi.TakeoverReport{rep.report()}, err
}

// Restore puts every place takeover touched back the way it was **in the original**, leaving
// the rest alone.
//
// # Why not swap the whole file back (codex does)
//
// hermes writes this file itself: `/model … --global`, `hermes config set`, `hermes setup` and
// version migrations all land on disk. After takeover those changes belong to the user; swapping
// the whole file back would discard everything he changed in hermes during the takeover -- and
// `newgate off hermes` only promises "take newgate out". So restore by path: the places takeover
// wrote take their values from the original (removed if absent there), our `providers.newgate*`
// is removed, and not one other byte is touched. It goes through hermes' parser check too.
//
// No original (never taken over) = nothing happens, not an error.
func (Takeover) Restore() ([]string, error) {
	target := ConfigPath()
	orig, err := ioutil.ReadFile(agentapi.OriginalPath(target))
	if err != nil {
		return nil, nil
	}
	cur, err := ioutil.ReadFile(target)
	if err != nil {
		// The file is gone (the user deleted hermes' config): the original is the only copy we
		// have, so put it back as is.
		done, err := agentapi.RestoreFile(target)
		if err != nil || !done {
			return nil, err
		}
		return []string{target}, nil
	}
	od := parseDoc(string(orig))
	d := parseDoc(string(cur))
	var edits []edit
	for _, p := range takenOverPaths(d, od) {
		edits = append(edits, edit{Path: p, FromOrig: true})
		if lines, ind, ok := od.raw(p...); ok {
			if err := d.putRaw(lines, ind, p...); err != nil {
				return nil, restoreErr(target, err)
			}
		} else {
			d.remove(p...)
		}
	}
	out := d.String()
	if out == string(cur) {
		return nil, nil
	}
	if _, err := verifyEdits(string(cur), out, edits, string(orig)); err != nil {
		return nil, restoreErr(target, err)
	}
	if err := writeIfUnchanged(target, cur, []byte(out)); err != nil {
		return nil, err
	}
	return []string{target}, nil
}

func restoreErr(target string, err error) error {
	return i18n.Ef(err, "cannot restore {file} key by key ({err}); the untouched original is at {orig}",
		i18n.A{"file": target, "err": err, "orig": agentapi.OriginalPath(target)})
}

// result is the account of one rewrite: which places changed, who checked it, and what could
// not be listed.
type result struct {
	File    string
	Changes []string
	Parser  string
	Skipped []string
	// Missing the file is absent (hermes never ran).
	Missing bool
	// Paths the paths that really changed (used to decide whether the gateway needs a restart,
	// see gateway.go).
	Paths [][]string
}

func (r *result) report() *agentapi.TakeoverReport {
	if r == nil {
		return &agentapi.TakeoverReport{File: ConfigPath()}
	}
	rep := &agentapi.TakeoverReport{File: r.File, Rewrites: r.Changes}
	if r.Missing {
		rep.Skipped = i18n.T("no such file — run hermes once so it writes its config, then take over again", nil)
	}
	if r.Parser == "" && len(r.Changes) > 0 {
		rep.Rewrites = append(rep.Rewrites, i18n.T("(not cross-checked by hermes' YAML parser: its interpreter was not found)", nil))
	}
	for _, s := range r.Skipped {
		rep.Rewrites = append(rep.Rewrites, i18n.T("not listed in /model: {what}", i18n.A{"what": s}))
	}
	return rep
}

// rewriteFile is the write path shared by Apply and background sync. backup = whether to keep a
// timestamped snapshot this time (full takeover: yes; background sync: no -- it may run many
// times a day and would push out the useful snapshots, and it touches only our own lines).
func rewriteFile(target string, backup bool, plan func(*doc) ([]edit, catalog)) (*result, error) {
	res := &result{File: target}
	raw, err := ioutil.ReadFile(target)
	if err != nil {
		res.Missing = true
		return res, nil
	}
	d := parseDoc(string(raw))
	edits, c := plan(d)
	res.Skipped = c.Skipped
	var applied []edit
	for _, e := range edits {
		changed, err := d.put(e)
		if err != nil {
			return res, i18n.Ef(err, "{file} has a shape newgate will not edit by hand ({err}); nothing was written",
				i18n.A{"file": target, "err": err})
		}
		if changed {
			applied = append(applied, e)
			res.Paths = append(res.Paths, e.Path)
			res.Changes = append(res.Changes, describe(e))
		}
	}
	if len(applied) == 0 {
		return res, nil
	}
	out := d.String()
	// What is checked is **all** edits, not only the changed ones: the unchanged places must
	// also hold the intended values in the result.
	v, err := verifyEdits(string(raw), out, edits, "")
	if err != nil {
		return res, i18n.Ef(err, "refusing to write {file}: {err}", i18n.A{"file": target, "err": err})
	}
	res.Parser = v.Parser
	if backup {
		if err := agentapi.BackupFile(target, hasMarker); err != nil {
			return res, err
		}
	}
	return res, writeIfUnchanged(target, raw, []byte(out))
}

// writeIfUnchanged reads once more before writing: if hermes wrote this file itself in the
// meantime (`/model --global` will), **do not write** -- our edits were computed against the old
// content, and writing would overwrite what it just wrote. Report a line and come back later
// (background sync retries by itself; on the CLI, run the command again).
func writeIfUnchanged(target string, base, out []byte) error {
	now, err := ioutil.ReadFile(target)
	if err != nil || !bytes.Equal(now, base) {
		return i18n.E("{file} changed while newgate was editing it; nothing was written — try again",
			i18n.A{"file": target})
	}
	return agentapi.WriteAtomic(target, out, 0o600)
}

// describe one edit written as a line for the user (secrets never appear in the value: we only
// write the placeholder).
func describe(e edit) string {
	p := strings.Join(e.Path, ".")
	switch v := e.Value.(type) {
	case nil:
		return i18n.T("{path} removed", i18n.A{"path": p})
	case omap:
		return i18n.T("{path} written", i18n.A{"path": p})
	default:
		s, _ := scalarLit(v)
		return p + " -> " + s
	}
}

// currentInputs asks for all the facts to write at this moment.
//
// When newgate's own config cannot be read, **return an error, not an empty list**: edits
// computed from an empty list mean "delete the line of every profile" -- one typo corrupting
// providers.json and background sync wipes out every model selectable in hermes. Unreadable =
// do nothing this round.
func currentInputs(port int) (inputs, error) {
	in := inputs{Port: port, Tiers: map[string]string{}}
	for _, s := range Agent().Slots {
		in.Tiers[s.Name] = tierFor(s.Name)
	}
	snap, err := store.Load()
	if err != nil || snap == nil {
		return in, i18n.Ef(err, "cannot read the newgate config ({err}); hermes' config.yaml was left alone",
			i18n.A{"err": err})
	}
	in.Active = snap.State.ActiveFor(ID)
	in.Profiles = append([]*domain.Profile(nil), snap.Profiles...)
	// store.Load skips unreadable profiles (one bad one does not block the rest). To us
	// "unreadable" != "absent": leave that line alone and sync once it is fixed (see
	// catalogEdits).
	if names, err := store.ListProfiles(); err == nil {
		loaded := map[string]bool{}
		for _, p := range snap.Profiles {
			loaded[p.Name] = true
		}
		for _, n := range names {
			if !loaded[n] {
				in.Unreadable = append(in.Unreadable, n)
			}
		}
	}
	return in, nil
}

// syncCatalog syncs the /model list once (shared by background, command line and the card).
func syncCatalog() (*result, error) {
	in, err := currentInputs(proxyPort())
	if err != nil {
		return nil, err
	}
	return rewriteFile(ConfigPath(), false, func(d *doc) ([]edit, catalog) { return catalogEdits(d, in) })
}

// proxyPort is the port takeover writes into base_url, from the same source as the command line
// (same reason as codex's proxyPort).
func proxyPort() int {
	if st := store.LoadState(); st != nil && st.Port != 0 {
		return st.Port
	}
	return domain.ProxyPort
}
