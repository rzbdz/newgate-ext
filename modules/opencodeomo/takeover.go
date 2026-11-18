package opencodeomo

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	opencodeapi "github.com/rzbdz/newgate-ext/modules/opencode"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

const ProviderID = "newgate"

// Report records what one takeover did, for the user to see.
type Report = agentapi.TakeoverReport

// Backup, restore and atomic write **moved into the kernel** on 2026-09-21 (see
// confighook/backup.go): codex's takeover writes the same set of things, and keeping it in this
// module could only mean copying it, and the copy would drift sooner or later -- what drifts is the
// write protection for `original/`, with the symptom "the user's original config is lost for good"
// and not a sound from the tests.
//
// What is left here is only the two **home-grown** pieces of knowledge: which file, and what counts
// as already taken over.

// originalPath / backup / writeAtomic are this file's names for those three kernel items, kept
// short: there are many call sites, and writing agentapi.X would fill this screen with package
// names.
func originalPath(target string) string { return agentapi.OriginalPath(target) }

// backup stores one original. The criterion is "does the content contain the provider key we
// write" -- it is decided by Confighook, and this module only supplies that string.
func backup(target string) error { return agentapi.BackupFile(target, tainted) }

// tainted is opencode's "already taken over" criterion: the file has the `"newgate"` provider key
// (or a `"newgate/…"` pointing at one of its models).
func tainted(b []byte) bool {
	return agentapi.ContainsMarker(b, `"`+ProviderID+`"`) ||
		agentapi.ContainsMarker(b, `"`+ProviderID+`/`)
}

func writeAtomic(path string, b []byte) error { return agentapi.WriteAtomic(path, b, 0o600) }

func marshal(v interface{}) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ---------- opencode.json ----------

// buildNewgateProvider builds the provider block pointing at the local proxy.
// The model names are the semantic tiers -- this is the core of docs/01-product.md: no real model
// names in the config.
//
// extra are the module slot keys (omo-sisyphus / cat-deep …): omo's config file references these
// ids, so they must be registered in the provider's models first, otherwise opencode does not
// recognize the model. Where the keys come from is the module's decision (see omo.go); no
// omo-related judgment is made here.
func buildNewgateProvider(port int, extra []string) map[string]interface{} {
	models := map[string]interface{}{}
	add := func(id string) {
		models[id] = map[string]interface{}{
			"name": "newgate " + id,
			"limit": map[string]interface{}{
				"context": 1000000,
				"output":  64000,
			},
		}
	}
	for _, r := range domain.Roles {
		add(r)
	}
	for _, k := range extra {
		if k != "" && !domain.IsRole(k) {
			add(k)
		}
	}
	return map[string]interface{}{
		"npm":  "@ai-sdk/openai-compatible",
		"name": "newgate (local proxy)",
		"options": map[string]interface{}{
			// **The path must carry `/a/opencode`**: the gateway recognizes "which client this round is
			// from" only from the URL (forward.parseTarget; clients do not let us set headers), and
			// without it, it picks the chain head by the **global default**. claude (`/a/claude`) and
			// codex (`/a/codex/v1`) always carried it; this one was missed -- so the profile picked
			// separately for opencode in the UI was written into state.json yet never asked for. Field
			// test on snode1, 2026-10-29: opencode switched to ds, and requests still took the global
			// default claude's chain. The global default on that machine happened to be ds too, so it
			// never showed.
			//
			// Machines already taken over must re-run `newgate on opencode` (Apply rewrites
			// provider.newgate as a whole block each time, so this cell gets replaced).
			"baseURL": fmt.Sprintf("http://127.0.0.1:%d/a/%s/v1", port, opencodeapi.ID),
			"apiKey":  "newgate-local",
		},
		"models": models,
	}
}

// ApplyOpencode is a merge-style rewrite: only add provider.newgate and point model/small_model at
// it; the user's existing provider blocks, plugins, and any other keys are all kept as is.
func ApplyOpencode(target string, port int, extra []string) (*Report, error) {
	rep := &Report{File: target}
	raw, err := ioutil.ReadFile(target)
	if err != nil {
		return rep, err
	}
	if err := backup(target); err != nil {
		return rep, err
	}

	// Use a map to keep unknown keys
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return rep, i18n.Ef(err, "cannot parse {file}: {err}", i18n.A{"file": target})
	}

	provs := map[string]json.RawMessage{}
	if v, ok := root["provider"]; ok {
		if err := json.Unmarshal(v, &provs); err != nil {
			return rep, i18n.Ef(err, "the provider field is not an object: {err}", nil)
		}
	}
	np, err := marshal(buildNewgateProvider(port, extra))
	if err != nil {
		return rep, err
	}
	provs[ProviderID] = json.RawMessage(np)
	if b, err := marshal(provs); err == nil {
		root["provider"] = json.RawMessage(b)
	} else {
		return rep, err
	}

	// The top-level model / small_model point at the semantic tiers
	root["model"] = json.RawMessage(`"` + ProviderID + `/normal"`)
	root["small_model"] = json.RawMessage(`"` + ProviderID + `/light"`)
	rep.Rewrites = append(rep.Rewrites,
		"model -> "+ProviderID+"/normal",
		"small_model -> "+ProviderID+"/light",
		i18n.T("provider.{id} injected (all pre-existing providers kept)",
			i18n.A{"id": ProviderID}))

	out, err := marshal(root)
	if err != nil {
		return rep, err
	}
	return rep, writeAtomic(target, out)
}

// ---------- oh-my-openagent.json ----------

// ApplyOpenagent wires omo's intra-agent slots onto newgate.
//
// The difference from the old version: **slot identity is kept**. Every `"model": "vendor/model"`
// used to be replaced with a build-derived `newgate/normal`, and sisyphus and librarian looked
// identical from then on; now it is replaced with a slot-derived key `newgate/omo-sisyphus` /
// `newgate/cat-deep`, and the build (which tier it currently counts as, which tier is suggested) is
// recorded in omo-slots.json in the same directory.
//
// Three things, none of which rewrites anything the user cannot see:
//
//	slot key: agents.<name> → omo-<name>, categories.<name> → cat-<name> (omo.go)
//	registry: key → current tier / suggested tier / original model name, written to omo-slots.json
//	chain ownership: default in the registry is this key's default when the profile writes none
//
// `fallback_models` converges to one entry (the same key): the chain is already covered by
// newgate on the server side, and multiple fallbacks left in the config only make one failure retry
// the same thing. The original list is recorded in the registry (was_fallbacks), and the original
// file is still in backups/original/.
//
// Idempotent: a slot already at newgate/xxx is not translated again; re-takeover recovers "what
// model it originally was" from the registry and the original file in the backup, so repeated
// takeovers lose no information.
func ApplyOpenagent(target string, port int) (*Report, error) {
	rep := &Report{File: target}
	raw, err := ioutil.ReadFile(target)
	if err != nil {
		return rep, err
	}
	if err := backup(target); err != nil {
		return rep, err
	}

	var root map[string]interface{}
	if err := json.Unmarshal(raw, &root); err != nil {
		return rep, i18n.Ef(err, "cannot parse {file}: {err}", i18n.A{"file": target})
	}

	slots, err := DiscoverSlots(target)
	if err != nil {
		return rep, err
	}
	prev := ReadOmoSlots()
	orig := map[string]Slot{}
	for _, s := range OriginalSlots() {
		orig[s.Kind+"/"+s.Name] = s
	}

	reg := &OmoSlots{}
	if prev != nil {
		reg.Mode = prev.Mode
		reg.Overrides = prev.Overrides
	}
	byKey := map[string]OmoSlot{}
	for _, sl := range slots {
		key := sl.Key()
		origModel, origVariant := "", ""
		if o, ok := orig[sl.Kind+"/"+sl.Name]; ok {
			origModel, origVariant = o.Model, o.Variant
		}

		// was = the real model name before takeover. Three sources, ordered by trustworthiness: the
		// original file in the backup (never touched by us) > what the last takeover recorded > the
		// current file (it held the real name only on the first takeover; after that it is our
		// newgate/xxx).
		was := ""
		if origModel != "" && !isTakenOver(origModel) {
			was = origModel
		} else {
			if p, ok := prev.SlotOf(key); ok {
				was = p.Was
			}
			if was == "" && !isTakenOver(sl.Model) {
				was = sl.Model
			}
		}

		variant := firstNonEmpty(sl.Variant, origVariant)
		current, note := currentTier(sl.Model, key, prev, was)
		suggested, swhy := Suggest(was, variant)
		entry := OmoSlot{
			Key: key, Kind: sl.Kind, Name: sl.Name,
			Was: was, WasFallbacks: sl.Fallbacks, Variant: variant,
			Current: current, Suggested: suggested, Default: current,
			Why: joinWhy(note, swhy),
		}
		reg.Slots = append(reg.Slots, entry)
		byKey[key] = entry
		rep.Rewrites = append(rep.Rewrites, i18n.T("{section}.{name} → {provider}/{key} (now {now}{suggested})",
			i18n.A{"section": sl.Kind + "s", "name": sl.Name, "provider": ProviderID,
				"key": key, "now": current, "suggested": suggestTag(suggested, current)}))
	}
	// default in the registry follows overrides/mode (whoever writes the file sees the final binding)
	for i := range reg.Slots {
		reg.Slots[i].Default = reg.SlotBinding(reg.Slots[i].Key)
		if reg.Slots[i].Default == "" {
			reg.Slots[i].Default = reg.Slots[i].Current
		}
		byKey[reg.Slots[i].Key] = reg.Slots[i]
	}
	if err := WriteOmoSlots(reg); err != nil {
		return rep, i18n.Ef(err, "cannot write the slot registry: {err}", nil)
	}
	rep.Rewrites = append(rep.Rewrites,
		i18n.N("slot registry → {file} ({n} key; default decides which tier each key follows)",
			"slot registry → {file} ({n} keys; default decides which tier each key follows)",
			len(reg.Slots), i18n.A{"file": SlotsFile(), "n": len(reg.Slots)}))

	// Rewrite: touch only agents.*.model / categories.*.model and their fallback_models
	fallbackIsObject := map[string]bool{}
	for _, s := range slots {
		fallbackIsObject[s.Key()] = s.FallbackObjects
	}
	for kind, plural := range map[string]string{"agent": "agents", "category": "categories"} {
		section, ok := root[plural].(map[string]interface{})
		if !ok {
			continue
		}
		for name, rawNode := range section {
			node, ok := rawNode.(map[string]interface{})
			if !ok {
				continue
			}
			key, ok := byKey[slotKey(kind, name)]
			if !ok {
				continue // skipped by DiscoverSlots (no model field)
			}
			node["model"] = ProviderID + "/" + key.Key
			if _, has := node["fallback_models"]; has {
				// Keep the element shape as is (object or string), only converge the content onto the same
				// key.
				if fallbackIsObject[key.Key] {
					node["fallback_models"] = []interface{}{
						map[string]interface{}{"model": ProviderID + "/" + key.Key}}
				} else {
					node["fallback_models"] = []interface{}{ProviderID + "/" + key.Key}
				}
			}
		}
	}

	out, err := marshal(root)
	if err != nil {
		return rep, err
	}
	return rep, writeAtomic(target, out)
}

// currentTier "current" = which tier this slot lands on when the suggestion is not enabled -- i.e.
// the old version's behavior.
//
//	newgate/<tier>  a file taken over by an old version: keep as is, the user's current behavior
//	                unchanged
//	newgate/<key>   taken over by the new version: reuse the current recorded in the registry
//	the rest (real names)  first takeover: classify by build; a fuzzy hit must state its provenance
func currentTier(model, key string, prev *OmoSlots, was string) (tier, note string) {
	if strings.HasPrefix(model, ProviderID+"/") {
		rest := strings.TrimPrefix(model, ProviderID+"/")
		if domain.IsRole(rest) {
			return rest, i18n.T("keeping the tier an older takeover wrote", nil)
		}
		if p, ok := prev.SlotOf(key); ok && p.Current != "" {
			return p.Current, ""
		}
	}
	name := firstNonEmpty(was, model)
	t, exact := ClassifyModel(name)
	if !exact {
		return t, i18n.T("model name {model} matched no rule; falling back to mid",
			i18n.A{"model": name})
	}
	return t, ""
}

func slotKey(kind, name string) string {
	if kind == "category" {
		return OmoCatPrefix + name
	}
	return OmoAgentPrefix + name
}

func isTakenOver(model string) bool {
	return strings.HasPrefix(model, ProviderID+"/") || strings.HasPrefix(model, "@")
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func joinWhy(note, why string) string {
	switch {
	case note == "":
		return why
	case why == "":
		return note
	default:
		return i18n.T("{note}; {why}", i18n.A{"note": note, "why": why})
	}
}

func suggestTag(suggested, current string) string {
	if suggested == "" || suggested == current {
		return ""
	}
	return i18n.T(", {tier} suggested", i18n.A{"tier": suggested})
}

// ---------- orchestration ----------

func IsTakenOver(target string) bool {
	b, err := ioutil.ReadFile(target)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), `"`+ProviderID+`/`) ||
		strings.Contains(string(b), `"`+ProviderID+`"`)
}

// ApplyAll takes over all target files. Files not found are skipped and recorded, not treated as
// errors.
func ApplyAll(port int) ([]*Report, error) {
	// Discover the module slot keys first (omo's intra-agent). opencode.json's provider block must
	// register these ids first, so that referencing them while editing the omo config is recognized.
	// Which keys, and what they are called, is the module's decision (omo.go); this only lines the two
	// sides up.
	var reps []*Report
	var extra []string
	if t := omoTargetPath(); t != "" {
		slots, err := DiscoverSlots(t)
		if err != nil {
			// Say it out loud when discovery fails; do not let takeover "succeed" having changed nothing
			reps = append(reps, &Report{File: t, Skipped: err.Error()})
		}
		for _, s := range slots {
			extra = append(extra, s.Key())
		}
	}
	if reg := ReadOmoSlots(); reg != nil {
		for _, s := range reg.Slots {
			extra = append(extra, s.Key)
		}
	}

	for _, t := range TargetFiles() {
		if _, err := os.Stat(t); os.IsNotExist(err) {
			reps = append(reps, &Report{File: t, Skipped: i18n.T("the file does not exist", nil)})
			continue
		}
		var rep *Report
		var err error
		if strings.Contains(filepath.Base(t), "openagent") {
			rep, err = ApplyOpenagent(t, port)
		} else {
			rep, err = ApplyOpencode(t, port, extra)
		}
		if err != nil {
			return reps, i18n.Ef(err, "taking over {file} failed: {err}", i18n.A{"file": t})
		}
		reps = append(reps, rep)
	}
	return reps, nil
}

// RestoreAll restores from original/. This is the escape hatch: after restoring, deleting newgate
// still leaves a working setup.
func RestoreAll() ([]string, error) {
	var done []string
	for _, t := range TargetFiles() {
		orig := originalPath(t)
		b, err := ioutil.ReadFile(orig)
		if err != nil {
			continue // no backup means never taken over
		}
		if err := writeAtomic(t, b); err != nil {
			return done, i18n.Ef(err, "restoring {file} failed: {err}", i18n.A{"file": t})
		}
		done = append(done, t)
	}
	// Slot keys lapse with the release; the user's hand-written overrides stay. Say it out loud when
	// they cannot be cleared -- the keys left behind are treated as the user's choice on the next
	// takeover (see the note on ClearOmoSlots).
	if err := ClearOmoSlots(); err != nil {
		return done, i18n.Ef(err, "restore finished, but the slot table was not cleared (the next takeover will reuse the old assignment): {err}", nil)
	}
	return done, nil
}
