package hermes

import (
	"encoding/json"

	"github.com/rzbdz/newgate-ext/lib/view"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is the side of hermes handed to the web UI: **which tier it runs**, plus the two
// gateway things (list sync, does it need a restart). The shape is aligned with codex's tier
// card (same set of Kinds, same position), because they are the same thing: "which tier this
// client uses right now".

const conceptID = "hermes.model"

func profileOf() agentapi.AgentProfile { return agentapi.AgentProfile{AgentID: ID} }

func registerView(v view.Service) (modules.Release, error) {
	return v.Register("hermes",
		view.Title(func() string { return i18n.T("Hermes", nil) }).
			In(func() string { return i18n.T("Clients", nil) }).
			Of(view.FieldClients),
		func() ([]view.Concept, error) { return []view.Concept{modelConcept()}, nil })
}

func modelConcept() view.Concept {
	a := Agent()
	_, bad := slotsOf().Read()
	items := make([]view.ToggleItem, 0, len(a.Slots)+1)
	items = append(items, profileOf().ProfileItem())
	for _, s := range a.Slots {
		why := s.Desc
		if note := slotsOf().Note(s.Name, s.Tier); note != "" {
			why += " · " + note
		}
		items = append(items, view.ToggleItem{
			ID: s.Name, Label: s.Name, Kind: view.ToggleSelect,
			Value:   agentapi.TierOf(facts{}, s),
			Options: slotsOf().AllowedFor(s.Name),
			Why:     why,
		})
	}
	file := "state.json"
	if len(bad) > 0 {
		file = i18n.T("state.json — {key} could not be read, the defaults are in use", i18n.A{"key": SlotsKey})
	}
	c := view.Concept{
		ID: conceptID, Kind: view.KindToggles,
		Title: i18n.T("Hermes model", nil),
		Data:  view.Toggles{File: file, Items: items},
		Order: 20,
		Apply: applyModel,
		// On a machine without hermes the whole card is grayed out (facts{} must be passed: it
		// recognizes ~/.local/bin beyond PATH, see facts.go).
		Locked: agentapi.NotInstalled(a, facts{}),
	}
	if (Takeover{}).IsTakenOver(ConfigPath()) {
		tone := view.ToneOK
		if restartPending() {
			tone = view.ToneWarn
		}
		c.Note = &view.Note{Text: statusText(false), Tone: tone}
		c.Actions = []view.Action{{
			ID:    "sync",
			Label: func() string { return i18n.T("sync /model list", nil) },
			Run: func() (string, error) {
				_, err := syncCatalog()
				return "", err
			},
		}, {
			ID:    "restart",
			Label: func() string { return i18n.T("restart hermes gateway", nil) },
			Run:   func() (string, error) { return "", restartGateway(true) },
		}}
	}
	return c
}

// applyModel writes the chain head and the slot map, then **takes over again right there**
// (when already taken over).
//
// One difference from codex's card: codex's tier waits for the next `newgate on codex` to be
// written into the file -- that is its historical behavior; hermes writes it as soon as save is
// pressed -- a slot's meaning is exactly those cells in config.yaml, and recording it only in
// state.json would mean nothing happened (the same rule as codex's switchMode).
func applyModel(edit json.RawMessage, _ string) (string, error) {
	var patch map[string]string
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the slot map in this request is not readable: {err}", i18n.A{"err": err})
	}
	profile, patch := agentapi.SplitProfile(patch)
	known := map[string]bool{}
	for _, s := range Agent().Slots {
		known[s.Name] = true
	}
	next := map[string]string{}
	for slot, tier := range patch {
		if known[slot] {
			next[slot] = tier
		}
	}
	if err := profileOf().Write(profile); err != nil {
		return "", err
	}
	if err := slotsOf().Write(next); err != nil {
		return "", err
	}
	if (Takeover{}).IsTakenOver(ConfigPath()) {
		if _, err := (Takeover{}).Apply(proxyPort()); err != nil {
			return "", err
		}
	}
	return "", nil
}
