package roleprov

import (
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
)

type registryProvider struct{ source string }

func (p registryProvider) Source() string { return p.source }
func (registryProvider) Roles() ([]domain.ExtraRole, error) {
	return nil, nil
}

func TestInstallDefaultRestoresOnlyItsOwnRegistry(t *testing.T) {
	original := currentRegistry()
	first := NewRegistry()
	restoreFirst := InstallDefault(first)
	second := NewRegistry()
	restoreSecond := InstallDefault(second)
	restoreFirst()
	if currentRegistry() != second {
		t.Fatal("stale restore replaced the current registry")
	}

	restoreSecond()
	if currentRegistry() != first {
		t.Fatal("nested registry did not restore its predecessor")
	}
	restoreFirst()
	if currentRegistry() != original {
		t.Fatal("original registry was not restored")
	}
}

func TestRegisterReleaseOwnsOnlyItsRegistration(t *testing.T) {
	registry := NewRegistry()
	releaseFirst, err := registry.Register(registryProvider{source: "owned"})
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.providers) != 1 {
		t.Fatal("registered provider is not visible")
	}
	if err := releaseFirst(); err != nil {
		t.Fatal(err)
	}
	if len(registry.providers) != 0 {
		t.Fatal("released provider remains visible")
	}

	releaseSecond, err := registry.Register(registryProvider{source: "owned"})
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseFirst(); err != nil {
		t.Fatal(err)
	}
	if len(registry.providers) != 1 {
		t.Fatal("stale release removed a newer registration")
	}
	if err := releaseSecond(); err != nil {
		t.Fatal(err)
	}
}
