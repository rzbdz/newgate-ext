package webdashboard

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/rzbdz/newgate-ext/lib/view"
)

// The BFF has only three duties: serve concepts, **forward** edits to the contributor, serve
// static assets. These tests lock that there is no fourth - one more (say reading config
// itself) means it starts knowing a module, which is what this version deleted.

func testAssets() fs.FS {
	return fstest.MapFS{
		"index.html":              {Data: []byte("<html>app</html>")},
		"app.js":                  {Data: []byte("console.log(1)")},
		"assets/index-deadbee.js": {Data: []byte("console.log(2)")},
	}
}

func newHandler() *Handler {
	return NewHandler(testAssets(), view.NewRegistry(), newThemeRegistry(), newExtensionRegistry(), newPluginRegistry())
}

// contribute registers a contributor: the producer hands this batch of concepts on each call.
func contribute(t *testing.T, h *Handler, source string, concepts ...view.Concept) {
	t.Helper()
	if _, err := h.views.Register(source, view.Title(func() string { return source }),
		func() ([]view.Concept, error) { return concepts, nil }); err != nil {
		t.Fatal(err)
	}
}

// TestLiveSurvivesTheWire: Live is declared by the contributor in Go, and the UI decides which
// sources to re-ask every few seconds from the JSON field (see the front end's liveSources).
//
// This hop breaking is **silent**: field lost -> no source refreshes -> the page quietly stops
// at the moment it opened, while the "auto refresh" checkbox looks fine. So it is worth an
// assertion, rather than waiting for someone to notice a frozen counter in the browser.
func TestLiveSurvivesTheWire(t *testing.T) {
	h := newHandler()
	contribute(t, h, "somewhere",
		view.Concept{ID: "live.one", Kind: view.KindSeries, Title: "Counters", Live: true},
		view.Concept{ID: "static.one", Kind: view.KindTable, Title: "Modules"},
	)

	rec := get(t, h, "/api/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot should be 200, actual %d", rec.Code)
	}
	var doc snapshotDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	byID := map[string]conceptDoc{}
	for _, c := range doc.Concepts {
		byID[c.ID] = c
	}
	if !byID["live.one"].Live {
		t.Error("a concept that declared Live must be served to the front end -- lose it and nobody is asked for the auto refresh")
	}
	if byID["static.one"].Live {
		t.Error("a concept that did not declare it must not be treated as changing: that would drag the whole source into a poll every few seconds")
	}

	// The **other** concepts of the same source go out too: the UI's refresh granularity is the
	// source, not this one (see Concept.Live in core/lib/view).
	if byID["live.one"].Source != byID["static.one"].Source {
		t.Fatal("this case assumes the two concepts have the same source")
	}
}

// TestSectionsSurviveTheWire is the whole path of a sidebar column's **name**, module to browser.
//
// Names are reported at registration (`view.Title`); the BFF knows no module, it only moves.
// Breaking this is **silent**: the front end falls back to drawing the source name as the
// title, so the sidebar shows config / plugin-manager / opencode-omo machine tags - which
// looks like "not translated", not a bug. Worth an assertion, not a browser discovery.
func TestSectionsSurviveTheWire(t *testing.T) {
	h := newHandler()
	// A source that **can produce no concept right now**: config dir unreadable, omo never
	// taken over - that column still needs a spot in the sidebar (vanishing says not installed).
	if _, err := h.views.Register("quiet", view.Title(func() string { return "Quiet module" }),
		func() ([]view.Concept, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	contribute(t, h, "config", view.Concept{ID: "config.state", Kind: view.KindToggles, Title: "Global settings"})

	rec := get(t, h, "/api/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot should be 200, actual %d", rec.Code)
	}
	var doc snapshotDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	titles := map[string]string{}
	for _, s := range doc.Sections {
		titles[s.Source] = s.Title
	}
	if len(doc.Sections) != 2 {
		t.Fatalf("both registered sources should be in the column table, actual %+v", doc.Sections)
	}
	if titles["quiet"] != "Quiet module" {
		t.Errorf("a source that produced no concepts should still appear with its name: %+v", doc.Sections)
	}
	// The title comes from the sentence reported at registration, not the source name (a
	// machine tag: it goes out too, but for the front end to match on, not for humans).
	if titles["config"] != "config" {
		t.Errorf("this case assumes the source name and title are the same shape in the test: %+v", doc.Sections)
	}
	if got := doc.Sections[0].Source; got != "config" {
		t.Errorf("the column table is sorted by source (UI order must be stable across restarts), the first should be config, actual %q", got)
	}
}

// TestLandingSurvivesTheWire: which column the first screen lands on is **declared by the
// contributor**, and the UI's default position is used at the last hop (see lib/view).
//
// Breaking this is **silent**: field lost -> no default -> the front end falls back to the
// old rule (the first column with cards), so the first screen lands on the alphabetically
// first column, which looks like "a layout I do not like", not a bug. Worth an assertion.
func TestLandingSurvivesTheWire(t *testing.T) {
	h := newHandler()
	if _, err := h.views.Register("home", view.Title(func() string { return "Home" }).Landing(),
		func() ([]view.Concept, error) {
			return []view.Concept{{ID: "home.chains", Kind: view.KindTable, Title: "Chains"}}, nil
		}); err != nil {
		t.Fatal(err)
	}
	contribute(t, h, "breaker", view.Concept{ID: "breaker.health", Kind: view.KindTable, Title: "Health"})

	rec := get(t, h, "/api/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot should be 200, actual %d", rec.Code)
	}
	var doc snapshotDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range doc.Sections {
		got[s.Source] = s.Default
	}
	if !got["home"] {
		t.Errorf("the column that declared Landing should serve default to the front end: %+v", doc.Sections)
	}
	if got["breaker"] {
		t.Errorf("the column that did not declare it must not carry default: %+v", doc.Sections)
	}
}

// TestFilteredSnapshotStillCarriesEverySection: the source-filtered snapshot (the UI's
// few-second refresh) must **still carry the full column table**.
//
// The opposite (only the named sources' columns) looks more "consistent" but makes the
// sidebar a board that shrinks: one refresh drops a column, the next brings it back, every
// few seconds, when nobody may be looking. The table calls no producer; it costs nothing.
func TestFilteredSnapshotStillCarriesEverySection(t *testing.T) {
	h := newHandler()
	var cheap, expensive atomic.Int64
	contributeCount(t, h, "gateway", &cheap)
	contributeCount(t, h, "config", &expensive)

	rec := get(t, h, "/api/snapshot?source=gateway")
	if rec.Code != http.StatusOK {
		t.Fatalf("filtering by source should be 200, actual %d", rec.Code)
	}
	var doc snapshotDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Concepts) != 1 {
		t.Fatalf("only the named concept should remain, actual %d", len(doc.Concepts))
	}
	if len(doc.Sections) != 2 {
		t.Errorf("the column table should be complete (the sidebar must not shrink on refresh), actual %+v", doc.Sections)
	}
}

// loopback is the default Host in tests.
//
// It must be set explicitly: httptest.NewRequest defaults to `example.com`, and the BFF now
// only accepts the local Host (see loopbackHost) - unset, every case hits 403 and looks broken.
const loopback = "127.0.0.1:8899"

// raw is "send once with the Host / Content-Type I give" - for the two gates' cases
// (others use get/post, with conventional Host and Content-Type).
func raw(t *testing.T, h http.Handler, method, path, body, host, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if host != "" {
		req.Host = host
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = loopback
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Host = loopback
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestSnapshotCarriesWhateverTheLedgerHolds: the BFF knows no module, it only moves the ledger.
func TestSnapshotCarriesWhateverTheLedgerHolds(t *testing.T) {
	h := newHandler()
	contribute(t, h, "somewhere",
		view.Concept{ID: "something.x", Kind: view.KindSeries, Title: "Counters",
			Data: map[string]any{"n": 3}},
		view.Concept{ID: "read.only", Kind: view.KindCode, Title: "A file",
			Data: map[string]any{"path": "x.json"}},
	)

	rec := get(t, h, "/api/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot should be 200, actual %d", rec.Code)
	}
	var doc snapshotDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Contract != Contract {
		t.Errorf("the contract version must match: %d", doc.Contract)
	}
	if len(doc.Concepts) != 2 {
		t.Fatalf("it should serve 2 concepts, actual %d", len(doc.Concepts))
	}
	// The read-only one must be marked read-only - the front end decides whether to show save.
	byID := map[string]conceptDoc{}
	for _, c := range doc.Concepts {
		byID[c.ID] = c
	}
	if byID["read.only"].Writable {
		t.Error("a concept without Apply should be marked read-only")
	}
	if byID["something.x"].Kind != view.KindSeries || byID["something.x"].Source != "somewhere" {
		t.Errorf("the concept's identity and source should be served as is: %+v", byID["something.x"])
	}
}

// TestApplyRoutesToTheConceptThatOwnsTheData: edits are **forwarded**, not handled here.
func TestApplyRoutesToTheConceptThatOwnsTheData(t *testing.T) {
	h := newHandler()
	var gotEdit string
	var gotBase string
	contribute(t, h, "x", view.Concept{
		ID: "a.b", Kind: view.KindCode, Title: "A",
		Apply: func(edit json.RawMessage, base string) (string, error) {
			gotEdit, gotBase = string(edit), base
			return "sha256:after", nil
		},
	})
	rec := post(t, h, "/api/apply", `{"id":"a.b","base":"sha256:before","edit":{"text":"hi"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("should be 200, actual %d: %s", rec.Code, rec.Body.String())
	}
	var resp applyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Base != "sha256:after" {
		t.Errorf("the new baseline should be handed back to the front end (so editing can continue without reloading): %+v", resp)
	}
	if gotEdit != `{"text":"hi"}` || gotBase != "sha256:before" {
		t.Errorf("the edit content and baseline should be passed through **verbatim**: edit=%s base=%s", gotEdit, gotBase)
	}
}

// TestApplyReportsConflictWithBothSides: a conflict must carry both sides' text. Returning
// just "expired" makes the user guess what they lost.
func TestApplyReportsConflictWithBothSides(t *testing.T) {
	h := newHandler()
	contribute(t, h, "x", view.Concept{
		ID: "a.b", Kind: view.KindCode, Title: "A",
		Apply: func(json.RawMessage, string) (string, error) {
			return "", &view.Conflict{
				Concept: "a.b", Path: "mappings/a.json",
				Base: "sha256:mine", Current: "sha256:theirs",
				Yours: `{"roles":{}}`, Theirs: `{"roles":{"heavy":[]}}`,
			}
		},
	})
	rec := post(t, h, "/api/apply", `{"id":"a.b","base":"sha256:mine","edit":{}}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a conflict should be 409, actual %d: %s", rec.Code, rec.Body.String())
	}
	var resp applyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Conflict == nil {
		t.Fatal("409 must carry the conflicting content")
	}
	if resp.Conflict.Yours == "" || resp.Conflict.Theirs == "" || resp.Conflict.Path == "" {
		t.Errorf("the conflict should give both texts and the path: %+v", resp.Conflict)
	}
}

// TestApplyRejectsUnknownAndReadOnly: both "should not write" cases must be stated clearly.
func TestApplyRejectsUnknownAndReadOnly(t *testing.T) {
	h := newHandler()
	contribute(t, h, "x", view.Concept{ID: "ro", Kind: view.KindCode, Title: "Read only"})
	// Unknown ID: the front end's snapshot is stale (e.g. the module was just turned off).
	if rec := post(t, h, "/api/apply", `{"id":"ghost","edit":{}}`); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown concept should be 404 (the front end should refresh the page), actual %d", rec.Code)
	}
	// Read-only concept: say it is read-only, not a vague failure.
	rec := post(t, h, "/api/apply", `{"id":"ro","edit":{}}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("a read-only concept should be rejected, actual %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "read-only") {
		t.Errorf("the error should say clearly that it is read-only: %s", rec.Body.String())
	}
	if rec := post(t, h, "/api/apply", `{not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON should be 400, actual %d", rec.Code)
	}
}

// TestApplyIsPostOnly: a write must not be triggerable with an <img src>.
func TestApplyIsPostOnly(t *testing.T) {
	h := newHandler()
	if rec := get(t, h, "/api/apply"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/apply should be 405, actual %d", rec.Code)
	}
}

// TestOnlyAddressesNotNamesAreAnswered: DNS rebinding.
//
// Listening on loopback stops outside connections, not **a script on someone else's page** -
// the attacker resolves their domain to 127.0.0.1 and the browser deems it same origin.
// The browser never lies about the Host it fills in, so the criterion is "which name".
//
// Loosened once on 2026-09-20, **by the same criterion**: names always refused, IPs always
// accepted. Originally only loopback, because it was for this machine; from Windows / LAN /
// Tailscale the addresses are all IP literals, and forging the Host means making the browser
// visit that IP - no longer rebinding. Writes have a second gate (JSON, pre-flight), so
// "IP accepted" is not "any page can change config".
func TestOnlyAddressesNotNamesAreAnswered(t *testing.T) {
	h := newHandler()
	contribute(t, h, "config", view.Concept{
		ID: "config.secretish", Kind: view.KindCode, Title: "A file",
		Data: map[string]any{"path": "x.json", "text": "something worth stealing"},
	})

	accepted := []string{
		loopback, "localhost:8899", "[::1]:8899",
		"10.0.50.11:8899",     // LAN / another machine
		"100.101.224.37:8899", // Tailscale（100.64.0.0/10）
		"172.24.1.5:8899",     // WSL / container subnet
	}
	for _, host := range accepted {
		if rec := raw(t, h, http.MethodGet, "/api/snapshot", "", host, ""); rec.Code != http.StatusOK {
			t.Errorf("Host %q should be accepted (it is an address, not a name), actual %d", host, rec.Code)
		}
	}
	// **Names** are always refused - that is what this gate really blocks.
	for _, host := range []string{"evil.com:8899", "localhost.evil.com:8899", "newgate.local:8899"} {
		rec := raw(t, h, http.MethodGet, "/api/snapshot", "", host, "")
		if rec.Code != http.StatusForbidden {
			t.Errorf("Host %q should be rejected (actual %d) -- the domain name is exactly the carrier of rebinding", host, rec.Code)
		}
		// Refused means no content leaks: this response carries config text (redacted parts).
		if strings.Contains(rec.Body.String(), "worth stealing") {
			t.Errorf("Host %q was rejected, yet the content was served anyway", host)
		}
	}
}

// TestSavingNeedsAJSONContentType: cross-site forms and no-cors fetches cannot send
// application/json, but they **can** send a text/plain body that looks like JSON - and the
// decoder does not look at Content-Type. So this is the only wall on that path.
func TestSavingNeedsAJSONContentType(t *testing.T) {
	h := newHandler()
	var applied bool
	contribute(t, h, "x", view.Concept{
		ID: "a.b", Kind: view.KindCode, Title: "A",
		Apply: func(json.RawMessage, string) (string, error) {
			applied = true
			return "sha256:new", nil
		},
	})

	body := `{"id":"a.b","base":"","edit":{"text":"x"}}`
	rec := raw(t, h, http.MethodPost, "/api/apply", body, loopback, "text/plain;charset=UTF-8")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a text/plain write request should be rejected (415), actual %d", rec.Code)
	}
	if applied {
		t.Error("a rejected request must not reach the Apply underneath")
	}
	// The real front end takes this path and must keep working.
	if rec := raw(t, h, http.MethodPost, "/api/apply", body, loopback, "application/json"); rec.Code != http.StatusOK {
		t.Fatalf("application/json should pass as usual, actual %d: %s", rec.Code, rec.Body.String())
	}
	if !applied {
		t.Error("a legitimate request did not reach Apply")
	}
}

// TestSnapshotKeepsBrokenConceptsVisible: an unreadable concept (file deleted, JSON broken)
// must **stay listed with the reason**, not vanish. Vanishing says it does not exist, and
// the user goes looking elsewhere; failing the whole page lets one bad file blank the UI.
func TestSnapshotKeepsBrokenConceptsVisible(t *testing.T) {
	h := newHandler()
	contribute(t, h, "config",
		view.Concept{ID: "config.profile.ds", Kind: view.KindMapping, Title: "ds",
			Broken: "open mappings/ds.kv: no such file or directory"},
		view.Concept{ID: "config.state", Kind: view.KindToggles, Title: "Global settings",
			Data: map[string]any{"items": []any{}}},
	)
	rec := get(t, h, "/api/snapshot")
	if rec.Code != http.StatusOK {
		t.Fatalf("one bad concept must not fail the whole snapshot, actual %d", rec.Code)
	}
	var doc snapshotDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Concepts) != 2 {
		t.Fatalf("the broken card should still appear, actual %d concepts", len(doc.Concepts))
	}
	byID := map[string]conceptDoc{}
	for _, c := range doc.Concepts {
		byID[c.ID] = c
	}
	if byID["config.profile.ds"].Error == "" {
		t.Error("the broken card must say why")
	}
	if byID["config.profile.ds"].Writable {
		t.Error("an undecodable concept should not be writable")
	}
	if byID["config.state"].Error != "" {
		t.Error("the good card must not be dragged down with it")
	}
}

// TestSnapshotCanBeAskedForOneSource: auto refresh asks only counters and logs (cheap), not
// the config source - it re-reads and re-parses every profile and source file. This locks
// that the refresh really saves that cost, not just returns fewer fields.
func TestSnapshotCanBeAskedForOneSource(t *testing.T) {
	h := newHandler()
	var cheap, expensive atomic.Int64
	contributeCount(t, h, "gateway", &cheap)
	contributeCount(t, h, "config", &expensive)

	if rec := get(t, h, "/api/snapshot"); rec.Code != http.StatusOK {
		t.Fatalf("without source it should ask everyone, actual %d", rec.Code)
	}
	if cheap.Load() != 1 || expensive.Load() != 1 {
		t.Fatalf("the first load should ask everyone: gateway=%d config=%d", cheap.Load(), expensive.Load())
	}

	rec := get(t, h, "/api/snapshot?source=gateway")
	if rec.Code != http.StatusOK {
		t.Fatalf("filtering by source should be 200, actual %d", rec.Code)
	}
	if cheap.Load() != 2 {
		t.Errorf("the named source should be asked again: %d", cheap.Load())
	}
	if expensive.Load() != 1 {
		t.Errorf("the unnamed one must not be woken up: %d times", expensive.Load())
	}
	var doc snapshotDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Concepts) != 1 || doc.Concepts[0].Source != "gateway" {
		t.Errorf("only the named source's concepts should come back: %+v", doc.Concepts)
	}
}

func contributeCount(t *testing.T, h *Handler, source string, n *atomic.Int64) {
	t.Helper()
	if _, err := h.views.Register(source, view.Title(func() string { return source }),
		func() ([]view.Concept, error) {
			n.Add(1)
			return []view.Concept{{ID: source + ".x", Kind: view.KindSeries, Title: source}}, nil
		}); err != nil {
		t.Fatal(err)
	}
}

// TestSnapshotFailureIsLoud: when a group cannot be produced (config root unreadable), say
// which step broke instead of an empty snapshot - which reads as "no modules".
func TestSnapshotFailureIsLoud(t *testing.T) {
	h := newHandler()
	if _, err := h.views.Register("config", view.Title(func() string { return "Configuration" }),
		func() ([]view.Concept, error) {
			return nil, errors.New("permission denied")
		}); err != nil {
		t.Fatal(err)
	}
	rec := get(t, h, "/api/snapshot")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("should be 500, actual %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "permission denied") {
		t.Errorf("the error should carry the real cause: %s", rec.Body.String())
	}
}

// TestStaticServesTheAppAndFallsBackForRoutes: static assets + SPA fallback; but a **typo'd
// asset must not** fall back to index.html - that would point browser errors elsewhere.
func TestStaticServesTheAppAndFallsBackForRoutes(t *testing.T) {
	h := newHandler()
	if rec := get(t, h, "/"); !strings.Contains(rec.Body.String(), "app") {
		t.Errorf("the root path should serve index.html, actual %q", rec.Body.String())
	}
	if ct := get(t, h, "/app.js").Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("the Content-Type for js is wrong: %q", ct)
	}
	if rec := get(t, h, "/some/route"); !strings.Contains(rec.Body.String(), "app") {
		t.Errorf("a front-end route should fall back to index.html, actual %q", rec.Body.String())
	}
	if rec := get(t, h, "/typo.js"); rec.Code != http.StatusNotFound {
		t.Errorf("a misspelled asset should be 404, actual %d (falling back to HTML would point the error somewhere else)", rec.Code)
	}
}

// TestTheEntryDocumentIsNeverCached: the entry document must ask the server every time.
//
// This guards the **most expensive kind of "you did not do it"**: after a release the user
// still sees the previous UI while the server is fine (curl gets the new output). Hashed
// names give it two layers - the old index.html points at the old JS name, and that JS is
// still cached, so the page renders, just as the old version. No error; the user believes
// "the feature was not done".
//
// So all three are pinned: entry no-store (only it knows the hash changed), hashed outputs
// immutable (one name, one set of bytes), the rest no-cache (better a round trip than a stale pin).
func TestTheEntryDocumentIsNeverCached(t *testing.T) {
	h := newHandler()

	cc := func(path string) string { return get(t, h, path).Header().Get("Cache-Control") }
	if got := cc("/"); got != "no-store" {
		t.Errorf("the entry should be no-store (after a release users must see the new version at once), actual %q", got)
	}
	if got := cc("/some/route"); got != "no-store" {
		t.Errorf("the SPA fallback also serves the entry, also no-store, actual %q", got)
	}
	if got := cc("/assets/index-deadbee.js"); !strings.Contains(got, "immutable") {
		t.Errorf("output with a content hash should be cacheable long-term, actual %q", got)
	}
	if got := cc("/app.js"); got != "no-cache" {
		t.Errorf("a file without a hash should be revalidated every time, actual %q", got)
	}
}

// TestPreviewHandsTheDraftToWhoeverOwnsTheFile: the preview path only **hands over**.
//
// It knows no file format - that is where "BFF knows no module" lands: draft in, contributor
// computes, result out. The test uses a fake contributor, pinning three things: the draft
// arrives untouched, the computed shape is served untouched, and **a parse failure is a
// 200, not a 4xx** - the line being typed cannot parse; normal mid-typing, not a broken
// request. Both UI places want one thing (keep the last shape); two states add a branch.
func TestPreviewHandsTheDraftToWhoeverOwnsTheFile(t *testing.T) {
	h := newHandler()
	var got string
	contribute(t, h, "x",
		view.Concept{ID: "ctl", Kind: view.KindMapping, Title: "Ctl",
			Preview: func(draft []byte) (any, error) {
				got = string(draft)
				return map[string]any{"seen": string(draft)}, nil
			}},
		view.Concept{ID: "raw", Kind: view.KindCode, Title: "Raw",
			Preview: func([]byte) (any, error) {
				return nil, errors.New("line 2 is not key=value")
			}},
	)

	rec := post(t, h, "/api/preview", `{"id":"ctl","text":"a=b\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("should be 200, actual %d: %s", rec.Code, rec.Body.String())
	}
	if got != "a=b\n" {
		t.Errorf("the draft was not handed to the contributor as is: %q", got)
	}
	var resp previewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if m, ok := resp.Data.(map[string]any); !ok || m["seen"] != "a=b\n" {
		t.Errorf("what the contributor computed was not served to the front end: %+v", resp)
	}

	// Mid-typing: 200 + error. A 4xx would make the browser log a console error, but that is
	// **normal input** - a false alarm nobody investigates, right over the real one.
	rec = post(t, h, "/api/preview", `{"id":"raw","text":"a="}`)
	if rec.Code != http.StatusOK {
		t.Errorf("failing to parse should be 200 (the normal state while typing), actual %d", rec.Code)
	}
	resp = previewResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == "" || resp.Data != nil {
		t.Errorf("it should carry that error and no data: %+v", resp)
	}

	// A concept with no Preview (it is the raw text itself): return empty, **not an error**.
	// To the UI this is only a casual refresh; an error popup turns one keystroke into a fault.
	contribute(t, h, "y", view.Concept{ID: "plain", Kind: view.KindToggles, Title: "Plain"})
	rec = post(t, h, "/api/preview", `{"id":"plain","text":"{}"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("a concept without the second half should not error, actual %d", rec.Code)
	}
}
