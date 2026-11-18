package thinking

import (
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	modules "github.com/rzbdz/newgate/component"
)

// Service provides model-agnostic thinking downgrade operations.
// Model plugins depend on this port, not the other way round on thinking's internals.
type Service interface {
	BestEffortDisable(body []byte, request *special.Request) ([]byte, []string, error)
}

// Capability identifies the single thinking policy service.
var Capability = modules.NewCapability[Service]("thinking")
