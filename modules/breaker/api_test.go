package breaker

import (
	"reflect"
	"strings"
	"testing"
)

// These two pin **the shape of the port**, not behavior: what the health
// table lends outside is one thing, and how it wires itself internally is another;
// mixing them on one interface is the beginning of "anyone can quietly change it".
//
// It came out of a real review (2026-09-21). `Breaker` once hung three setters
// -- `SetPolicy` / `SetErrorHandler` / `SetVerifier` -- and **not one place** in the
// repo called them through this interface (the call sites were all in the package, on a
// concrete `*table`). The extra part was not neutral: `SetVerifier` is the switch for the
// active diagnostic added before tripping, and one `SetVerifier(nil)` removes it, with
// no error, no edge on the dependency graph, and nothing in review showing anyone touched
// it. And `SetPolicy` has **zero callers**, an API reserved for a feature not yet built.
//
// The test is "one capability should have one door, and the door belongs to the
// module that owns it": the data plane's runtime capability lent to policy goes
// through `policy.EnvBinder.BindEnv` (that seam is owned and directed, with
// clear timing), not through a write door on the port that anyone can push.

// TestThePortCarriesNoUnownedSetters asserts there is not one setter on the public port.
//
// It tests by name prefix rather than comparing against a whitelist: a whitelist
// needs an edit every time a legitimate method is added, and after enough edits it
// turns into "add a line when this goes red" -- by then it is no longer a ratchet.
// The prefix is a naming convention, not a proof, so the real test is written
// in the comment above; this test pins **the shape** (a verb that outsiders can
// change), and the shape is exactly the only stable feature of this anti-pattern.
func TestThePortCarriesNoUnownedSetters(t *testing.T) {
	typ := reflect.TypeOf((*Breaker)(nil)).Elem()
	names := make([]string, 0, typ.NumMethod())
	for i := 0; i < typ.NumMethod(); i++ {
		names = append(names, typ.Method(i).Name)
	}
	for _, n := range names {
		if strings.HasPrefix(n, "Set") {
			t.Errorf("%s is on the public port: it is a write door anyone can push.\n"+
				"to wire into it, use BindEnv (policy.EnvBinder), or leave the method on *table -\n"+
				"for the reason see the 'why this interface is read-mostly' passage in api.go.", n)
		}
	}
	// A guard against an empty table making the loop above
	// vacuously green: the port cannot have zero methods.
	if len(names) == 0 {
		t.Fatal("reflection read no methods - the loop above is a no-op")
	}
}

// TestTheWiringMethodsStillExistOnTheTable is the other half of the previous test.
//
// Cutting the port is **not** cutting the capability: those three methods are still
// there, and `BindEnv` uses them to wire the log outlet and the pre-trip diagnostic back into the
// health table. Only one test watches "no setters on the port", so the next refactor might delete
// the implementation along with it -- and after deleting it compilation still passes (nobody calls
// them through the port), with the symptom that the pre-trip diagnostic **silently** stops running:
// bad bindings are still delisted, just without that "ask for active evidence one more time" step.
func TestTheWiringMethodsStillExistOnTheTable(t *testing.T) {
	concrete := reflect.TypeOf(&table{})
	for _, name := range []string{"SetPolicy", "SetErrorHandler", "SetVerifier"} {
		if _, ok := concrete.MethodByName(name); !ok {
			t.Errorf("%s is gone from *table - narrowing the port must not cut the wiring method too,"+
				"BindEnv still needs it (see api.go)", name)
		}
	}
}
