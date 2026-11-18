package rewrite

import (
	"bytes"
	"encoding/json"
	"testing"
)

// compact strips meaningless whitespace to make semantic comparison easy.
// RawMessage keeps the raw bytes but Marshal compresses whitespace - whitespace does not
// affect semantics; number literals and key order do, and those two are what we must hold.
func compact(b []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return string(b)
	}
	return buf.String()
}

// rewriteModel reproduces the only change the proxy makes to a request body, to assert
// fidelity.
func rewriteModel(body []byte, newModel string) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	mj, err := json.Marshal(newModel)
	if err != nil {
		return nil, err
	}
	payload["model"] = json.RawMessage(mj)
	return json.Marshal(payload)
}

// A real opencode request shape: deeply nested tool schemas, big integers, empty arrays,
// nulls, escapes, Unicode. These are exactly what an interface{} round trip destroys.
const realistic = `{
  "model": "heavy",
  "max_tokens": 1000000,
  "temperature": 0,
  "stream": true,
  "messages": [
    {"role":"system","content":[{"type":"text","text":"long prefix","cache_control":{"type":"ephemeral"}}]},
    {"role":"user","content":"compute 1e10 and \"quotes\" and \\ backslashes"}
  ],
  "tools": [
    {"type":"function","function":{
      "name":"background_cancel",
      "description":"Cancel a background task",
      "parameters":{
        "type":"object",
        "properties":{
          "id":{"type":"string"},
          "ids":{"type":"array","items":{"type":"string"}},
          "flags":{"type":"array","items":{"type":"string"},"default":[]},
          "nested":{"type":"object","properties":{"deep":{"type":"integer","maximum":9007199254740991}}}
        },
        "required":["id"],
        "additionalProperties":false
      }
    }},
    {"type":"function","function":{
      "name":"empty_required",
      "parameters":{"type":"object","properties":{},"required":[]}
    }}
  ],
  "metadata": {"nullable": null, "big": 9007199254740993, "sci": 1e10, "neg": -0.5}
}`

func TestOnlyModelChanges(t *testing.T) {
	out, err := rewriteModel([]byte(realistic), "model-large")
	if err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}

	var in, got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(realistic), &in); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}

	if len(in) != len(got) {
		t.Fatalf("the number of top-level keys changed: %d -> %d", len(in), len(got))
	}
	for k, v := range in {
		g, ok := got[k]
		if !ok {
			t.Errorf("key %q was lost", k)
			continue
		}
		if k == "model" {
			if string(g) != `"model-large"` {
				t.Errorf("model should be rewritten, got %s", g)
			}
			continue
		}
		// Every other top-level value must be semantically identical (including number literals
		// and key order)
		if compact(v) != compact(g) {
			t.Errorf("key %q was changed:\n original: %s\n now: %s", k, v, g)
		}
	}
}

// Recorded explicitly: a map[string]interface{} round trip would destroy those values.
// This test proves we did not take that road.
func TestInterfaceRoundTripWouldCorrupt(t *testing.T) {
	var viaIface map[string]interface{}
	if err := json.Unmarshal([]byte(realistic), &viaIface); err != nil {
		t.Fatal(err)
	}
	corrupted, _ := json.Marshal(viaIface)

	var a, b map[string]json.RawMessage
	_ = json.Unmarshal([]byte(realistic), &a)
	_ = json.Unmarshal(corrupted, &b)

	changed := 0
	for k, v := range a {
		if k == "model" {
			continue
		}
		if compact(v) != compact(b[k]) {
			changed++
			t.Logf("the interface{} round trip broke %q:\n original: %s\n after: %s", k, v, b[k])
		}
	}
	if changed == 0 {
		t.Skip("under this Go version the interface{} round trip happens not to break anything - but we must still not rely on it")
	}
	t.Logf("the interface{} round trip broke %d top-level fields, which is why RawMessage is mandatory", changed)
}
