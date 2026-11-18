package special

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type orderedTestPlugin struct {
	name   string
	before []string
	after  []string
}

func (plugin orderedTestPlugin) Name() string        { return plugin.name }
func (plugin orderedTestPlugin) Why() string         { return "order test" }
func (plugin orderedTestPlugin) Match(*Request) bool { return false }
func (plugin orderedTestPlugin) Apply(body []byte, _ *Request) ([]byte, []string, error) {
	return body, nil, nil
}
func (plugin orderedTestPlugin) Before() []string { return plugin.before }
func (plugin orderedTestPlugin) After() []string  { return plugin.after }

func TestOrderPluginsUsesNamedDependencies(t *testing.T) {
	plugins := orderPlugins([]Plugin{
		orderedTestPlugin{name: "model"},
		orderedTestPlugin{name: "client", before: []string{"model"}},
		orderedTestPlugin{name: "translator", after: []string{"model"}},
	})
	if got, want := pluginNames(plugins), []string{"client", "model", "translator"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestOrderPluginsRejectsCycle(t *testing.T) {
	defer func() {
		recovered := recover()
		if recovered == nil || !strings.Contains(fmt.Sprint(recovered), "cycle") {
			t.Fatalf("panic = %v, want cycle", recovered)
		}
	}()
	orderPlugins([]Plugin{
		orderedTestPlugin{name: "a", after: []string{"b"}},
		orderedTestPlugin{name: "b", after: []string{"a"}},
	})
}
