package thinkcache

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// The core value of the on-disk cold tier: after a daemon restart, find back the reasoning the
// previous process accumulated. Opening the same file with a new DiskStore = simulating a
// restart.
func TestDiskStoreSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thinkcache.bin")

	d1, err := openDisk(path, 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	d1.appendKeys([]string{"tool:t1", "text:abc"}, []byte("the real reasoning from this turn"), time.Now())
	if err := d1.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart: a new process opens again, replays the index
	d2, err := openDisk(path, 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if blob, ok := d2.get("tool:t1"); !ok || string(blob) != "the real reasoning from this turn" {
		t.Fatalf("not recovered after restart: ok=%v blob=%q", ok, blob)
	}
}

func TestOriginSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thinkcache.bin")
	want := Origin{Profile: "ark", Provider: "ark", Model: "ark-code-latest"}

	c1 := New(1<<20, time.Hour)
	d1, err := openDisk(path, 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c1.disk = d1
	c1.PutOrigin([]string{"call_restart"}, want)
	if err := d1.Close(); err != nil {
		t.Fatal(err)
	}

	c2 := New(1<<20, time.Hour)
	d2, err := openDisk(path, 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	c2.disk = d2
	if got, ok := c2.GetOrigin("call_restart"); !ok || got != want {
		t.Fatalf("tool origin lost after restart: ok=%v got=%+v", ok, got)
	}
}

// Expired entries are skipped during replay and never enter the index.
func TestDiskStoreDropsExpiredOnReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thinkcache.bin")

	d1, err := openDisk(path, 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	d1.appendKeys([]string{"tool:old"}, []byte("expired content"), time.Now().Add(-2*time.Hour))
	d1.appendKeys([]string{"tool:new"}, []byte("new content"), time.Now())
	_ = d1.Close()

	d2, err := openDisk(path, 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if _, ok := d2.get("tool:old"); ok {
		t.Fatal("an expired entry must not be there after replay")
	}
	if blob, ok := d2.get("tool:new"); !ok || string(blob) != "new content" {
		t.Fatalf("an unexpired entry was lost: ok=%v blob=%q", ok, blob)
	}
}

// Past maxBytes*2 the file is rewritten (rolling): expired entries dropped, live ones kept,
// and the file shrinks back.
func TestDiskStoreRollsOver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thinkcache.bin")

	d, err := openDisk(path, 1024, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := time.Now()
	blob := []byte("a reasoning text neither long nor short, used to push the file past the 2048-byte limit and trigger a rewrite")
	for i := 0; i < 20; i++ {
		at := now
		if i < 10 {
			at = now.Add(-2 * time.Hour) // first 10 expired, last 10 alive
		}
		d.appendKeys([]string{fmt.Sprintf("tool:k%d", i)}, blob, at)
	}
	if d.size > d.max*2 {
		t.Fatalf("the file is still too big after compact: size=%d max*2=%d", d.size, d.max*2)
	}
	if _, ok := d.get("tool:k0"); ok {
		t.Fatal("an expired entry is still there after compact")
	}
	if _, ok := d.get("tool:k19"); !ok {
		t.Fatal("a live entry was lost by compact")
	}
}
