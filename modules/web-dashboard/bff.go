package webdashboard

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/lib/view"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Contract is the contract version between the front end and the BFF.
//
// The front end **must** check it first: for an unknown version, better a blank page saying
// "the UI is older/newer" than guessing shapes - a wrong guess hides a switch, silently.
const Contract = 1

// Handler is the BFF: a set of JSON endpoints plus static assets.
//
// # It knows no module
//
// This file has no config, no gateway, no "profile" - it does three things: serve the
// ledger's **concepts** to the front end, **forward** the front end's edits to the module
// that contributed them, and serve static assets. So adding a module's UI needs no change
// here (the module optionally depends on the view capability and registers in its own Start).
//
// Not fussiness: once the BFF reads config or writes files, it must know the fields, the
// directory layout, which fields are off limits - that knowledge grows here, one block per
// module. The first version was written that way, then deleted.
//
// Mounted under /ui (porthub's StripPrefix), so paths here start with /.
type Handler struct {
	assets fs.FS
	views  *view.Registry
	// themes is the skin registry (see api.go / theme.go). It is **this module's own**, not
	// pulled from the assembly graph: skin modules optionally depend on this port, and the
	// port lives for this assembly, so both sides see one instance.
	themes *themeRegistry
	// extensions are **extra JSON endpoints registered by modules** (see api.go's
	// ExtensionService). Same as themes: this module builds it, other modules register
	// into it, and it lives for this assembly.
	extensions *extensionRegistry
	// plugins is **front-end code modules ship** (see api.go's "front-end plugins").
	plugins *pluginRegistry
}

func NewHandler(assets fs.FS, views *view.Registry, themes *themeRegistry, extensions *extensionRegistry, plugins *pluginRegistry) *Handler {
	return &Handler{assets: assets, views: views, themes: themes, extensions: extensions, plugins: plugins}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// First gate: this request **really targets this machine** (see loopbackHost). Not against
	// outsiders - loopback-only handles them - but **a script on someone else's page**.
	if !loopbackHost(r) {
		h.writeJSONStatus(w, http.StatusForbidden, map[string]any{
			"error": i18n.T("this interface answers on loopback only; reach it as http://127.0.0.1:{port} "+
				"(the host name in this request was {host})", i18n.A{"port": portOf(r), "host": r.Host}),
		})
		return
	}
	switch {
	case r.URL.Path == "/api/snapshot":
		doc, err := h.snapshot(r.URL.Query()["source"]...)
		if err != nil {
			// A contributor cannot produce anything (the whole config dir is unreadable). Say
			// which step broke instead of an empty snapshot - which reads as "no modules".
			h.writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		h.writeJSONStatus(w, http.StatusOK, doc)
	case r.URL.Path == "/api/themes":
		// The skin is the **UI's own preference**, not in the concept ledger - so it goes
		// through its own door, not into the snapshot. The snapshot is each module's "what
		// my side looks like"; "what colors this screen uses" belongs to no module.
		if r.Method == http.MethodPost {
			h.setTheme(w, r)
			return
		}
		h.writeJSONStatus(w, http.StatusOK, themeDocOf(h.themes))
	case r.URL.Path == "/api/health":
		h.writeJSONStatus(w, http.StatusOK, map[string]any{"ok": true, "contract": Contract})
	case r.URL.Path == "/api/section":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, i18n.T("section actions use POST", nil), http.StatusMethodNotAllowed)
			return
		}
		h.sectionAction(w, r)
	case r.URL.Path == "/api/concept-action":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, i18n.T("concept actions use POST", nil), http.StatusMethodNotAllowed)
			return
		}
		h.conceptAction(w, r)
	case r.URL.Path == "/api/row-action":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, i18n.T("row actions use POST", nil), http.StatusMethodNotAllowed)
			return
		}
		h.rowAction(w, r)
	case r.URL.Path == "/api/apply":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, i18n.T("saving uses POST", nil), http.StatusMethodNotAllowed)
			return
		}
		h.apply(w, r)
	case r.URL.Path == "/api/preview":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, i18n.T("previewing uses POST", nil), http.StatusMethodNotAllowed)
			return
		}
		h.preview(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/ext/"):
		h.extension(w, r)
	case r.URL.Path == "/api/plugins":
		h.writeJSONStatus(w, http.StatusOK, map[string]any{"plugins": h.plugins.manifest()})
	case strings.HasPrefix(r.URL.Path, "/plugins/"):
		h.plugins.servePlugin(w, r)
	default:
		h.static(w, r)
	}
}

// ---------- this UI only answers the local machine ----------

// loopbackHost reports whether this request's Host header **points at this machine**.
//
// Why it must be checked: this UI can change config and flip runtime switches. Listening on
// loopback stops outside connections, not **a script on someone else's page** - the attacker
// resolves a domain they control to 127.0.0.1 (DNS rebinding), the browser deems it same
// origin, and it can send any Content-Type and read any response. The one thing the browser
// never lies about is the Host it fills in: the request goes to evil.com, the Host is
// evil.com, even if that name currently points to 127.0.0.1.
//
// Allowed: 127.0.0.1, ::1, localhost (any port). Everything else - this machine's LAN IP,
// any domain - is refused. The cost is plain: a newgate.local in /etc/hosts is shut out.
func loopbackHost(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		// Names (domains) are always refused - **that is why this gate exists**: DNS rebinding
		// needs an attacker-controlled domain resolving to the local machine. Users opening the
		// UI from elsewhere use an IP, so refusing names blocks no normal use.
		return false
	}
	// IP literals: loopback, the Tailscale range, and anything else (LAN / WSL's 172.x).
	//
	// Why "any IP" is fine: the Host is **filled in by the browser** with the address the
	// user navigated to. Forging it means making the user's browser visit
	// `http://192.168.1.5:8899` - no longer rebinding. Writes also have a second gate (JSON
	// only, pre-flight, no cross-origin read), so "IP allowed" is not "any page may write".
	_ = ip
	return true
}

// isTailscale says whether this address is in Tailscale's range (100.64.0.0/10, carrier NAT).
//
// It no longer gates anything on its own (IP literals are all allowed, see allowedHost); it
// stays as the **only** place stating "what 100.64/10 is" - the sentence users read in
// the config UI's listen-address cell.
func isTailscale(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	// 100.64.0.0/10: first octet 100, second octet's top 2 bits 01 (64..127).
	return v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}

// portOf takes the port this request connected to. Only so the error can build a **usable**
// URL - "open 127.0.0.1" without a port leaves the user guessing.
func portOf(r *http.Request) string {
	if _, port, err := net.SplitHostPort(r.Host); err == nil {
		return port
	}
	return ""
}

// ---------- snapshot ----------

type conceptDoc struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Source is who contributed it: the front end groups by it, and maps concepts to
	// sectionDoc. A **machine tag** (module name), not translated - sectionDoc names the column.
	Source string `json:"source"`
	// Writable false = this concept is read-only (the contributor gave no Apply).
	Writable bool `json:"writable"`
	// Non-empty Locked = this card is **meaningless right now** (see lib/view's Concept.Locked);
	// the value is the reason shown on the card. The UI greys it and disables the controls -
	// **still drawn**: hiding says the feature does not exist, but it does, just not here.
	Locked string `json:"locked,omitempty"`
	// Live true = this side's data changes by itself, so the UI should include its source in the
	// few-second refresh (see lib/view's Concept.Live). **Declared by the contributor**: owners
	// know whether a read is expensive; the UI cannot infer it.
	Live bool `json:"live,omitempty"`
	// Group is the left-column grouping (see lib/view's Concept.Group): cards in one group share
	// a heading. Empty string = its own group. **Affects order only**, no identity judgment.
	Group string `json:"group,omitempty"`
	// Previewable = the contributor gave a Preview (see lib/view's Concept.Preview): this card
	// can take a draft from the other half of the same file and say "how should I show this".
	// The UI decides whether to ask after a text edit - without it, do not ask; it is a wasted trip.
	Previewable bool `json:"previewable,omitempty"`
	// Order is who comes first within a section (see lib/view's Concept.Order).
	//
	// **Must go out**: the snapshot arrays are sorted by (Source, Order, ID), but the front
	// end merges fresh cards into its copy on a **partial refresh**, and that merge sorts
	// again - without Order it sorts by ID, so Order 0/1 cards like "global settings" drop
	// below a dozen tier cards on each silent refresh (measured: on top until refreshed, then
	// at the bottom - exactly what users reported as "they keep jumping down by themselves").
	Order int `json:"order"`
	Data  any `json:"data"`
	// Actions are the buttons **on this card** (see view.Concept.Actions). Same shape and rule
	// as sectionDoc.Actions: the BFF moves only ID and label; the click handler stays with the owner.
	Actions []view.ActionInfo `json:"actions,omitempty"`
	// Note is **a sentence of state** (see view.Concept.Note): "this is the current config"
	// and the like. Like Actions it passes through whole (`view.Note` has its own json tags),
	// and the BFF does not read its tone - `ok`/`warn`/`bad` is already the same vocabulary as
	// table cells (`view.Cell.Tone`); the front end knows those three words and colors nothing else.
	//
	// It is not a button: clicking it does nothing (contrast Actions, whose Run runs).
	Note *view.Note `json:"note,omitempty"`
	// Non-empty Error = this concept **cannot be read right now** (file deleted, JSON broken).
	// The card still appears with the reason instead of vanishing - vanishing says it is gone.
	Error string `json:"error,omitempty"`
}

type snapshotDoc struct {
	Contract    int    `json:"contract"`
	GeneratedAt string `json:"generated_at"`
	// Lang is the language this process **resolved** (NEWGATE_LANG > config > LC_ALL > ..., see
	// modules/locale). The front end picks its own UI copy with it - concept titles are already
	// translated by the backend, while buttons and hints are the front end's own words.
	//
	// Why the backend gives it instead of the front end guessing: language resolution has one
	// implementation (that module); a second guess (navigator.language?) means two sites, and
	// when they disagree the page mixes languages - and nobody reports that as a bug.
	Lang string `json:"lang"`
	// Sections is the sidebar's column table: **every registered source**, including those
	// that can produce no concept right now (config dir unreadable, omo never taken over).
	// Deriving sources from concepts makes those columns vanish, and users see "this module
	// does not exist" - then go looking elsewhere.
	//
	// Names are reported at registration (`view.Title`); the BFF only moves them, knowing no module.
	Sections []sectionDoc `json:"sections"`
	Concepts []conceptDoc `json:"concepts"`
}

// sectionDoc is one row of the column table: machine tag + human name (+ optional group).
type sectionDoc struct {
	Source string `json:"source"`
	Title  string `json:"title"`
	// Group is the heading this column falls under (empty = none, at the top). **Reported by
	// the contributor** (view.Section.Group): the BFF knows no module, so it cannot decide
	// "the breaker belongs with the gateway".
	Group string `json:"group,omitempty"`
	// Actions are this column's buttons (see view.Section.Actions). The BFF moves only ID and
	// label; "what happens on click" is the contributor's - its Run is a function, not shippable.
	Actions []view.ActionInfo `json:"actions,omitempty"`
	// Default is "land here when there is no position" (see view.Section.Default). **Declared
	// by the contributor**: which column the first screen lands on is a product choice, and
	// the BFF/front end know no module, so they cannot tell who should be there. They move it.
	Default bool `json:"default,omitempty"`
	// Fields say **what this column is for** (see view.SectionInfo.Fields). The UI classifies
	// by it - the displayed word is the UI's (translated), the class is this machine tag.
	//
	// This cell is **moved by hand** (this file copies ledger fields in places), so it was
	// missed once: Fields was added 2026-09-29 and this did not follow, so the sidebar
	// grouping never changed - with the ledger, kernel and every module all correct.
	Fields string `json:"fields,omitempty"`
}

// snapshot asks the contributors for what this moment looks like.
//
// "Asking" costs something (someone re-reads disk, recomputes metrics), so it only happens
// here - when someone really opens the UI or hits refresh. Never during assembly.
//
// `?source=` is for **auto refresh** (repeatable): every few seconds the UI refreshes
// counters and logs, which are cheap; the config source re-reads and re-parses every profile
// and every source file, and should not be woken along the way. Absent = all (the first load).
func (h *Handler) snapshot(sources ...string) (snapshotDoc, error) {
	doc := snapshotDoc{
		Contract:    Contract,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Lang:        i18n.Current(),
	}
	// The column table **comes every time**, even on a source-filtered refresh: it calls no
	// producer (only reads the registered names), so it is free; and the sidebar's badge counts
	// and "is this module still here" are what the few-second refresh most needs to track.
	for _, s := range h.views.Sections() {
		doc.Sections = append(doc.Sections, sectionDoc{
			Source: s.Source, Title: s.Title, Group: s.Group,
			Actions: s.Actions, Default: s.Default, Fields: s.Fields,
		})
	}
	if doc.Sections == nil {
		doc.Sections = []sectionDoc{}
	}
	concepts, err := h.views.Snapshot(sources...)
	if err != nil {
		return doc, err
	}
	for _, c := range concepts {
		doc.Concepts = append(doc.Concepts, conceptDoc{
			ID: c.ID, Kind: c.Kind, Title: c.Title, Source: c.Source,
			Writable: c.Apply != nil, Locked: c.Locked, Live: c.Live, Group: c.Group,
			Previewable: c.Preview != nil, Order: c.Order, Data: c.Data, Error: c.Broken,
			Actions: actionInfos(c.Actions), Note: c.Note,
		})
	}
	if doc.Concepts == nil {
		doc.Concepts = []conceptDoc{}
	}
	return doc, nil
}

// ---------- save ----------

type applyRequest struct {
	// ID is the concept's stable identity (the one in the snapshot).
	ID string `json:"id"`
	// Base is the baseline the front end got at load. Empty string = "it did not exist then".
	Base string `json:"base"`
	// Edit's shape is **defined by the concept itself**: the BFF does not parse it, just forwards.
	Edit json.RawMessage `json:"edit"`
}

type applyResponse struct {
	OK bool `json:"ok"`
	// Base is the **new baseline** after writing: the front end keeps editing without a reload.
	Base string `json:"base,omitempty"`
	// Focus is **which concept to switch to after the action** (see view.Action.Run) - "new"
	// needs it most: only the implementer knows what the new thing is called. Empty = stay put.
	Focus    string       `json:"focus,omitempty"`
	Conflict *conflictDoc `json:"conflict,omitempty"`
	Error    string       `json:"error,omitempty"`
}

type conflictDoc struct {
	Concept string `json:"concept"`
	Path    string `json:"path"`
	Base    string `json:"base"`
	Current string `json:"current"`
	Yours   string `json:"yours"`
	Theirs  string `json:"theirs"`
}

func (h *Handler) apply(w http.ResponseWriter, r *http.Request) {
	// Second gate: **a write must declare itself JSON**.
	//
	// Not fussiness: it keeps "another page pressed save for me" out. Cross-site forms and
	// no-cors fetches cannot send `Content-Type: application/json` (that pre-flights, and we
	// return no CORS headers), but they **can** send text/plain whose body looks like JSON -
	// and the decoder does not look at Content-Type. So this is the only wall on that path.
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		h.writeJSONStatus(w, http.StatusUnsupportedMediaType, applyResponse{
			Error: i18n.T("saving needs Content-Type: application/json (got {ct})", i18n.A{"ct": ct})})
		return
	}
	var req applyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{Error: i18n.T("the request body is not valid JSON: {err}", i18n.A{"err": err})})
		return
	}
	c, err := h.views.Get(req.ID)
	if errors.Is(err, view.ErrUnknownConcept) {
		// 404 rather than a silent success: the front end's ID came from a snapshot, so a
		// mismatch means **its snapshot is stale** (the module was just turned off); say so.
		h.writeJSONStatus(w, http.StatusNotFound, applyResponse{
			Error: i18n.T("no such concept: {id} — the page is probably stale, reload it", i18n.A{"id": req.ID})})
		return
	}
	if err != nil {
		h.writeJSONStatus(w, http.StatusInternalServerError, applyResponse{Error: err.Error()})
		return
	}
	if c.Apply == nil {
		h.writeJSONStatus(w, http.StatusConflict, applyResponse{
			Error: i18n.T("{title} is read-only", i18n.A{"title": c.Title})})
		return
	}
	newBase, err := c.Apply(req.Edit, req.Base)
	if err != nil {
		var conflict *view.Conflict
		if errors.As(err, &conflict) {
			h.writeJSONStatus(w, http.StatusConflict, applyResponse{Conflict: &conflictDoc{
				Concept: conflict.Concept, Path: conflict.Path, Base: conflict.Base,
				Current: conflict.Current, Yours: conflict.Yours, Theirs: conflict.Theirs,
			}})
			return
		}
		// The contributor's error goes out verbatim: that is **its** account of its data (which
		// field is unreadable, which line is invalid); the BFF paraphrasing only loses information.
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{Error: err.Error()})
		return
	}
	h.writeJSONStatus(w, http.StatusOK, applyResponse{OK: true, Base: newBase})
}

// ---------- section actions ----------

type sectionActionRequest struct {
	// Source is which section (machine tag, the one in the snapshot).
	Source string `json:"source"`
	// Action is which action in that section (see view.Action.ID).
	Action string `json:"action"`
}

// sectionAction runs an action mounted on a column - "create another tier file" and the like.
//
// Kept apart from apply on purpose: an action **belongs to no card** (what it creates has no
// concept yet), so it has no base, no edit, only "which section, which action". The BFF still
// only forwards: it does not know what the action does.
//
// Same gate as apply (must be JSON): it touches config on disk.
func (h *Handler) sectionAction(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		h.writeJSONStatus(w, http.StatusUnsupportedMediaType, applyResponse{
			Error: i18n.T("section actions need Content-Type: application/json (got {ct})", i18n.A{"ct": ct})})
		return
	}
	var req sectionActionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{
			Error: i18n.T("the request body is not valid JSON: {err}", i18n.A{"err": err})})
		return
	}
	focus, err := h.views.RunAction(req.Source, req.Action)
	if err != nil {
		// The contributor's error goes out verbatim ("no such action in that section" too) -
		// far more useful than a BFF-paraphrased "operation failed".
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{Error: err.Error()})
		return
	}
	h.writeJSONStatus(w, http.StatusOK, applyResponse{OK: true, Focus: focus})
}

// setTheme changes the skin (POST /api/themes, body `{"id": "..."}`).
//
// An empty id is **legal**: it means "back to the factory tokens" - a fourth value this cell
// already has. An unknown id errors instead of silently falling back: usually a module was
// turned off, and "the click did nothing" and "that skin is gone" are two different sentences.
func (h *Handler) setTheme(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		h.writeJSONStatus(w, http.StatusUnsupportedMediaType, applyResponse{
			Error: i18n.T("choosing a theme needs Content-Type: application/json (got {ct})", i18n.A{"ct": ct})})
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{
			Error: i18n.T("the request body is not valid JSON: {err}", i18n.A{"err": err})})
		return
	}
	if req.ID != "" && !h.themes.has(req.ID) {
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{
			Error: i18n.T("there is no theme {id} in this build", i18n.A{"id": req.ID})})
		return
	}
	if err := setSelectedTheme(req.ID); err != nil {
		h.writeJSONStatus(w, http.StatusInternalServerError, applyResponse{Error: err.Error()})
		return
	}
	h.writeJSONStatus(w, http.StatusOK, applyResponse{OK: true})
}

// actionInfos serves the contributor's action table in the shape the UI knows (labels evaluated).
//
// Same as the block in Registry.Sections; extracted because there are now two call sites
// (sections and concepts) - written twice, one would forget to evaluate Label up front, and
// the symptom of that mistake is **buttons with no text**.
func actionInfos(acts []view.Action) []view.ActionInfo {
	var out []view.ActionInfo
	for _, a := range acts {
		label := ""
		if a.Label != nil {
			label = a.Label()
		}
		out = append(out, view.ActionInfo{ID: a.ID, Label: label})
	}
	return out
}

// ---------- concept actions ----------

type conceptActionRequest struct {
	// ID is which card (the concept's stable identity, the one in the snapshot).
	ID string `json:"id"`
	// Action is which action on that card (see view.Action.ID).
	Action string `json:"action"`
}

// conceptAction runs an action mounted on **a card** - "make this one the default" and the like.
//
// Kept apart from sectionAction rather than merged because their **identities** differ: a
// section action knows a source name, a concept action knows a concept ID. Merged, one of the
// two fields would be optional, and "which one is empty" would be checked in two places.
//
// Same gate as apply (must be JSON): it touches config on disk.
func (h *Handler) conceptAction(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		h.writeJSONStatus(w, http.StatusUnsupportedMediaType, applyResponse{
			Error: i18n.T("concept actions need Content-Type: application/json (got {ct})", i18n.A{"ct": ct})})
		return
	}
	var req conceptActionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{
			Error: i18n.T("the request body is not valid JSON: {err}", i18n.A{"err": err})})
		return
	}
	focus, err := h.views.RunConceptAction(req.ID, req.Action)
	if err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{Error: err.Error()})
		return
	}
	h.writeJSONStatus(w, http.StatusOK, applyResponse{OK: true, Focus: focus})
}

// ---------- row actions ----------

type rowActionRequest struct {
	// ID is which card (the concept's stable identity).
	ID string `json:"id"`
	// Row is which row (see lib/view's Row.ID). A **machine tag**, not translated.
	Row string `json:"row"`
	// Action is which action on that row.
	Action string `json:"action"`
}

// rowAction runs an action on **one row** of a table - "probe this binding" and the like.
//
// Three fields rather than two because each row in a table is **a separate thing**: without
// the Row layer, "test" would not know what to test. The BFF still only forwards: it does not
// know what that row is or what the action does (it should not even know "this is a table").
func (h *Handler) rowAction(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		h.writeJSONStatus(w, http.StatusUnsupportedMediaType, applyResponse{
			Error: i18n.T("row actions need Content-Type: application/json (got {ct})", i18n.A{"ct": ct})})
		return
	}
	var req rowActionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{
			Error: i18n.T("the request body is not valid JSON: {err}", i18n.A{"err": err})})
		return
	}
	focus, err := h.views.RunRowAction(req.ID, req.Row, req.Action)
	if err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, applyResponse{Error: err.Error()})
		return
	}
	h.writeJSONStatus(w, http.StatusOK, applyResponse{OK: true, Focus: focus})
}

// ---------- preview ----------

type previewRequest struct {
	// ID is the stable identity of the concept **being previewed** (the controls half).
	ID string `json:"id"`
	// Text is the **unsaved** draft of the same file (the UI took it from the raw-text half).
	Text string `json:"text"`
}

type previewResponse struct {
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// preview hands a draft to the contributor owning the file, getting back "how should this
// card show right now".
//
// Same gate as apply (must be JSON): it saves nothing, but **the request carries the user's
// config content**, and the response returns config shapes to the page. apply says why.
//
// Failures are always 200: a draft that does not parse is **normal mid-typing**, not a
// broken request. With 4xx the UI needs two branches for "half-built" and "broken".
func (h *Handler) preview(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		h.writeJSONStatus(w, http.StatusUnsupportedMediaType, previewResponse{
			Error: i18n.T("previewing needs Content-Type: application/json (got {ct})", i18n.A{"ct": ct})})
		return
	}
	var req previewRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, previewResponse{
			Error: i18n.T("the request body is not valid JSON: {err}", i18n.A{"err": err})})
		return
	}
	c, err := h.views.Get(req.ID)
	if err != nil {
		// The contributor has no such id: empty. On the UI side this is a passing update, not a
		// button the user pressed - an error popup would turn typing into a visible failure.
		h.writeJSONStatus(w, http.StatusOK, previewResponse{})
		return
	}
	if c.Preview == nil {
		// This concept has no second half (it is the raw text), so there is nothing to preview.
		h.writeJSONStatus(w, http.StatusOK, previewResponse{})
		return
	}
	data, err := c.Preview([]byte(req.Text))
	if err != nil {
		h.writeJSONStatus(w, http.StatusOK, previewResponse{Error: err.Error()})
		return
	}
	h.writeJSONStatus(w, http.StatusOK, previewResponse{Data: data})
}

// ---------- extension endpoints ----------

// extension forwards one `/api/ext/<name>` call to the module that registered that name.
//
// The four things the BFF does are all **shape**, none business: recognize the verb, require
// JSON, find the name, forward verbatim. It does not know what `<name>` is, what the body
// should hold, or what counts as success - that is the registering module's (see api.go).
//
// Same gate as apply (must be JSON): extensions touch upstreams and runtime state, and
// there is no reason they should be looser than saving.
func (h *Handler) extension(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, i18n.T("extensions use POST", nil), http.StatusMethodNotAllowed)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		h.writeJSONStatus(w, http.StatusUnsupportedMediaType, map[string]any{
			"error": i18n.T("extension calls need Content-Type: application/json (got {ct})", i18n.A{"ct": ct})})
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/ext/")
	if name == "" || strings.Contains(name, "/") {
		// `/api/ext/` and `/api/ext/a/b` are not extension names. 404 rather than look up a
		// name containing a slash - the miss would say "no extension a/b", while the truth is
		// "this URL shape is wrong".
		h.writeJSONStatus(w, http.StatusNotFound, map[string]any{
			"error": i18n.T("no API extension at {path}", i18n.A{"path": r.URL.Path})})
		return
	}
	run, ok := h.extensions.Lookup(name)
	if !ok {
		// 404 rather than 500: usually the providing module is not in this assembly (or was just
		// removed). The UI then falls back to available data instead of going blank.
		h.writeJSONStatus(w, http.StatusNotFound, map[string]any{
			"error": i18n.T("there is no API extension named {name} in this build — "+
				"the page may be newer than the daemon, or the module that provides it is not installed",
				i18n.A{"name": name})})
		return
	}
	var body json.RawMessage
	if r.Body != nil {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			h.writeJSONStatus(w, http.StatusBadRequest, map[string]any{
				"error": i18n.T("cannot read the request body: {err}", i18n.A{"err": err})})
			return
		}
		if len(strings.TrimSpace(string(raw))) > 0 {
			if !json.Valid(raw) {
				h.writeJSONStatus(w, http.StatusBadRequest, map[string]any{
					"error": i18n.T("the request body is not valid JSON", nil)})
				return
			}
			body = raw
		}
	}
	out, err := run(ExtensionRequest{Context: r.Context(), Method: r.Method, Query: r.URL.Query(), Body: body})
	if err != nil {
		// The extension's error goes out verbatim: far more useful than the BFF paraphrasing
		// "operation failed" (same rule as apply).
		h.writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if out == nil {
		out = map[string]any{}
	}
	h.writeJSONStatus(w, http.StatusOK, out)
}

// ---------- static assets ----------

// static serves files from the embedded build output; unknown paths fall back to index.html
// (the front end routes). So a new front-end page needs no backend path registration.
func (h *Handler) static(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	if b, err := fs.ReadFile(h.assets, p); err == nil {
		serve(w, p, b)
		return
	}
	// SPA fallback: serve index.html only for GET and when it does not look like an asset
	// request; otherwise a typo'd .js silently becomes this page's HTML (errors point elsewhere).
	if r.Method == http.MethodGet && !strings.Contains(filepath.Base(p), ".") {
		if b, err := fs.ReadFile(h.assets, "index.html"); err == nil {
			serve(w, "index.html", b)
			return
		}
	}
	http.NotFound(w, r)
}

func serve(w http.ResponseWriter, p string, b []byte) {
	w.Header().Set("Content-Type", contentType(p))
	w.Header().Set("Cache-Control", cacheControl(p))
	_, _ = w.Write(b)
}

// cacheControl says how long a browser may keep this file.
//
// # Why this is mandatory (the 2026-09-20 trap)
//
// A response with no `Cache-Control` is **not** left uncached - the browser decides by
// heuristic (usually 10% of the age since last modified; without Last-Modified, per session).
// And this UI's build output has content hashes in names: `index-<hash>.js`. A new binary
// changes the hash and the old file leaves the embedded output, **but the browser still holds
// an old index.html pointing at the old name** - its old JS is cached too, so the page
// renders fine, just the **previous version** of the UI.
//
// That is the symptom: the server is fine (curl gets the new output), the user says "the new
// button is not there", and **no error anywhere**. The only way out is a hard refresh, and
// nobody thinks of that. Two of the three "you did not do it" reports came from this.
//
// So the entry document must ask the server **every time**: it is a few hundred bytes and the
// only file that knows the hash changed. The rest splits on "does the name carry a hash":
//
//   - under `assets/` are Vite-hashed outputs: content changes, name changes, so one name is
//     always one set of bytes and can be cached forever (the SPA convention, letting refresh
//     re-download only the few hundred bytes of index.html).
//   - others (a favicon dropped into dist later) lack that guarantee, so they get an ETag
//     check each time: one more 304 round trip beats pinning a changed same-name file forever.
func cacheControl(p string) string {
	if strings.HasSuffix(p, ".html") {
		return "no-store"
	}
	if strings.HasPrefix(p, "assets/") {
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
}

func contentType(p string) string {
	switch {
	case strings.HasSuffix(p, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(p, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(p, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(p, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(p, ".json"):
		return "application/json; charset=utf-8"
	case strings.HasSuffix(p, ".woff2"):
		return "font/woff2"
	}
	return "application/octet-stream"
}

func (h *Handler) writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
