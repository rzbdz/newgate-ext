package domain

import (
	"sync"
	"testing"
)

// TestExtraRolesSurvivesConcurrentRefresh: reads and
// writes of dynamic role keys must be concurrency-safe.
//
// This is not a hypothetical concurrency: **production really has two goroutines**.
// The writer is the daemon's config watcher (once a second: store.Reload ->
// store.Load -> roleprov.Refresh -> SetExtraRoles), the reader is request goroutines
// (resolve.BuildChain -> CandidatesFor -> DefaultBindingFor -> ExtraRoleOf), and
// every exchange carrying an omo slot key in the request takes that read path.
//
// Before 2026-09-18 extraRoles was one bare assignment, and this would hit DATA RACE.
// The consequence is not just "reads a stale value": a slice header is three machine
// words, and a torn header makes range walk out of bounds, at best judging garbage as
// a known role key into a wrong binding, at worst panicking in a request goroutine.
//
// So this test **must run under -race** (`make test-race`); without -race it is still useful -- it
// pins "the old slice must not be rewritten after the page swap", the premise of lock-free reads.
func TestExtraRolesSurvivesConcurrentRefresh(t *testing.T) {
	defer SetExtraRoles(nil)

	// Each round swaps in a **new** slice (not reusing the same one): the premise of lock-free
	// reads is that the old slice is never rewritten after the swap; if the implementation ever
	// changes to modifying slice contents in place, this test goes red together with -race.
	batch := func(i int) []ExtraRole {
		return []ExtraRole{
			{Key: "omo-sisyphus", Default: "@normal", Source: "omo"},
			{Key: "omo-librarian", Default: "@light", Source: "omo"},
			{Key: "omo-cat-deep" + string(rune('a'+i%26)), Source: "omo"},
		}
	}

	var wg sync.WaitGroup
	const rounds = 2000

	wg.Add(1) // writer: the watcher goroutine
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			SetExtraRoles(batch(i))
		}
	}()

	wg.Add(2) // readers: request goroutines
	for r := 0; r < 2; r++ {
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				// These three are the reads on the hot path.
				_, _ = ExtraRoleOf("omo-sisyphus")
				_, _ = DefaultBindingFor("omo-librarian")
				_ = IsKnownRole("normal")
				_ = ExtraRoles()
			}
		}()
	}
	wg.Wait()

	// After the page swap, what is read must be the complete last copy, not half of one.
	SetExtraRoles(batch(7))
	roles := ExtraRoles()
	if len(roles) != len(builtinAliases)+3 {
		t.Fatalf("the last snapshot should be fully visible, actual %d entries: %v", len(roles), roles)
	}
	if _, ok := ExtraRoleOf("omo-sisyphus"); !ok {
		t.Error("after writing, omo-sisyphus should be visible")
	}
}

// TestExtraRolesCallerCannotMutateTheSnapshot: the slice a reader gets is a
// read-only snapshot; changing it must not affect the next read. This guards
// the other premise of lock-free reads -- no third party will write to it.
func TestExtraRolesCallerCannotMutateTheSnapshot(t *testing.T) {
	defer SetExtraRoles(nil)

	source := []ExtraRole{{Key: "omo-sisyphus", Default: "@normal", Source: "omo"}}
	SetExtraRoles(source)

	// The writer reuses the same slice afterwards (roleprov.Refresh
	// rebuilds every round, but this is the contract)
	source[0].Key = "was corrupted"

	got, ok := ExtraRoleOf("omo-sisyphus")
	if !ok {
		t.Fatal("SetExtraRoles should copy the slice, so the writer changing it later must not affect the stored snapshot")
	}
	if got.Key != "omo-sisyphus" {
		t.Errorf("the snapshot was changed by the writer: %q", got.Key)
	}

	// A reader changing its own copy should not affect the next read either.
	all := ExtraRoles()
	for i := range all {
		all[i].Key = "was corrupted"
	}
	if _, ok := ExtraRoleOf("omo-sisyphus"); !ok {
		t.Error("ExtraRoles should return a copy, so the reader changing it must not affect the snapshot")
	}
}
