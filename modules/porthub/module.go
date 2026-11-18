// Package porthub makes "several services on one port" a component.
//
// It exists for one reason: to let consumers ask "**does this process have a shared
// port**" - if yes, mount on it (share gateway's port); if not, listen on its own (fallback).
// The table itself lives in lib/porthub (a leaf mechanism).
//
// The port belongs to this table: the daemon entry (modules/gateway/serve.go) hands the
// data plane's handler to `Root`, gets the root handler, gives it to http.Server. So
// "turn porthub off" is no special case - serve it ourselves (today) vs hand over to dispatch.
package porthub

import (
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"net/http"

	hub "github.com/rzbdz/newgate-ext/lib/porthub"
	modules "github.com/rzbdz/newgate/component"
)

// Service is the face consumers see: mount a row on the table (the web UI, say), or hand
// over this port's root handler (the data plane).
//
// The query half is not exposed - "who caught this path" is needed only where dispatch
// happens, and every extra asker adds one more "who comes before whom" question.
type Service interface {
	Mount(prefix, owner string, h http.Handler) (func(), error)

	// Root, see lib/porthub.Registry.Root: register the fallback service (the data plane)
	// and get back the root handler this port should use.
	Root(owner string, fallback http.Handler) (http.Handler, error)
}

// Capability is the fact "this mount table exists in this process" itself.
var Capability = modules.NewCapability[Service]("porthub")

// New declares the porthub component.
//
// No Requires, no listener, no logging, no Stop: Start runs in **every** `newgate ...`
// command (modules/i18n logged a line in Start and every command flooded - that lesson
// is recorded in its note), so all this does is "offer itself". Mounting is in-process
// registration, creating no socket and no goroutine, so a CLI process can call it safely.
func New() modules.Component {
	return modules.Component{
		Name: "porthub",
		Desc: func() string {
			return i18n.T("several services on one port, or a port of its own when there is no shared one", nil)
		},
		Type:     "infra",
		Provides: []modules.Provision{modules.Provide(Capability, Service(hub.Default()))},
	}
}
