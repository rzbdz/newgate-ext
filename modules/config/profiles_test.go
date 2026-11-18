package config

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// **Every action** on a tier file hangs on its own kv card (applyProfileRoles):
// change a tier, change inheritance, rename, delete, create.
//
// Before 2026-09-20 these were scattered over two cards (a "tier files" directory card
// for create/rename/delete, one card per profile for tiers), and the user's words were that the
// directory card was "redundant, because every unit has its own kv" -- so it went, and the actions
// moved onto the card itself. What is pinned below is the correctness after the consolidation.
//
// The most important one is still **delete**: it deletes config on disk, and
// "deleted what someone else just wrote" is the least forgivable kind of error
// in this class of action (the CAS exists for this, see store.RemoveIfUnchanged).

// seedFile writes a tier file to disk and returns its absolute path.
// **Each test opens testkit.Sandbox(t) itself** -- one sandbox is enough;
// this layer does not open another because that would switch NEWGATE_HOME
// to another fresh temporary directory and lose what was written before.
func seedFile(t *testing.T, rel, body string) string {
	t.Helper()
	if err := os.MkdirAll(paths.Mappings(), 0o770); err != nil {
		t.Fatal(err)
	}
	abs := paths.Mappings() + "/" + rel
	if err := os.WriteFile(abs, []byte(body), 0o660); err != nil {
		t.Fatal(err)
	}
	return abs
}

// seedDemo writes a demo tier file in the sandbox (the caller
// opens the sandbox first) and returns its absolute path.
func seedDemo(t *testing.T) string {
	t.Helper()
	testkit.Sandbox(t)
	return seedFile(t, "demo.kv", "desc=demo base\nnormal=p/demo-model\n")
}

// edit runs that card's apply once on some file. An empty base means
// "judge without a baseline" (not used for create / metadata-only edits).
func edit(t *testing.T, abs, body, base string) error {
	t.Helper()
	_, err := applyProfileRoles("config.profile."+strings.TrimSuffix(filepathBase(abs), ".kv"), abs, []byte(body), base)
	return err
}

func filepathBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// TestTheSectionActionCreatesAProfile: the "new" action on the
// section creates an empty tier file, with a name it picks itself.
//
// It replaces a `{"create":"name"}` edit that hung on a card (removed 2026-09-21): the
// newly created one **has no concept yet**, so no card can hang this button -- the action
// belongs to **this section** (see view.Section.Actions). Hanging it on the section has
// another benefit: it is always within reach, no matter which card you are looking at.
//
// The reason the backend picks the name is in this one too: create
// three times in a row, and the three names must all differ -- and
// "differ" is something only someone who can see what is on disk can do.
func TestTheSectionActionCreatesAProfile(t *testing.T) {
	testkit.Sandbox(t)
	seedFile(t, "demo.kv", "desc=demo\nnormal=p/m\n")

	// The action must also hand back **the card it created** (see the
	// return value of view.Action.Run): the UI switches to it based on that,
	// and the name is picked by the backend, so the UI cannot spell it.
	wantFocus := []string{
		"config.profile.new-profile",
		"config.profile.new-profile-2",
		"config.profile.new-profile-3",
	}
	for i := 1; i <= 3; i++ {
		focus, err := newProfileFile()
		if err != nil {
			t.Fatalf("creating %d failed: %v", i, err)
		}
		if focus != wantFocus[i-1] {
			t.Errorf("creating %d should report %q, actual %q", i, wantFocus[i-1], focus)
		}
	}
	want := []string{"demo", "new-profile", "new-profile-2", "new-profile-3"}
	if got := profileNames(); !slices.Equal(got, want) {
		t.Errorf("creating three should produce three profiles with distinct names:\n  want %v\n  got %v", want, got)
	}
	// What is created is a **blank page**: not a copy of demo's
	// content (that would make people think create = clone).
	b, err := os.ReadFile(paths.Mappings() + "/new-profile.kv")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "normal=") {
		t.Errorf("the new profile should be a blank slate, actual:\n%s", b)
	}
}

// TestDeleteRemovesTheFile: delete really removes from disk, and **judges by the baseline
// at load** -- if someone else got ahead it reports an error, rather than deleting the
// new content along with it. This is the only genuinely dangerous action on this card.
func TestDeleteRemovesTheFile(t *testing.T) {
	abs := seedDemo(t)
	if err := edit(t, abs, `{"delete":true}`, store.Revision(abs)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("the file is still there after deletion: %v", err)
	}
}

func TestDeleteWithStaleBaseIsRejected(t *testing.T) {
	abs := seedDemo(t)
	if err := os.WriteFile(abs, []byte("desc=new content, edited by someone else\n"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := edit(t, abs, `{"delete":true}`, "sha256:old"); err == nil {
		t.Fatal("a stale baseline was allowed to delete? that would delete what someone else just wrote")
	}
	if _, serr := os.Stat(abs); serr != nil {
		t.Fatal("a rejected delete must not touch the disk")
	}
}

// TestRenamingMovesTheRolesOver: rename = create a new
// file, delete the old one, **tiers moved over as is**.
func TestRenamingMovesTheRolesOver(t *testing.T) {
	abs := seedDemo(t)
	if err := edit(t, abs, `{"name":"renamed"}`, store.Revision(abs)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Error("the old file was not deleted")
	}
	b, err := os.ReadFile(paths.Mappings() + "/renamed.kv")
	if err != nil {
		t.Fatalf("the new file was not created: %v", err)
	}
	if !strings.Contains(string(b), "normal=p/demo-model") {
		t.Errorf("renaming should move the tier along with it, actual:\n%s", b)
	}
}

// TestExtendsIsWrittenAndCleared: inheritance is written/cleared directly from the UI.
//
// This is the cell the user asked for by name ("these things have an
// inherited one, make the inheritance selectable in the UI"): picking a
// parent tier writes `extends=`, picking back empty removes that line.
func TestExtendsIsWrittenAndCleared(t *testing.T) {
	abs := seedDemo(t)
	seedFile(t, "parent.kv", "normal=p/parent-model\n")

	if err := edit(t, abs, `{"extends":"parent"}`, store.Revision(abs)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(abs)
	if !strings.Contains(string(b), "extends=parent") {
		t.Errorf("the inheritance was not written:\n%s", b)
	}
	// Writing inheritance **should not touch tiers** (roles not submitted = not changed).
	if !strings.Contains(string(b), "normal=p/demo-model") {
		t.Errorf("changing only the inheritance wiped the tiers:\n%s", b)
	}

	if err := edit(t, abs, `{"extends":""}`, store.Revision(abs)); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(abs)
	if strings.Contains(string(b), "extends=") {
		t.Errorf("after clearing the inheritance that line is still there:\n%s", b)
	}
}

// TestRolesStillReplaceWholesale: without extends/name submitted, the behavior
// is exactly as before -- the roles submitted are all of this file's tiers. This
// guards against "added a new action and quietly turned the old one incremental".
func TestRolesStillReplaceWholesale(t *testing.T) {
	abs := seedDemo(t)
	body := `{"roles":{"heavy":[{"provider":"p","model":"big"}],"light":[{"provider":"p","model":"small"}]}}`
	if err := edit(t, abs, body, store.Revision(abs)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(abs)
	s := string(b)
	if strings.Contains(s, "normal=") {
		t.Errorf("the old normal should have been replaced (roles are replaced as a whole):\n%s", s)
	}
	for _, want := range []string{"heavy=p/big", "light=p/small"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s:\n%s", want, s)
		}
	}
}

// TestStaleWriteIsRejected: tier edits are judged by the baseline too -- the only
// line of defense when the UI and the command line change one file at the same time.
func TestStaleWriteIsRejected(t *testing.T) {
	abs := seedDemo(t)
	if err := edit(t, abs, `{"roles":{"normal":[{"provider":"p","model":"m"}]}}`, "sha256:old"); err == nil {
		t.Error("a stale baseline was allowed to write? two changes would silently overwrite each other")
	}
}

// TestProfileFamilyTerminatesAndGroups pins **the return value** and **termination** at once.
//
// The termination part is not superfluous: this passage used to be written as `for
// i := strings.Index(name, "-"); i > 0; i = strings.Index(name[i+1:], "-") + i + 1`, and
// when there is no hyphen in `name[i+1:]`, `Index` returns -1, `-1+i+1 == i`, and the index
// stays put. What triggered it was **the UI's default name for a new tier, `new-profile`**
// (no second hyphen after `new`, and `new` is not an existing tier) -- the consequence being
// `/ui/api/snapshot` looping forever, CPU pegged, the whole UI frozen (measured 2026-09-20).
//
// So it is run with a goroutine + timeout: a regression shows as
// **this code not returning**, not a red assertion -- without a timeout
// the test suite would hang along with it, and nobody could tell where.
func TestProfileFamilyTerminatesAndGroups(t *testing.T) {
	cases := []struct {
		name string
		all  []string
		want string
	}{
		// Has siblings: grouped into that family.
		{"claude-cheap", []string{"claude", "claude-cheap"}, "claude"},
		{"minimax-fast", []string{"minimax", "minimax-fast"}, "minimax"},
		// No sibling: a family of its own. Hyphens are common in tier names (`gpt-4.1-mini`), and
		// grouping by "has a hyphen in the name" would invent a pile of families that do not exist.
		{"gpt-4.1-mini", []string{"gpt-4.1-mini"}, "gpt-4.1-mini"},
		// **The regression one**: the name the UI's "+ new tier" makes.
		{"new-profile", []string{"demo", "new-profile"}, "new-profile"},
		{"new-profile-2", []string{"demo", "new-profile", "new-profile-2"}, "new-profile"},
		// Boundaries: no hyphen, hyphens at the ends, only one segment.
		{"demo", []string{"demo"}, "demo"},
		{"-leading", []string{"-leading"}, "-leading"},
		{"trailing-", []string{"trailing-"}, "trailing-"},
		{"-", []string{"-"}, "-"},
	}
	for _, c := range cases {
		done := make(chan string, 1)
		go func() { done <- profileFamily(c.name, c.all) }()
		select {
		case got := <-done:
			if got != c.want {
				t.Errorf("profileFamily(%q, %v) = %q, want %q", c.name, c.all, got, c.want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("profileFamily(%q, %v) did not return -- it holds the shared snapshot read path everyone uses", c.name, c.all)
		}
	}
}

// findConcept takes one card from this assembly by id.
func findConcept(t *testing.T, id string) view.Concept {
	t.Helper()
	all, err := concepts()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range all {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("there is no %s in this assembly", id)
	return view.Concept{}
}

// TestANameThatIsTakenIsRefusedAsSuch: a name clash must be refused **as the
// fact it is**, not left for the CAS to report as "someone else changed it".
//
// The scene (measured 2026-09-21): renaming aaa to an existing bbb, the UI reported
// "create bbb.kv failed: ... (loaded (absent), on disk sha256:...) (changed by
// someone else after the page opened -- reload and retry)". That sentence is wrong
// here -- nobody changed anything, and reloading and retrying ten thousand times
// gives the same result. The user would follow it, reload, and be stuck there.
//
// Blocked along with it is a more hidden hole: when `.kv` and `.json` coexist, `.kv`
// wins (see store.LoadProfile), so writing an `other.kv` while `other.json` is still there
// amounts to **silently suppressing** that json -- it is fine on disk, but nobody will ever
// read it again. So the criterion is by **name**, not by "the path I am about to write".
func TestANameThatIsTakenIsRefusedAsSuch(t *testing.T) {
	seedDemo(t) // demo.kv
	seedFile(t, "other.json", `{"roles":{}}`)

	abs := paths.Mappings() + "/demo.kv"
	refuse := func(body, why string) {
		t.Helper()
		err := edit(t, abs, body, store.Revision(abs))
		if err == nil {
			t.Errorf("%s: should be rejected", why)
			return
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Errorf("%s: should say \"the name is taken\" based on the facts, instead of leaving it to CAS to report \"someone else changed it\": %v", why, err)
		}
	}
	refuse(`{"name":"other"}`, "renaming to an already-taken name")

	// The reverse must be there too: renaming to its own current
	// name is a no-op, and must not be blocked while at it.
	if err := edit(t, abs, `{"name":"demo"}`, store.Revision(abs)); err != nil {
		t.Errorf("renaming to its own current name should not error: %v", err)
	}
}
