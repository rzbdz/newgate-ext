package modellist

import (
	"context"
	"time"

	modules "github.com/rzbdz/newgate/component"
)

// Name is this module's name in the assembly table and in `newgate plugin` (= the directory name).
const Name = "modellist"

// Endpoint is the name it registers on the web BFF: `POST /ui/api/ext/modellist`.
//
// A machine marker, not translated -- that is what `modelList()` in the front end's `api.ts` uses.
const Endpoint = "modellist"

// Model is one model listed by an upstream.
//
// ID is **the name the upstream recognizes** (it is what bindings write); Name is only a long name
// for humans (some upstreams return `display_name`, some do not). Group is the upstream's own
// grouping -- empty when absent, and the UI draws it as "one group".
type Model struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Group string `json:"group,omitempty"`
}

// Catalog is the answer to one "ask the upstream for its model list".
//
// # Supported being false is not an error
//
// Most upstreams have no `GET /models`, or this key is not allowed to list models. That is a
// **normal answer**: the UI falls back to the list declared in providers.json, and writes Note at
// the top of the menu. Only a real failure (providers.json unreadable, provider name nonexistent)
// takes the error return.
type Catalog struct {
	Provider string `json:"provider"`
	// Supported = the upstream really gave a listing (Models non-empty).
	Supported bool `json:"supported"`
	// Adapter is the adapter that hit (`openai` / `anthropic`). A machine marker, not translated.
	Adapter string `json:"adapter,omitempty"`
	// Endpoint is the address actually asked (**without credentials**: the key is only in the request
	// headers).
	Endpoint string `json:"endpoint,omitempty"`
	// Models is an empty array rather than nil: the front end need not write two branches for "none"
	// and "field missing".
	Models []Model `json:"models"`
	// Groups are the groups that appeared (in first-appearance order). Omitted when there are none.
	Groups []string `json:"groups,omitempty"`
	// FetchedAt is when this listing was fetched (UTC).
	FetchedAt time.Time `json:"fetched_at"`
	// Cached = this one came from the in-process cache (the UI shows "cached" from that).
	Cached bool `json:"cached,omitempty"`
	// Note is the human-facing reason when Supported=false (already translated).
	Note string `json:"note,omitempty"`
}

// Options are List's switches.
type Options struct {
	// Refresh skips the cache and asks the upstream again (the ↻ button next to the dropdown).
	Refresh bool
}

// Service is the port other modules (maybe a CLI in the future) ask for model listings.
//
// It **does not know where the provider is stored**: the name goes in, the listing comes out. All
// the knowledge of reading providers.json (the key from the environment variable, both dialects'
// bases) stays in the implementation -- the same rule as config's Config port.
type Service interface {
	List(ctx context.Context, provider string, opts Options) (*Catalog, error)
}

// Capability is its name in the assembly graph.
var Capability = modules.NewCapability[Service]("modellist")
