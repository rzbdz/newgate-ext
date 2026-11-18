package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// The two halves of the provider table card: **is what is drawn right**, and **is what
// is written right**. The latter matters most -- that card is where the user configures
// credentials, and there is only one rule for credentials: the value must not leave.

// seedProviders places a providers.json in the sandbox.
func seedProviders(t *testing.T, body string) {
	t.Helper()
	testkit.Sandbox(t)
	if err := os.MkdirAll(paths.Root(), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ProvidersFile(), []byte(body), 0o660); err != nil {
		t.Fatal(err)
	}
}

// providersCard takes that card, and confirms it is still **writable**.
//
// This assertion is not fluff: before this card existed its form was
// "read-only source text", and the difference between read-only and writable
// shows in the UI only as a save button -- lose it and nothing goes red.
func providersCard(t *testing.T) view.Concept {
	t.Helper()
	c := providersConcept()
	if c.Kind != view.KindRecords {
		t.Fatalf("the provider table should be %q, actual %q", view.KindRecords, c.Kind)
	}
	if c.Broken != "" {
		t.Fatalf("cannot read this card: %s", c.Broken)
	}
	if c.Apply == nil {
		t.Fatal("the provider table is not writable -- then users can only add providers from the command line")
	}
	return c
}

// fieldValue takes a value by field name (Records' fields are an array, found by ID).
func fieldValue(t *testing.T, rec view.Record, id string) view.Field {
	t.Helper()
	for _, f := range rec.Fields {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("record %s has no field %q", rec.ID, id)
	return view.Field{}
}

// TestTheKeyNeverLeavesTheDaemon is this card's security
// half: **the credential value does not enter the snapshot**.
//
// The criterion is "the value never leaves the daemon", not "redacted before
// leaving": redacted source text going back to the UI means the user saves
// once and writes *** back to disk (which is exactly why providers.json was
// read-only all along). If the value never leaves, there is no writing it back.
func TestTheKeyNeverLeavesTheDaemon(t *testing.T) {
	seedProviders(t, `{"providers":{"demo":{"protocol":"anthropic","base_url":"https://x","api_key":"sk-super-secret"}}}`)

	card := providersCard(t)
	data := card.Data.(view.Records)
	if len(data.Items) != 1 {
		t.Fatalf("want exactly one provider, got %d", len(data.Items))
	}
	key := fieldValue(t, data.Items[0], "api_key")
	if key.Kind != view.FieldSecret {
		t.Errorf("api_key kind should be %q, got %q", view.FieldSecret, key.Kind)
	}
	if key.Value != "" {
		t.Errorf("the credential value leaked into the snapshot (%q) -- it travels over HTTP to the browser and is written back verbatim", key.Value)
	}
	// Non-credential fields give their values as usual: otherwise this card would be unusable.
	if got := fieldValue(t, data.Items[0], "base_url").Value; got != "https://x" {
		t.Errorf("base_url should be shown as usual, got %q", got)
	}
	// The hint must say "already set" -- that is the only difference the user
	// can see (the value itself is never shown). The criterion compares two i18n
	// strings rather than hardcoding Chinese: tests run in the source language, and
	// a hardcoded-Chinese one would go red on "is the catalog installed", while what
	// it wants to verify is "the hint differs when a key is set and when it is not".
	if key.Placeholder == "" || key.Placeholder == i18n.T("not set — paste the key here", nil) {
		t.Errorf("with a key set, the hint should differ from the unset case, got %q", key.Placeholder)
	}

	// Serializing the whole card must not carry that key
	// either: it could leak from another field (label, why...).
	raw, err := json.Marshal(card.Data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-super-secret") {
		t.Fatalf("the snapshot carries a plaintext key:\n%s", raw)
	}
}

// TestSavingWithoutTypingAKeyKeepsTheOldOne: the UI does not have the value,
// and the empty string it sends back means "do not touch it" -- not "delete it".
//
// If this is wrong, the symptom is **the user changes base_url once and the
// key is gone**, and silently: save succeeds, the next request is a 401.
func TestSavingWithoutTypingAKeyKeepsTheOldOne(t *testing.T) {
	seedProviders(t, `{"providers":{"demo":{"base_url":"https://old","api_key":"sk-keep-me"}}}`)
	card := providersCard(t)

	base := baseOf(t, card)
	if _, err := card.Apply(json.RawMessage(`{"items":[{"id":"demo","values":{
		"name":"demo","base_url":"https://new","api_key":""}}]}`), base); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	got := readProviders(t)
	p := got["demo"]
	if p["base_url"] == nil || !strings.Contains(string(p["base_url"]), "https://new") {
		t.Errorf("base_url was not edited: %s", p["base_url"])
	}
	if p["api_key"] == nil || !strings.Contains(string(p["api_key"]), "sk-keep-me") {
		t.Errorf("posting back an empty string lost the key: %s", p["api_key"])
	}
}

// TestTypingAKeyReplacesIt: typed means changed (the only way to add a provider).
func TestTypingAKeyReplacesIt(t *testing.T) {
	seedProviders(t, `{"providers":{"demo":{"base_url":"https://x","api_key":"sk-old"}}}`)
	card := providersCard(t)
	if _, err := card.Apply(json.RawMessage(`{"items":[{"id":"demo","values":{
		"name":"demo","api_key":"sk-new"}}]}`), baseOf(t, card)); err != nil {
		t.Fatal(err)
	}
	if p := readProviders(t)["demo"]; !strings.Contains(string(p["api_key"]), "sk-new") {
		t.Errorf("the key typed in was not written: %s", p["api_key"])
	}
}

// TestANewProviderAndADeletedOne: what the UI sends back is **all the records it
// holds**; what is not sent is deleted; a record with an empty id is a new one.
//
// Rename takes the same path (a mismatched id is one deleted and one
// added), so this also pins "rename does not silently leave two".
func TestANewProviderAndADeletedOne(t *testing.T) {
	seedProviders(t, `{"providers":{"old":{"base_url":"https://o"},"kept":{"base_url":"https://k"}}}`)
	card := providersCard(t)
	if _, err := card.Apply(json.RawMessage(`{"items":[
		{"id":"kept","values":{"name":"kept","base_url":"https://k"}},
		{"id":"","values":{"name":"fresh","base_url":"https://f","api_key":"sk-fresh"}}
	]}`), baseOf(t, card)); err != nil {
		t.Fatal(err)
	}
	got := readProviders(t)
	if len(got) != 2 {
		t.Fatalf("should be two left (kept and fresh), got %d: %v", len(got), keysOf(got))
	}
	if got["old"] != nil {
		t.Error("a provider that was not posted back must be deleted -- otherwise the UI's delete button is decoration")
	}
	if !strings.Contains(string(got["fresh"]["api_key"]), "sk-fresh") {
		t.Errorf("the newly added provider was not written in full: %v", got["fresh"])
	}
}

// TestUnknownKeysSurvive: this file may hold things the UI does not
// recognize (hand-written notes, fields core adds later). A full rewrite
// would erase them, and the user would not know which save lost them.
func TestUnknownKeysSurvive(t *testing.T) {
	seedProviders(t, `{"providers":{"demo":{"base_url":"https://x","my_note":"hand-written"}},"other_section":1}`)
	card := providersCard(t)
	if _, err := card.Apply(json.RawMessage(`{"items":[{"id":"demo","values":{
		"name":"demo","base_url":"https://y"}}]}`), baseOf(t, card)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(paths.ProvidersFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "my_note") {
		t.Errorf("an unknown key inside a provider was erased:\n%s", raw)
	}
	if !strings.Contains(string(raw), "other_section") {
		t.Errorf("an unknown top-level section was erased:\n%s", raw)
	}
}

// TestDuplicateNamesAreRejected: when two records write the same name, quietly keeping
// one results in "the one I edited is gone". Report an error and let the user see it.
func TestDuplicateNamesAreRejected(t *testing.T) {
	seedProviders(t, `{"providers":{}}`)
	card := providersCard(t)
	_, err := card.Apply(json.RawMessage(`{"items":[
		{"id":"","values":{"name":"dup","base_url":"https://a"}},
		{"id":"","values":{"name":"dup","base_url":"https://b"}}
	]}`), baseOf(t, card))
	if err == nil {
		t.Fatal("a duplicate name must be rejected")
	}
	if !strings.Contains(err.Error(), "dup") {
		t.Errorf("the error must name which name collided: %v", err)
	}
}

// TestAProviderWithoutANameIsRejected: the name is the key tier bindings
// reference it by, and an empty one writes something nobody can point at.
func TestAProviderWithoutANameIsRejected(t *testing.T) {
	seedProviders(t, `{"providers":{}}`)
	card := providersCard(t)
	if _, err := card.Apply(json.RawMessage(`{"items":[{"id":"","values":{"name":"  "}}]}`),
		baseOf(t, card)); err == nil {
		t.Fatal("a provider with no name must be rejected")
	}
}

// TestAMissingFileIsNotAnError: a fresh install has not created providers.json yet.
// That is not a fault -- the UI should be able to add the first one against it.
func TestAMissingFileIsNotAnError(t *testing.T) {
	testkit.Sandbox(t)
	card := providersConcept()
	if card.Broken != "" {
		t.Fatalf("a missing file must not become a broken card: %s", card.Broken)
	}
	data := card.Data.(view.Records)
	if len(data.Items) != 0 {
		t.Fatalf("an empty table should have no rows, got %d", len(data.Items))
	}
	if !data.CanAdd {
		t.Error("an empty table needs \"add one\" most of all -- CanAdd should be true")
	}
	if _, err := card.Apply(json.RawMessage(`{"items":[{"id":"","values":{
		"name":"first","base_url":"https://f","api_key":"sk-1"}}]}`), ""); err != nil {
		t.Fatalf("creating one from scratch should succeed: %v", err)
	}
	if p := readProviders(t)["first"]; p == nil {
		t.Error("the first one was not written")
	}
}

// ---------- small helpers ----------

// baseOf is the baseline this card hands out. Deliberately taken from **the card
// itself** (rather than computed on the spot), because "is the baseline given along
// with the card" is one of the things to pin: the front end relies on it when saving.
func baseOf(t *testing.T, c view.Concept) string {
	t.Helper()
	got, ok := c.Data.(view.Records)
	if !ok {
		t.Fatalf("Data should be view.Records, actual %T", c.Data)
	}
	return got.Base
}

// readProviders reads back the source text of the providers section (keeping the raw JSON).
func readProviders(t *testing.T) map[string]map[string]json.RawMessage {
	t.Helper()
	got, err := readProvidersRaw(paths.ProvidersFile())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func keysOf(m map[string]map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
