package confighook

import (
	"testing"
)

func TestRegistryReleaseRemovesOwnedExtensions(t *testing.T) {
	registry := &registry{
		agents: make(map[string]*Agent),
		fields: make(map[string]string),
		tokens: make(map[string]uint64),
	}
	releaseAgent, err := registry.RegisterAgent(&Agent{ID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	releaseField, err := registry.RegisterStateField("test", "field")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("test"); !ok {
		t.Fatal("registered agent is not visible")
	}
	if err := releaseField(); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.fields["field"]; ok {
		t.Fatal("released state field remains visible")
	}
	if err := releaseAgent(); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("test"); ok {
		t.Fatal("released agent remains visible")
	}
}

func TestRegistryReturnsAgentCopy(t *testing.T) {
	registry := &registry{
		agents: make(map[string]*Agent),
		fields: make(map[string]string),
		tokens: make(map[string]uint64),
	}
	_, err := registry.RegisterAgent(&Agent{
		ID:       "test",
		Bin:      []string{"one"},
		UnsetEnv: []string{"A"},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := registry.Get("test")
	agent.Bin[0] = "changed"
	agent.UnsetEnv[0] = "B"
	again, _ := registry.Get("test")
	if again.Bin[0] != "one" || again.UnsetEnv[0] != "A" {
		t.Fatal("consumer mutated the registry's agent")
	}
}
