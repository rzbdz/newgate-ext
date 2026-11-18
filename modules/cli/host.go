package cli

import (
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
)

// moduleCLIHost exposes rendering and daemon notification primitives to
// module-owned commands without importing concrete modules into the CLI.
//
// verb is **by which name the dispatcher found this command this time**
// (see cliapi.Host.Verb). It is decided at the moment of dispatch, so the
// host is a short-lived value, **one per invocation**, not a singleton.
type moduleCLIHost struct{ verb string }

func (moduleCLIHost) Die(code int, message string) int { return die(code, message) }

func (h moduleCLIHost) Verb() string { return h.verb }

func (moduleCLIHost) NotifyProxy() { notifyProxy() }

// DaemonRunning: is the daemon running now.
//
// It goes through the control-plane leaf's State() (which reads the pidfile), rather
// than importing runtime/daemon directly: the UI **does not know runtime**, and "how
// to tell whether a process is there" is between the data plane and the control plane.
func (moduleCLIHost) DaemonRunning() bool {
	info, _ := controlplane.State()
	return info != nil
}
