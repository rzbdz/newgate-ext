package proto

import (
	paths "github.com/rzbdz/newgate-ext/modules/config/paths"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// applyFiles "writes and records" a snapshot, simulating one successful sync.
func applyFiles(t *testing.T, files map[string][]byte) map[string]string {
	t.Helper()
	res, err := Materialize(paths.Config(), files, nil)
	if err != nil {
		t.Fatalf("writing to disk failed: %v", err)
	}
	return res.Manifest
}

func TestDetectDriftClean(t *testing.T) {
	sandbox(t)
	files := snapshotFiles()
	applied := applyFiles(t, files)

	rep := DetectDrift(paths.Config(), applied, files)
	if !rep.Empty() {
		t.Fatalf("right after a sync it should be clean, got %+v", rep)
	}
	if rep.Blocking() {
		t.Fatal("a clean state must not block")
	}
}

func TestDetectDriftModifiedBlocks(t *testing.T) {
	sandbox(t)
	files := snapshotFiles()
	applied := applyFiles(t, files)

	// Someone hand-edited providers.json while the remote has other content - writing wipes it.
	writeLive(t, ProvidersName, []byte(`{"providers":{"mine":{"base_url":"http://localhost"}}}`))
	incoming := snapshotFiles()
	incoming[ProvidersName] = []byte(`{"providers":{"remote":{"base_url":"https://x"}}}`)

	rep := DetectDrift(paths.Config(), applied, incoming)
	if len(rep.Modified) != 1 || rep.Modified[0] != ProvidersName {
		t.Fatalf("should report exactly that providers.json was modified, got %+v", rep)
	}
	if !rep.Blocking() {
		t.Fatal("a modified managed file must block - otherwise the next round silently overwrites it")
	}
	if got := rep.Paths(); len(got) != 1 || got[0] != ProvidersName {
		t.Fatalf("Paths() should return the one to archive, got %v", got)
	}
}

// TestDetectDriftConvergedIsNotDrift is an **edge case**; the criterion here is "would
// materialize actually change anything", not "was the file touched".
//
// The user's hand-edited content happens to equal the remote's current version: writing
// destroys nothing (their version now agrees with the authority). Blocking would only
// force a pointless --force for "accepting a version that is already identical".
func TestDetectDriftConvergedIsNotDrift(t *testing.T) {
	sandbox(t)
	files := snapshotFiles()
	applied := applyFiles(t, files)

	// The user changed it to B first...
	converged := []byte(`{"providers":{"same-as-remote":{"base_url":"https://same"}}}`)
	writeLive(t, ProvidersName, converged)
	// ...and the remote happens to have B this round too.
	incoming := snapshotFiles()
	incoming[ProvidersName] = converged

	rep := DetectDrift(paths.Config(), applied, incoming)
	if rep.Blocking() {
		t.Fatalf("when the hand edit matches the remote it must not block "+
			"(writing it changes nothing), got %+v", rep)
	}
}

func TestDetectDriftDeleted(t *testing.T) {
	sandbox(t)
	files := snapshotFiles()
	applied := applyFiles(t, files)

	// Delete a tier we wrote.
	if err := os.Remove(filepath.Join(paths.Config(), "mappings", "prod.json")); err != nil {
		t.Fatalf("deleting failed: %v", err)
	}

	t.Run("the remote still has it: block (otherwise it comes back)", func(t *testing.T) {
		rep := DetectDrift(paths.Config(), applied, snapshotFiles())
		if len(rep.Deleted) != 1 || rep.Deleted[0] != "mappings/prod.json" {
			t.Fatalf("the deleted file should be reported, got %+v", rep)
		}
		if !rep.Blocking() {
			t.Fatal("while the remote still has it this must block: materialize would write it " +
				"back and undo the user's deletion")
		}
	})
	t.Run("the remote dropped it too: converged, no block", func(t *testing.T) {
		incoming := snapshotFiles()
		delete(incoming, "mappings/prod.json")
		rep := DetectDrift(paths.Config(), applied, incoming)
		if rep.Blocking() {
			t.Fatalf("the remote retired it too, both sides agree, must not block, got %+v", rep)
		}
	})
}

// TestDetectDriftLocalLayerIsReportedNotBlocking is where "two-layer config" lands in
// the drift guard: the local layer (machine-private tier files) must **report, not block**.
//
// Otherwise "local customization" and "auto sync" cannot coexist - users would give up
// one. Reporting is mandatory: not reporting is the silent-override trap (the remote
// synced, but local behavior comes from another file, with no signal at all).
func TestDetectDriftLocalLayerIsReportedNotBlocking(t *testing.T) {
	sandbox(t)
	files := snapshotFiles()
	applied := applyFiles(t, files)

	writeLive(t, "mappings/local-only.json", []byte(`{"name":"local-only","roles":{}}`))

	rep := DetectDrift(paths.Config(), applied, snapshotFiles())
	if rep.Blocking() {
		t.Fatalf("the local layer must not block sync, got %+v", rep)
	}
	if len(rep.Extra) != 1 || rep.Extra[0] != "mappings/local-only.json" {
		t.Fatalf("the local layer must be reported, got %+v", rep)
	}
	if rep.Empty() {
		t.Fatal("with a local layer it is not Empty - status must show it")
	}
}

// TestDetectDriftShadowed is the nastiest: store.readProfileFile checks .kv before .json,
// so a local prod.kv makes the managed prod.json **take no effect even when written**.
// Remote synced, local behavior unchanged, no error - must count as blocking drift.
func TestDetectDriftShadowed(t *testing.T) {
	sandbox(t)
	// The snapshot has only the .json form.
	files := map[string][]byte{
		ProvidersName:        validProviders(),
		"mappings/prod.json": validProfile(),
	}
	applied := applyFiles(t, files)

	// Someone on this machine wrote a .kv with the same name.
	writeLive(t, "mappings/prod.kv", []byte("normal = smt-deepseek/deepseek-chat\n"))

	rep := DetectDrift(paths.Config(), applied, files)
	if len(rep.Shadowed) != 1 || rep.Shadowed[0] != "mappings/prod.json" {
		t.Fatalf("should report the managed file shadowed by a local .kv, got %+v", rep)
	}
	if !rep.Blocking() {
		t.Fatal("a shadowed managed file must block: writing it has no effect, " +
			"which would be a lie that the sync succeeded")
	}
}

func TestDetectDriftShadowIgnoredWhenKVIsAlsoOurs(t *testing.T) {
	sandbox(t)
	// Both forms are host-managed: priority is the host's call, not drift.
	files := map[string][]byte{
		"mappings/prod.json": validProfile(),
		"mappings/prod.kv":   []byte("normal = smt-deepseek/deepseek-chat\n"),
	}
	applied := applyFiles(t, files)

	rep := DetectDrift(paths.Config(), applied, files)
	if len(rep.Shadowed) != 0 {
		t.Fatalf("managed on both sides is not drift, got %+v", rep)
	}
	if rep.Blocking() {
		t.Fatalf("must not block, got %+v", rep)
	}
}

func TestArchiveFiles(t *testing.T) {
	sandbox(t)
	writeLive(t, ProvidersName, []byte("local version"))
	writeLive(t, "mappings/prod.json", []byte("local tier"))

	dir, err := ArchiveFiles(paths.Config(), "20260917-214512",
		[]string{ProvidersName, "mappings/prod.json", "mappings/gone.json"})
	if err != nil {
		t.Fatalf("archiving failed: %v", err)
	}
	if !strings.HasSuffix(dir, filepath.Join("backups", "configshare-20260917-214512")) {
		t.Fatalf("wrong archive directory name: %s", dir)
	}
	// Content must stay verbatim (--force overwrites user edits, but must not make them
	// unrecoverable - archiving is why this command is safe to run).
	got, err := os.ReadFile(filepath.Join(dir, ProvidersName))
	if err != nil || string(got) != "local version" {
		t.Fatalf("wrong archived providers.json content: %q / %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(dir, "mappings", "prod.json"))
	if err != nil || string(got) != "local tier" {
		t.Fatalf("the archive should keep the relative structure: %q / %v", got, err)
	}
	// Already-deleted files are skipped (nothing to save), and that is not an error.
	if _, err := os.Stat(filepath.Join(dir, "mappings", "gone.json")); !os.IsNotExist(err) {
		t.Fatal("a nonexistent file must not be archived")
	}
}

func TestArchiveFilesEmpty(t *testing.T) {
	sandbox(t)
	dir, err := ArchiveFiles(paths.Config(), "20260917-214512", nil)
	if err != nil || dir != "" {
		t.Fatalf("with nothing to archive it should give an empty directory name "+
			"and no error, got %q / %v", dir, err)
	}
}
