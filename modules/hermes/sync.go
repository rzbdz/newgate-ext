package hermes

import (
	"io/ioutil"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rzbdz/newgate/lib/i18n"
)

// This file is the **background sync of the model list**: when newgate's profiles change (one
// added, one binding edited, one deleted), our `providers.newgate*` lines in config.yaml are
// rewritten to their current shape.
//
// # Why it is automatic, and why only this one thing
//
// The first usage ("follow newgate": default model = a tier name) needs no sync at all: the
// tier name does not change, and which model it lands on is decided by newgate at request
// time. Only the second usage needs syncing -- the **concrete model list** shown in `/model`
// is a snapshot written into the file, stale the moment a profile changes. The symptom of
// staleness is "the model you can pick in hermes is long gone from newgate" (picking it: 404)
// or "a model newgate just added is not selectable in hermes". The user should not have to
// remember to do this by hand.
//
// But sync **touches only those lines**, not the slot cells (model.default, delegation,
// auxiliary): those were written at takeover time and may since have been changed by hand in
// hermes (`/model … --global` writes model.default and model.provider). Rewriting them in the
// background amounts to undoing the choice the user just made. Slot rewrites happen only when
// the user asks explicitly (`newgate on hermes`, saving on the card).
//
// # Notification
//
// hermes' gateway re-reads this file by mtime, so the next message after a sync sees the new
// list -- **no restart** (see the header of gateway.go). The sync itself leaves a record (the
// hermes cell in state.json + one log line), visible in `newgate status` and on the card. The
// one case that really does need a restart is judged by gateway.go, which likewise only warns
// and never restarts on the user's behalf.

// syncInterval how often to look. Profile changes are made by hand, and a ten-second delay is
// noticed by nobody; and each look re-reads every profile + this YAML (tens of KB), so looking
// more often has no point.
const syncInterval = 10 * time.Second

// startSync is the serving.OnServe starter: it runs only in the daemon (the reason is in the
// kernel's docs/09-extension-guide.md §3b2).
func startSync() (func(), error) {
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(syncInterval)
		defer t.Stop()
		for {
			syncOnce()
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()
	return func() { once.Do(func() { close(stop) }) }, nil
}

// lastSyncErr logs the same error only once: this runs every ten seconds in the background, and
// one unwritable file should not flood the log.
var (
	syncMu      sync.Mutex
	lastSyncErr string
)

// syncOnce runs one sync. Not taken over / not installed / no change are all a normal "do
// nothing".
func syncOnce() {
	target := ConfigPath()
	if !(Takeover{}).IsTakenOver(target) {
		return
	}
	// Only sync **the file that points at me**. Measured 2026-11-04: a second daemon on the
	// same machine (another NEWGATE_HOME, another port) had both sides rewriting base_url to
	// their own port every ten seconds, and hermes' requests bounced between the two proxies.
	// Whichever the file points at right now is the owner of this takeover; changing the port
	// is the job of `newgate on hermes` (explicit, once).
	if !pointsAt(target, proxyPort()) {
		return
	}
	res, err := syncCatalog()

	syncMu.Lock()
	defer syncMu.Unlock()
	if err != nil {
		if msg := err.Error(); msg != lastSyncErr {
			lastSyncErr = msg
			log.Printf("[hermes] %s", i18n.T("model list sync skipped: {err}", i18n.A{"err": err}))
		}
		return
	}
	lastSyncErr = ""
	if res == nil || len(res.Changes) == 0 {
		return
	}
	note := strings.Join(res.Changes, ", ")
	log.Printf("[hermes] %s", i18n.T("model list synced into {file}: {changes} (the gateway picks it up on the next message)",
		i18n.A{"file": target, "changes": note}))
	rec := loadRecord()
	rec.SyncedAt = time.Now().Unix()
	rec.SyncNote = note
	_ = saveRecord(rec)
}

// staleCatalog whether the list in the file is stale right now (for status / the card;
// read-only, no writes).
func staleCatalog() (bool, error) {
	target := ConfigPath()
	if !(Takeover{}).IsTakenOver(target) {
		return false, nil
	}
	in, err := currentInputs(proxyPort())
	if err != nil {
		return false, err
	}
	d, err := readDoc(target)
	if err != nil {
		return false, err
	}
	edits, _ := catalogEdits(d, in)
	for _, e := range edits {
		changed, err := d.put(e)
		if err != nil {
			return false, err
		}
		if changed {
			return true, nil
		}
	}
	return false, nil
}

func readDoc(path string) (*doc, error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseDoc(string(b)), nil
}

// pointsAt whether the base_url of `providers.newgate` in the file is this machine's port.
func pointsAt(target string, port int) bool {
	d, err := readDoc(target)
	if err != nil {
		return false
	}
	u, ok := d.get("providers", ProviderID, "base_url")
	return ok && strings.HasPrefix(u, "http://127.0.0.1:"+strconv.Itoa(port)+"/")
}
