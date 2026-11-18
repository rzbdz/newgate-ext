package agentstate

import (
	"sync"

	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
)

var (
	mu           sync.RWMutex
	catalog      confighookapi.AgentCatalog
	currentOwner *owner
)

type owner struct{}

func Set(next confighookapi.AgentCatalog) func() {
	mu.Lock()
	previous := catalog
	previousOwner := currentOwner
	token := &owner{}
	catalog = next
	currentOwner = token
	mu.Unlock()
	return func() {
		mu.Lock()
		if currentOwner == token {
			catalog = previous
			currentOwner = previousOwner
		}
		mu.Unlock()
	}
}

func Catalog() confighookapi.AgentCatalog {
	mu.RLock()
	current := catalog
	mu.RUnlock()
	if current == nil {
		panic("runtime: agent catalog capability is unavailable")
	}
	return current
}
