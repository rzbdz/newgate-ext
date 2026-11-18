package kernel

import (
	"context"
	app "github.com/rzbdz/newgate/app"
	"testing"

	pluginmanagerapi "github.com/rzbdz/newgate-ext/modules/pluginmanager"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	modules "github.com/rzbdz/newgate/component"
)

// TestPluginMetadataIsConsistent guards two things without compile-time protection:
//
//  1. **Module name != component name**. RegisterSelf's name is the switch Path's prefix,
//     and newgate plugin reconciles against the component graph. A mismatch means the row
//     is never read - the list looks fine, just with empty switches for this module.
//  2. **A component's Type is empty**. resolve rejects only the empty value, not the value
//     (the kernel not knowing values is deliberate), so "empty" is the one thing caught.
//
// Same shape as TestGeneratedModuleListIsCurrent: both guard "forgot to register", both via
// a test rather than the type system.
//
// Deliberately **not** checking Type in DisplayOrder: unknown classifications go to others -
// correct degradation, not an error; classification is a product concept that grows, and
// hard refusal would crash all of newgate plugin over one new word.
func TestPluginMetadataIsConsistent(t *testing.T) {
	testkit.Sandbox(t)
	built, err := app.New(context.Background(), staticLoader(kernelGraph(t)))
	if err != nil {
		t.Fatalf("assembly failed: %v", err)
	}
	t.Cleanup(func() { _ = built.Stop(context.Background()) })

	components := built.Components()
	inGraph := map[string]bool{}
	for _, c := range components {
		if c.Type == "" {
			t.Fatalf("component %q has no Type -- resolve should have caught it at assembly time", c.Name)
		}
		inGraph[c.Name] = true
	}
	if len(components) == 0 {
		t.Fatal("the component graph is empty")
	}

	pm := modules.MustGet(built.Context(), pluginmanagerapi.Capability)
	listed := pm.Modules()
	if len(listed) < len(components) {
		t.Fatalf("newgate plugin lists only %d modules, the graph has %d -- the enumeration source must be the graph, not the registry",
			len(listed), len(components))
	}
	for _, m := range listed {
		if !inGraph[m.Name] {
			t.Fatalf("module %q reported itself (switch point %d), but that component name is not in the graph -- "+
				"the name passed to RegisterSelf must match Component.Name, otherwise those switch points will never be read",
				m.Name, len(m.Switches))
		}
	}
}
