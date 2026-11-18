package codex

// Lifting the codex client's **tool dialect** to where the upstream can understand it.
//
// This layer is the normalization between "the shape codex sends" and "the shape any
// upstream reads". It does **not** do model-side fixes (custom -> function and the like)
// -- that is crossings' business (see modules/codex_deepseek). Its whole job is: flatten
// the old shape where Codex 0.155 hangs a tool tree in `input[0].additional_tools`
// **byte for byte** onto the top-level `tools`, so the upstream sees the tools.
//
// Measured (2026-09-23, hitting real smt-ark / smt-deepseek /
// smt-claude / smt-gemini / smt-codex / kimi / minimax, 3/3 per cell):
//
//	top-level tools already present (non-empty array) -> upstream gets function_call
//	input[0].additional_tools                         -> upstream 400s (gemini/gpt/minimax),
//	                                                     or 200 with zero tool_call (ark/deepseek/kimi/claude)
//
// ark/claude/kimi returning 200 with no tool_call is the nastiest family -- it is **not
// an error**, and the client only gets a normal paragraph of text, so it "never successfully
// got tool_result". That is what this move fixes. The gemini/gpt/minimax 400 is more obvious,
// but the root cause is the same: the upstream **never** reads the `input[0]` position, only
// the top-level `tools`. This rule comes from the upstream's docs and measured behavior, not
// from Codex's expectations -- but since the upstream is like that, this layer follows it.

import (
	"bytes"

	rw "github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Inside this package, takeover.go uses `rewrite` as a **local function name**
// (the move that does TOML byte surgery; see there). Go's package namespace cannot
// hold both "the rewrite package" and "the rewrite function", so the alias `rw`
// is used here -- the same primitive functionally, a name that avoids the clash.

// ToolsItem is the `type` value of the item Codex puts when it places the tool tree in input[].
//
// It is **client dialect** -- codex's, **not** any upstream's, and not part of the
// Responses dialect. So it lives in modules/codex's constants, and not one word of it
// should appear in the kernel (the forwarding hot path does not even know "responses").
const ToolsItem = "additional_tools"

// ToolsInInput pulls Codex's tool tree (the nested shape) out of the request body.
//
// "Not found" is not an error -- Codex 0.155 also uses the new shape
// with a top-level `tools` (see HasUsableTopLevelTools), which is not what
// this move handles. Returning nil, false means "someone else's business".
func ToolsInInput(body []byte) ([]byte, bool) {
	input, ok := rw.TopLevelRaw(body, "input")
	if !ok {
		return nil, false
	}
	items, ok := rw.ArrayItems(input)
	if !ok {
		return nil, false
	}
	for _, item := range items {
		if kind, _ := rw.TopLevelString(item, "type"); kind == ToolsItem {
			arr, ok := rw.TopLevelRaw(item, "tools")
			return arr, ok
		}
	}
	return nil, false
}

// HasUsableTopLevelTools reports whether the top-level `tools` is already a **non-empty** array.
//
// Truth table (measured):
//
//	`"tools": [...]` (non-empty) -> use the upstream's (do not touch)
//	`"tools": []`               -> treat as absent, lift it up
//	`"tools": null`             -> same as above
//	absent                      -> same as above
//
// Why an empty array/null is treated as "absent": measured, Codex 0.155 writes the
// top-level `tools` as `null` or `[]` on requests that carry an `additional_tools` item
// (its old shape), and that case should be handled as "I want to use the nested one";
// in the new shape `tools` is always a **non-empty** array, which is the dividing line.
func HasUsableTopLevelTools(body []byte) bool {
	raw, has := rw.TopLevelRaw(body, "tools")
	if !has || len(raw) == 0 {
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	items, ok := rw.ArrayItems(trimmed)
	return ok && len(items) > 0
}

// FlattenToolTree flattens the nested tool tree (`namespace` containers that may
// appear in the `additional_tools` array) into a single top-level `tools` array.
//
// `namespace` on Codex's side is for client-side grouping (functions / collaboration);
// the upstream has no corresponding concept (measured: smt-deepseek / smt-ark read the `tools`
// array directly and neither error on nor expand a namespace -- "cannot see it" equals "the nested
// shape failed"). After flattening, every leaf is untouched byte for byte: items with an unknown
// `type` are kept as is too (let the tool types Codex newly adds go out, rather than us guessing).
//
// The cross-repo old-version question (the note in the kernel): whether
// Codex's `custom` tool kind is accepted upstream is **per upstream** --
// not this layer's business. This layer only "lifts up", never "downgrades".
//
// The return is (new array, leaf count). The leaf count is used for:
// (1) deciding "did anything change" -- 0 = forward as is, the same rule
// as reasoning; (2) telling the reader the truth when reporting notes.
func FlattenToolTree(arr []byte) ([]byte, int) {
	items, ok := rw.ArrayItems(arr)
	if !ok {
		return nil, 0
	}
	leaves := collectLeaves(items)
	if len(leaves) == 0 {
		return nil, 0
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, leaf := range leaves {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(leaf)
	}
	buf.WriteByte(']')
	return buf.Bytes(), len(leaves)
}

// collectLeaves collects leaves recursively. namespace
// containers are descended into, other types are collected as is.
func collectLeaves(items [][]byte) [][]byte {
	out := make([][]byte, 0, len(items))
	for _, item := range items {
		kind, _ := rw.TopLevelString(item, "type")
		if kind == "namespace" {
			nested, ok := rw.TopLevelRaw(item, "tools")
			if !ok {
				out = append(out, item) // unrecognized shape: treat it as a leaf
				continue
			}
			nestedItems, ok := rw.ArrayItems(nested)
			if !ok {
				out = append(out, item)
				continue
			}
			out = append(out, collectLeaves(nestedItems)...)
			continue
		}
		out = append(out, item)
	}
	return out
}

// SetTopLevelTools writes the `tools` field as this run's lifted array.
//
// Truth table (measured Codex 0.155.1):
//
//	absent            -> Insert (this is really "I want to fill it in")
//	`"tools":null`    -> Replace (the key is already there, only the value is null; not absent)
//	`"tools":[]`      -> Replace
//	`"tools":[...]`   -> Replace
//
// Why "null / [] go Replace" rather than "Insert as absent": InsertTopLevelRaw reports
// "the top level already has this key" when the key exists -- so "treat as absent"
// can only be decided at the Apply layer; writing here must faithfully Replace.
//
// Errors are always propagated as is: the caller decides fail-open whether to forward as is.
func SetTopLevelTools(body, tools []byte) ([]byte, error) {
	if _, has := rw.TopLevelRaw(body, "tools"); has {
		return rw.ReplaceTopLevelRaw(body, "tools", tools)
	}
	return rw.InsertTopLevelRaw(body, "tools", tools)
}

// noteLifted reports "lifted N tools". N is the leaf
// count -- `namespace` containers are not counted.
//
// "Did anything get lifted" looks at the leaf count, not "was anything downgraded":
// even with not one custom tool (all native function), as long as they hang in input[0] the
// model sees not one tool. What is fixed is this step itself; downgrading is another layer's
// (crossings') other matter -- mixing the two in one note would, when "not one custom exists",
// turn that sentence into "lifted N, 0 downgraded", where the extra "0" is not noise but
// the information "no tool field was touched this time"; a separate sentence is more robust.
func noteLifted(n int) string {
	return i18n.N(
		"lifted {n} tool declaration from input[0] to the top level (most upstreams read tools only there)",
		"lifted {n} tool declarations from input[0] to the top level (most upstreams read tools only there)",
		n, i18n.A{"n": n})
}
