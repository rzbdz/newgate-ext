// Package locale decides **which language this process uses**, and installs the catalogs into
// lib/i18n.
//
// See the split clearly: table lookup, placeholders, plurals and language-tag matching all live in
// the leaf lib/i18n (stateless, anyone can import it); this does the one stateful thing --
// **resolution**: which of the environment variable, the persisted choice in state.json, and the
// system locale has the final say. It is removable: with it gone lib/i18n has no catalogs, every
// sentence comes out as the source-language text, and the process carries on.
package locale

import (
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Source says **where this language came from**. Why report the source: after `newgate lang
// zh-Hans` is written into the config, if the current shell's `LANG` overrides it, the user thinks
// "I clearly set it, why does it not work". The source is the answer to that sentence -- doctor
// and `newgate lang` both print it.
type Source string

const (
	// SourceEnvOverride is a session-level explicit override (`NEWGATE_LANG=zh-Hans newgate
	// status`).
	SourceEnvOverride Source = "NEWGATE_LANG"
	// SourceConfig is the choice persisted in the config (written by `newgate lang zh-Hans`).
	SourceConfig Source = "config"
	// SourceSystem is following the system (LC_ALL / LC_MESSAGES / LANG).
	SourceSystem Source = "system"
	// SourceDefault is nobody said anything → the source language.
	SourceDefault Source = "default"
)

// Service is the ownership port for "which language this run uses".
type Service interface {
	// Lang is the language tag in effect (the one **actually** used, after fallback).
	Lang() string
	// Requested is which language was asked for (may differ from Lang: an untranslated one).
	Requested() string
	// Source is where this choice came from.
	Source() Source
	// Available is the checkup of the selectable languages (coverage, machine-translation counts).
	Available() []i18n.Info
}

// Capability is this port's unique identity.
var Capability = modules.NewCapability[Service]("locale")
