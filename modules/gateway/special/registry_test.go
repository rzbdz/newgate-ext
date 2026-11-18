package special

import "testing"

type registryPlugin struct{ name string }

func (p registryPlugin) Name() string { return p.name }
func (registryPlugin) Why() string    { return "test" }
func (registryPlugin) Match(*Request) bool {
	return true
}
func (registryPlugin) Apply(body []byte, _ *Request) ([]byte, []string, error) {
	return body, nil, nil
}

func TestInstallDefaultRestoresOnlyItsOwnRegistry(t *testing.T) {
	original := currentRegistry()
	first := NewRegistry()
	restoreFirst := InstallDefault(first)
	if currentRegistry() != first {
		t.Fatal("first registry was not installed")
	}

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
	releaseFirst, err := registry.Register(registryPlugin{name: "owned"})
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.plugins) != 1 {
		t.Fatal("registered plugin is not visible")
	}
	if err := releaseFirst(); err != nil {
		t.Fatal(err)
	}
	if len(registry.plugins) != 0 {
		t.Fatal("released plugin remains visible")
	}

	releaseSecond, err := registry.Register(registryPlugin{name: "owned"})
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseFirst(); err != nil {
		t.Fatal(err)
	}
	if len(registry.plugins) != 1 {
		t.Fatal("stale release removed a newer registration")
	}
	if err := releaseSecond(); err != nil {
		t.Fatal(err)
	}
}
