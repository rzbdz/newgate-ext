// Package serving makes "this process has started serving" an **ordinary module**:
// its ledger is notified from one side by the module that owns the port (gateway),
// registered from the other by modules that want to work in the serving process.
//
// Why a module and not a line in the kernel: neither end of this edge should know the
// other. As a module the arrow is "gateway -> serving <- web-dashboard", nobody imports
// anybody; remove it and both degrade to "no serving callback" - the data plane serves
// as usual, the UI just loses its own-port route. Same shape as porthub (shared port).
//
// It **listens on nothing itself**: just a ledger. Real listening happens in the
// callbacks of registered modules - whose port, which number, only they know.
package serving

import (
	"context"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	"github.com/rzbdz/newgate-ext/lib/serving"
	modules "github.com/rzbdz/newgate/component"
)

type service struct{ *serving.Registry }

func New() modules.Component {
	registry := serving.NewRegistry()
	return modules.Component{
		Name: "serving",
		Desc: func() string { return i18n.T("the edge that says this process is serving", nil) },
		Type: "infra",
		// Not a single out-edge: both notifier and listener use it only optionally. That is
		// also the premise for removing it (see app/matrix_test.go's removal matrix).
		Provides: []modules.Provision{modules.Provide(serving.Capability, serving.Service(service{registry}))},
		Start:    func(context.Context, modules.Context) error { return nil },
		Stop:     func(context.Context) error { return nil },
	}
}
