package codex_deepseek

import (
	"bytes"

	"github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
)

// Tools is the type name of the input item where Codex puts tool definitions.
//
// It is **the client's dialect**, not an upstream field name -- so it lives here, and not a word
// of it should appear in the kernel (the forwarding hot path does not even know "responses").
const Tools = "additional_tools"

// ApplyPatch is the only custom tool name the upstream natively supports.
//
// Measured (2026-09-21, against real smt-deepseek/deepseek-flash): sending another
// custom tool as is returns 400 `Unsupported custom tool: 'exec'. Only 'apply_patch'
// is supported.` So apart from it, every custom is downgraded to function.
const ApplyPatch = "apply_patch"

// freeformParams is the parameter table added to a downgraded function.
//
// It must exist: OpenAI's function tools **require** parameters, while
// custom tools have no such field. The shape is a single string parameter -- a
// custom tool's input is a whole free-form block of text anyway (Codex's exec takes
// a piece of JavaScript), and putting it in an `input` field is the semantically
// closest expression; the model also sees at a glance "put the original text here".
var freeformParams = []byte(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`)

// liftTools lifts Codex's tool tree into the top-level tools array DeepSeek recognizes.
//
// Returns (new array, names of downgraded tools, whether anything was lifted).
//
// **The lift itself is the step that fixes the root cause**; the downgrade is incidental: even
// with not one custom (all native function), as long as they hang in input[0] the model sees
// not a single tool. So "was anything lifted" counts **leaves**, not "was anything downgraded".
//
// What is lifted is a **two-level structure**: `namespace` (Codex's grouping, e.g. functions
// / collaboration) has no upstream counterpart and is flattened into the same array -- tool
// names are globally unique anyway, and the grouping is only for humans. After flattening,
// each leaf is either as is (native function) or downgraded (custom -> function).
//
// Byte surgery throughout: the bytes of leaf elements are untouched verbatim except
// for the few that were downgraded (one field value changed, one field added).
// The reasoning rule holds here too -- if it can be left alone, leave it alone.
// degradeTopLevel handles the shape where tools already sit in the top-level tools.
//
// Two sources (see the Apply comment): old Codex
// hangs tools in input[0].additional_tools, new Codex puts them directly in the top-level
// tools (including namespace groups and web_search). The top-level copy is what DeepSeek
// already reads, so it **needs no lift**, but it may still hold a `custom` -- and DeepSeek
// knows only one custom, apply_patch; any other gets a 400. So the only thing done to
// the top-level copy: downgrade non-apply_patch customs to function. The rest untouched.
//
// **Inside a namespace is scanned too**: when `custom` hides in a namespace the upstream
// reports a **different** 400 -- `Currently custom tools are not allowed inside a namespace.
// Found custom tool 'exec' in tools[1].tools[0].` (measured 2026-09-21 against real
// deepseek-flash). A version that looks only at the top level misses this family, and the
// consequence is exactly the same as the top-level one: 400, switch along the chain. So there
// is only one criterion -- "`type: "custom"` and not apply_patch" -- word for word the same
// as liftInto; the two places cannot each have their own notion of "what counts as custom".
//
// Returns: the rewritten array, the downgraded tool names, whether anything was
// actually changed. With no custom, changed is false, and the caller forwards as is
// based on that -- "do not touch what needs no fixing", the same rule as reasoning.
func degradeTopLevel(arr []byte) (out []byte, degraded map[string]bool, changed bool) {
	degraded = map[string]bool{}
	out, touched := degradeLevel(arr, degraded)
	return out, degraded, touched
}

// degradeLevel downgrades the customs in one level of an array, recursing into namespaces.
//
// The recursion only follows the namespace container: it is Codex's
// only grouping structure, and the spot the upstream's "not allowed inside a
// namespace" names. Any other type (function / web_search / ones added later)
// is left as is -- an unrecognized shape is not touched, same as liftInto.
func degradeLevel(arr []byte, degraded map[string]bool) (out []byte, changed bool) {
	items, ok := rewrite.ArrayItems(arr)
	if !ok {
		return arr, false
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	touched := false
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		kind, _ := rewrite.TopLevelString(item, "type")
		switch kind {
		case "namespace":
			nested, ok := rewrite.TopLevelRaw(item, "tools")
			if !ok {
				buf.Write(item)
				continue
			}
			inner, innerChanged := degradeLevel(nested, degraded)
			if !innerChanged {
				buf.Write(item) // nothing inside to change -> this entry stays byte-identical
				continue
			}
			rewritten, err := rewrite.ReplaceTopLevelRaw(item, "tools", inner)
			if err != nil {
				buf.Write(item) // fail-open: if it cannot be rewritten, send the original as-is
				continue
			}
			touched = true
			buf.Write(rewritten)
		case "custom":
			name, _ := rewrite.TopLevelString(item, "name")
			if name == "" || name == ApplyPatch {
				buf.Write(item)
				continue
			}
			conv, err := toFunction(item)
			if err != nil {
				buf.Write(item)
				continue
			}
			degraded[name] = true
			touched = true
			buf.Write(conv)
		default:
			buf.Write(item)
		}
	}
	buf.WriteByte(']')
	return buf.Bytes(), touched
}

// liftTools lifts Codex's tool tree into the top-level tools array DeepSeek recognizes.
//
// Returns (new array, names of downgraded tools, number of leaves lifted).
//
// **The lift itself is the step that fixes the root cause**; the downgrade
// is incidental: even with not one custom (all native function), as long as they hang in
// input[0] the model sees not a single tool. So the number in the return is **leaves**,
// not "how many were downgraded" -- the caller uses it to report a true sentence
// ("lifted N"), and to decide "was anything lifted" (0 = nothing lifted, forward as is).
//
// What is lifted is a **two-level structure**: `namespace` (Codex's grouping, e.g. functions
// / collaboration) has no upstream counterpart and is flattened into the same array -- tool
// names are globally unique anyway, and the grouping is only for humans. After flattening,
// each leaf is either as is (native function) or downgraded (custom -> function).
//
// Byte surgery throughout: the bytes of leaf elements are untouched verbatim except
// for the few that were downgraded (one field value changed, one field added).
// The reasoning rule holds here too -- if it can be left alone, leave it alone.
func liftTools(arr []byte) (out []byte, degraded map[string]bool, lifted int) {
	degraded = map[string]bool{}
	leaves := liftInto(nil, arr, degraded)
	if len(leaves) == 0 {
		return nil, degraded, 0
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
	return buf.Bytes(), degraded, len(leaves)
}

// liftInto recursively flattens the tool tree, collecting each
// leaf into dst and recording downgraded tool names along the way.
//
// When a single leaf fails to downgrade (unrecognized shape) it is **kept as is**
// and the loop goes on: one bad tool should not throw away the whole tool table --
// that would hide even the other tools from the model, far worse than sending one
// declaration it may not understand (fail-open, the same rule as the plugin layer).
//
// An unrecognized type is kept as is too (the default branch): when Codex adds a
// new tool type later, what is lifted is the text it wrote, not a shape we guessed.
func liftInto(dst [][]byte, arr []byte, degraded map[string]bool) [][]byte {
	items, ok := rewrite.ArrayItems(arr)
	if !ok {
		return dst
	}
	for _, item := range items {
		kind, _ := rewrite.TopLevelString(item, "type")
		switch kind {
		case "namespace":
			nested, ok := rewrite.TopLevelRaw(item, "tools")
			if !ok {
				dst = append(dst, item)
				continue
			}
			dst = liftInto(dst, nested, degraded)
		case "custom":
			name, _ := rewrite.TopLevelString(item, "name")
			if name == "" || name == ApplyPatch {
				dst = append(dst, item) // upstream natively understands this one, leave it alone
				continue
			}
			conv, err := toFunction(item)
			if err != nil {
				dst = append(dst, item)
				continue
			}
			degraded[name] = true
			dst = append(dst, conv)
		default:
			dst = append(dst, item)
		}
	}
	return dst
}

// toFunction rewrites a custom declaration into a function declaration.
//
// Only two things change: the value of `type`, and an added `parameters`.
// description stays as is -- it is what the model picks tools by
// (Codex's exec description reads like documentation, and it is useful).
func toFunction(item []byte) ([]byte, error) {
	out, err := rewrite.ReplaceTopLevelString(item, "type", "function")
	if err != nil {
		return nil, err
	}
	if _, has := rewrite.TopLevelRaw(out, "parameters"); has {
		return out, nil
	}
	return rewrite.InsertTopLevelRaw(out, "parameters", freeformParams)
}

// codexTools pulls the tool array of the additional_tools item out of the client request.
//
// Returns false when not found -- "this request is not the
// Codex shape" is not an error, it is "not mine to handle".
func codexTools(body []byte) ([]byte, bool) {
	input, ok := rewrite.TopLevelRaw(body, "input")
	if !ok {
		return nil, false
	}
	items, ok := rewrite.ArrayItems(input)
	if !ok {
		return nil, false
	}
	for _, item := range items {
		if kind, _ := rewrite.TopLevelString(item, "type"); kind == Tools {
			arr, ok := rewrite.TopLevelRaw(item, "tools")
			return arr, ok
		}
	}
	return nil, false
}

// setTopLevelTools writes the top-level `tools` as the array lifted this time.
//
// In the client's request `tools` is usually **absent** or `null` (measured: 0.155.1
// showed both), so both insertion and replacement must be handled. This distinction cannot be
// flattened with "insert then replace": InsertTopLevelRaw errors outright on an existing key,
// and that error would be taken by the upper layer as "this move of this plugin did not run".
func setTopLevelTools(body, tools []byte) ([]byte, error) {
	if _, has := rewrite.TopLevelRaw(body, "tools"); has {
		return rewrite.ReplaceTopLevelRaw(body, "tools", tools)
	}
	return rewrite.InsertTopLevelRaw(body, "tools", tools)
}
