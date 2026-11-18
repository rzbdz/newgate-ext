package config

import (
	"os"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// This pair pins the **read** on the Config port (Chains), because it is the only
// data source for "all chains on one screen", and its correctness does not show in
// the picture -- a chain one stop short, a skip category missing, all look like "that
// is how this machine is configured". So the assertions here go item by item through
// those facts **only the read port knows**: the order is the caller's, a missing
// profile is an error, maxAttempts must not cut the chain, one snapshot is one moment.

// chainsFor is the only place allowed to get the port
// this way: what is tested here is that very method.
func chainsFor(t *testing.T, profile string, keys ...string) *Chains {
	t.Helper()
	out, err := (&port{}).Chains(profile, keys...)
	if err != nil {
		t.Fatalf("Chains(%q, %v) failed: %v", profile, keys, err)
	}
	return out
}

// seedProvidersIn **does not open a sandbox twice**: testkit.Sandbox gives a new temporary
// directory on every call (`t.TempDir()` gives a unique path each time), so opening one more
// time inside the same test drops all the files seeded earlier into the previous directory -- the
// symptom is "demo.kv was plainly written but it says there is no such tier". The seedProviders
// in providers_test.go carries its own Sandbox because each of its tests starts from zero.
func seedProvidersIn(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(paths.Root(), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ProvidersFile(), []byte(body), 0o660); err != nil {
		t.Fatal(err)
	}
}

// Layout: a demo first (it is the default, hence the chain head), an alt hung after it.
//
// demo itself binds heavy to ark, other tiers go through the @heavy reference;
// alt adds a light that demo does not have. This way one run shows "the chain
// head is demo", "a cross-profile chain" and "reference expansion" at once.
func seedChains(t *testing.T) {
	t.Helper()
	testkit.Sandbox(t)
	seedProvidersIn(t, `{"providers":{
		"ark":{"base_url":"https://ark.example/v3","api_key":"k"},
		"zhipu":{"base_url":"https://zhipu.example","api_key":"k"},
		"side":{"base_url":"https://side.example","api_key":"k"}
	}}`)
	seedFile(t, "demo.kv", strings.Join([]string{
		"desc=demo base",
		"heavy=ark/deepseek-v3,ark/deepseek-lite",
		"normal=@heavy",
		"",
	}, "\n"))
	seedFile(t, "alt.kv", strings.Join([]string{
		"desc=alt base",
		"prio=50",
		"heavy=zhipu/glm-4",
		"light=zhipu/glm-4-flash",
		"",
	}, "\n"))
	if err := store.SetDefaultProfile("demo", false); err != nil {
		t.Fatalf("setting the default tier failed: %v", err)
	}
}

// An empty profile = the one globally in effect right now, and
// it must **report itself** whether it is the effective one.
//
// This cell is the only source of the green marker for "current config" in the UI (the same thing
// as profileConcept's Note). If the read port did not report it, the UI could only compare against
// the default name in state.json itself -- which again copies the config's knowledge into the UI.
func TestChainsWithoutAProfileMeansTheDefaultOne(t *testing.T) {
	seedChains(t)

	out := chainsFor(t, "", "heavy")
	if out.Profile != "demo" {
		t.Fatalf("with no profile given it should land on the default one (demo), actual %q", out.Profile)
	}
	if !out.Default {
		t.Error("demo is the one in effect right now, so Default should be true")
	}
	if got := out.Keys[0].Steps[0].Binding.String(); got != "ark/deepseek-v3" {
		t.Errorf("the chain head should be ark/deepseek-v3, actual %q", got)
	}

	// Name another one: the chain head follows, and Default
	// must flip to false (it is not the effective one).
	alt := chainsFor(t, "alt", "heavy")
	if alt.Default {
		t.Error("alt is not the one in effect, so Default should be false")
	}
	if got := alt.Keys[0].Steps[0].Binding.String(); got != "zhipu/glm-4" {
		t.Errorf("after naming alt the chain head should be zhipu/glm-4, actual %q", got)
	}
}

// The tier order is **returned as is**: who comes first is a product trade-off
// (`domain.Roles` is most capable first), and the read port does not reorder.
//
// This assertion guards against "sorting alphabetically while at it" -- which
// would turn the tier order in the UI into h/l/m/n, while `newgate status`
// still uses capability order, the same config sorted two ways on two screens.
func TestChainsKeepsTheCallersKeyOrder(t *testing.T) {
	seedChains(t)

	out := chainsFor(t, "alt", "light", "heavy")
	var got []string
	for _, k := range out.Keys {
		got = append(got, k.Key)
	}
	if strings.Join(got, ",") != "light,heavy" {
		t.Errorf("key order should be the caller's light,heavy, actual %v", got)
	}

	// No keys given = follow domain.Roles (the framework's five tiers).
	all := chainsFor(t, "alt")
	if len(all.Keys) != len(domain.Roles) {
		t.Fatalf("with no keys given it should list all %d tiers, actual %d", len(domain.Roles), len(all.Keys))
	}
	for i, k := range all.Keys {
		if k.Key != domain.Roles[i] {
			t.Errorf("tier %d should be %q, actual %q", i, domain.Roles[i], k.Key)
		}
	}
}

// A profile that does not exist is **an error**, not an empty chain.
//
// The shape of an empty chain is "this tier has nothing configured", while the fact
// is "there is no such tier". Quietly and the UI draws an empty card, the reader thinks his
// config was lost -- and not a word of the real cause (a typo, the file deleted) is visible.
func TestChainsRejectsAnUnknownProfile(t *testing.T) {
	seedChains(t)

	_, err := (&port{}).Chains("nope", "heavy")
	if err == nil {
		t.Fatal("asking for a tier that does not exist should error, but it gave an answer")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("the error should carry the name that was asked for, actual %q", err.Error())
	}
}

// The chain is **not truncated**: maxAttempts is the
// execution limit of one request, not the chain's membership.
//
// This pins a real perception bug that was hit (2026-09-17): after `newgate status`
// passed Attempts(), the 4th stop was silently trimmed, and "why is there no ark in
// the normal tier" took half a day -- ark was on the chain all along, just invisible on
// screen. If the read port passed it the same way, the UI would repeat the same sentence.
func TestChainsDoesNotTruncateAtMaxAttempts(t *testing.T) {
	seedChains(t)

	// Squeeze the execution limit down to 1 and read again: the chain must still be the complete ones.
	st := store.LoadState()
	st.Chain.MaxAttempts = 1
	if err := store.SaveState(st); err != nil {
		t.Fatalf("writing state failed: %v", err)
	}

	out := chainsFor(t, "demo", "heavy")
	steps := out.Keys[0].Steps
	if len(steps) < 2 {
		t.Fatalf("heavy should have 2 steps (ark/deepseek-v3, ark/deepseek-lite),"+
			"actual %d steps: maxAttempts must not trim the chain", len(steps))
	}
	for _, s := range out.Keys[0].Skips {
		if s.Kind == resolve.SkipMaxSteps {
			t.Errorf("a skip like %q appeared: reading the port must not truncate by maxAttempts", resolve.SkipMaxSteps)
		}
	}
}

// A reference's chain is **cross-profile**, and Step reports its own origin.
//
// demo's normal is written as `@heavy`, and both expanded stops belong to demo --
// that is a reference; while the chain head on `newgate tier` then continues with
// other profiles (sorted by priority). Both must state the origin: without it
// the user goes looking for a nonexistent candidate in the file he is looking at.
func TestChainsReportsWhereEachStepCameFrom(t *testing.T) {
	seedChains(t)

	out := chainsFor(t, "demo", "normal")
	steps := out.Keys[0].Steps
	if len(steps) == 0 {
		t.Fatal("the normal tier resolved to empty: the @heavy reference was not expanded")
	}
	for i, s := range steps {
		if s.Profile == "" {
			t.Errorf("step %d does not say which profile it came from (%s)", i, s.Binding.String())
		}
	}
	// The reference expands in place: demo's normal and heavy are the same chain.
	heavy := chainsFor(t, "demo", "heavy")
	if len(steps) != len(heavy.Keys[0].Steps) {
		t.Errorf("normal=@heavy expanded to %d steps while heavy itself is %d -- the reference was not expanded in place",
			len(steps), len(heavy.Keys[0].Steps))
	}
}

// An excluded profile appears in Skips rather than vanishing from the screen.
//
// It and Steps are two things: a skipped candidate often is not on the
// chain at all (this one is), so reporting only Steps leaves nowhere to
// say "I plainly configured this one" and "why did it not make the chain".
func TestChainsReportsSkippedProfiles(t *testing.T) {
	seedChains(t)
	seedFile(t, "side.kv", "prio=90\nexcluded=true\nheavy=side/model\n")

	out := chainsFor(t, "demo", "heavy")
	found := false
	for _, s := range out.Keys[0].Skips {
		if s.Profile == "side" && s.Kind == resolve.SkipExcluded {
			found = true
		}
	}
	if !found {
		t.Errorf("the excluded side should appear in Skips (kind=%q), actual %+v",
			resolve.SkipExcluded, out.Keys[0].Skips)
	}
	for _, s := range out.Keys[0].Steps {
		if s.Binding.Provider == "side" {
			t.Error("the excluded side must not appear in the chain")
		}
	}
}

// A broken profile must not take down the whole query
// -- store.Load skips it, the rest proceeds as usual.
//
// This is the "one hand-broken file must not stall every agent" rule as
// it looks on the read port: if the read port passed Load's error straight
// out, the UI would go blank, while the other config on disk is fine.
func TestChainsSurvivesABrokenProfileFile(t *testing.T) {
	seedChains(t)
	// A colon is not `=`: the kv parser rejects it (see the unknown key branch of ParseProfileKV).
	seedFile(t, "broken.kv", "normal: ark/deepseek-v3\n")

	out := chainsFor(t, "demo", "heavy")
	if len(out.Keys[0].Steps) == 0 {
		t.Error("one bad file emptied the whole query: the good tiers should be reported as usual")
	}
}

// The port's **read** and **write** are the same path: what the panel's `Chains` gets must
// be exactly what the command-line `newgate tier` gets (the same snapshot, the same resolve).
//
// This assertion guards against "the panel assembles its own" -- that
// implementation looks right today, and once resolve changes (one
// more Skip kind, different sorting) it quietly forks from the CLI.
func TestChainsAgreesWithTheCommandLine(t *testing.T) {
	seedChains(t)

	snap, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := resolve.BuildChain("heavy", snap.Profiles, snap.Providers,
		resolve.Opts{Active: snap.State.DefaultProfile, MaxSteps: 0})

	got := chainsFor(t, "", "heavy").Keys[0].Steps
	if len(got) != len(want) {
		t.Fatalf("the port gave %d steps, resolve computed %d directly", len(got), len(want))
	}
	for i := range got {
		if got[i].String() != want[i].String() {
			t.Errorf("step %d: port %q, resolve %q", i, got[i].String(), want[i].String())
		}
	}
}

// In the main directory's state, Chain's field names have only one spelling; the
// read port should not give them aliases while at it. (`store.LoadState` fills in
// values when missing, so this only confirms what was written can be read back.)
func TestChainsReadsTheSameSnapshotAsEverythingElse(t *testing.T) {
	seedChains(t)

	if _, err := os.Stat(paths.StateFile()); err != nil {
		t.Fatalf("state.json was not written: %v", err)
	}
	if got := chainsFor(t, "", "heavy").Profile; got != store.LoadState().DefaultProfile {
		t.Errorf("the port says the chain head is %q while state has %q as default -- the two are not reading the same snapshot",
			got, store.LoadState().DefaultProfile)
	}
}

// allChains is the same: what is tested here is that method.
func allChains(t *testing.T, keys ...string) []*Chains {
	t.Helper()
	out, err := (&port{}).AllChains(keys...)
	if err != nil {
		t.Fatalf("AllChains(%v) failed: %v", keys, err)
	}
	return out
}

// The effective one is **first**, the rest by name.
//
// This pins how the "one screen" screen is read: the first thing the eye
// looks for is "which one am I standing on now", and if it were tucked in
// the middle of a dozen profiles by name, that screen would be useless.
func TestAllChainsPutsTheActiveOneFirst(t *testing.T) {
	seedChains(t)
	// One whose name sorts before demo's, to prove the first cell does not come from the name.
	seedFile(t, "aaa.kv", "heavy=ark/deepseek-v3\n")

	got := allChains(t, "heavy")
	if len(got) != 3 {
		t.Fatalf("three tier files should report three, actual %d", len(got))
	}
	if got[0].Profile != "demo" || !got[0].Default {
		t.Errorf("the effective demo should be first and report Default itself, actual first slot is %q (Default=%v)",
			got[0].Profile, got[0].Default)
	}
	if got[1].Profile != "aaa" || got[2].Profile != "alt" {
		t.Errorf("the rest should be sorted by name (aaa, alt), actual %q, %q", got[1].Profile, got[2].Profile)
	}
	for _, c := range got[1:] {
		if c.Default {
			t.Errorf("%q is not the one in effect, it should not report Default", c.Profile)
		}
	}
}

// One call and asking one by one give **the same answer**.
//
// The "one snapshot" property (no two moments on this screen) **cannot be tested** here:
// to see it someone would have to write between two disk reads, and that is concurrency
// timing, not an assertion. So this only pins the half that can be pinned -- every
// card aggregated is **field for field identical** to what asking that one alone gives,
// i.e. the aggregate does not compute its own copy. The remaining property is written in
// AllChains' comment; it is its design reason, not something pretended to be verified here.
func TestAllChainsAgreesWithAskingEachProfile(t *testing.T) {
	seedChains(t)

	for _, c := range allChains(t, "heavy") {
		one := chainsFor(t, c.Profile, "heavy")
		if c.Default != one.Default {
			t.Errorf("%q Default does not match: aggregate %v, asked individually %v", c.Profile, c.Default, one.Default)
		}
		if len(c.Keys) != len(one.Keys) || len(c.Keys[0].Steps) != len(one.Keys[0].Steps) {
			t.Fatalf("%q chain does not match: aggregate %d tiers/%d steps, asked individually %d tiers/%d steps",
				c.Profile, len(c.Keys), len(c.Keys[0].Steps), len(one.Keys), len(one.Keys[0].Steps))
		}
		for i := range c.Keys[0].Steps {
			if c.Keys[0].Steps[i].String() != one.Keys[0].Steps[i].String() {
				t.Errorf("%q step %d: aggregate %q, asked individually %q", c.Profile, i,
					c.Keys[0].Steps[i].String(), one.Keys[0].Steps[i].String())
			}
		}
		// Actions must agree too: they grow from this one's source
		// text, and the aggregate path must not get someone else's.
		if len(c.Keys[0].Actions) != len(one.Keys[0].Actions) {
			t.Errorf("%q action count does not match: aggregate %d, asked individually %d",
				c.Profile, len(c.Keys[0].Actions), len(one.Keys[0].Actions))
		}
	}
}

// Every tier's chain **can be swapped**: actions grow from
// that tier's candidates, and the label is the binding itself.
func TestChainActionsOfferEveryOtherCandidate(t *testing.T) {
	seedChains(t)

	out := chainsFor(t, "demo", "heavy")
	acts := out.Keys[0].Actions
	if len(acts) != 1 {
		t.Fatalf("heavy has two candidates (ark/deepseek-v3, ark/deepseek-lite),"+
			"there should be 1 \"switch to the other one\" button, actual %d", len(acts))
	}
	if got := acts[0].Label(); got != "ark/deepseek-lite" {
		t.Errorf("the button label should be that binding itself, actual %q", got)
	}
	// The first candidate (the chain head right now) gets no button: clicking it changes nothing.
	for _, a := range acts {
		if a.ID == "head:ark/deepseek-v3" {
			t.Error("the first candidate is the current chain head, it should not have a \"switch to it\" button")
		}
	}
	// With only one candidate, or the candidate supplied by another profile, there are no actions.
	alt := chainsFor(t, "alt", "heavy")
	if len(alt.Keys[0].Actions) != 0 {
		t.Errorf("the heavy of alt writes only one candidate, so it should offer no action, actual %d", len(alt.Keys[0].Actions))
	}
	// demo's normal is `@heavy` (a reference): its own line writes only
	// this one, so there is nothing to swap -- ordering by the stops on
	// the chain is wrong, those are written literally under the heavy line.
	normal := chainsFor(t, "demo", "normal")
	if len(normal.Keys[0].Actions) != 0 {
		t.Errorf("normal=@heavy itself has no second candidate, so it should offer no action, actual %d", len(normal.Keys[0].Actions))
	}
}

// Click that button: **the order of that tier on disk
// really changes**, and it changes this file and this tier.
func TestSwitchHeadRewritesTheFile(t *testing.T) {
	seedChains(t)

	out := chainsFor(t, "demo", "heavy")
	acts := out.Keys[0].Actions
	if len(acts) == 0 {
		t.Fatal("nothing to run: the precondition of the case above cannot be met")
	}
	if _, err := acts[0].Run(); err != nil {
		t.Fatalf("switching the chain head failed: %v", err)
	}

	// In the file: ark/deepseek-lite should be first,
	// ark/deepseek-v3 is still there (a reorder, not a replacement).
	raw, err := store.LoadProfileRaw("demo")
	if err != nil {
		t.Fatal(err)
	}
	got := raw.Roles["heavy"]
	if len(got) != 2 {
		t.Fatalf("after reordering there should still be 2 candidates, actual %d: %v", len(got), got)
	}
	if got[0].String() != "ark/deepseek-lite" {
		t.Errorf("the first candidate of heavy in the file should be ark/deepseek-lite, actual %q", got[0].String())
	}
	if got[1].String() != "ark/deepseek-v3" {
		t.Errorf("the one pushed down should still be in the chain (second), actual %q", got[1].String())
	}
	// Resolution follows too: this tier now takes the new chain head.
	if head := chainsFor(t, "demo", "heavy").Keys[0].Steps[0].Binding.String(); head != "ark/deepseek-lite" {
		t.Errorf("after switching, the chain head of heavy should be ark/deepseek-lite, actual %q", head)
	}
	// Other tiers and other files **must not be touched by one byte**.
	if n := len(chainsFor(t, "demo", "normal").Keys[0].Steps); n == 0 {
		t.Error("normal (@heavy) resolved to empty after the head switch -- that tier's file was corrupted")
	}
	if got := chainsFor(t, "alt", "heavy").Keys[0].Steps[0].Binding.String(); got != "zhipu/glm-4" {
		t.Errorf("switching the head of demo must not touch alt; its heavy should still be zhipu/glm-4, actual %q", got)
	}
}

// **Look at the disk** before swapping the chain head: if someone
// else already changed this tier (the candidate the action holds is
// gone), report an error rather than write against a stale snapshot.
//
// This is the "two writers" rule as it looks on an inline row action. By design row
// actions take no parameters (see Action.Run in lib/view), so they cannot get the baseline
// the UI holds, and can only read once before writing -- and that read is the criterion:
// the candidate is gone, meaning the disk is no longer the config this screen saw.
//
// The criterion is **not one byte written**: the file still holds what the other person wrote.
//
// (This pins the first half of "between read and write". The real concurrency window -- the
// moment between finishing the read and writing -- is covered by store.WriteIfUnchanged's
// flock + baseline comparison, which store's own tests already pin; not repeated here.)
func TestSwitchHeadRefusesWhenTheFileMovedOn(t *testing.T) {
	seedChains(t)

	acts := chainsFor(t, "demo", "heavy").Keys[0].Actions
	if len(acts) == 0 {
		t.Skip("does heavy of demo have only one candidate in seedChains? the premise of the case above changed")
	}
	// Someone else (the command line, another tab) changed this tier to another candidate.
	seedFile(t, "demo.kv", "desc=demo base\nheavy=zhipu/glm-4\n")

	_, err := acts[0].Run()
	if err == nil {
		t.Fatal("the candidate is no longer in the file, this time it should error instead of writing anyway")
	}
	raw, err := store.LoadProfileRaw("demo")
	if err != nil {
		t.Fatal(err)
	}
	if got := raw.Roles["heavy"]; len(got) != 1 || got[0].String() != "zhipu/glm-4" {
		t.Errorf("on error not a single byte should be written, actual file content is %v", got)
	}
}
