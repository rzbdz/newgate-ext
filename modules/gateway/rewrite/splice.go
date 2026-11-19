package rewrite

import (
	"encoding/json"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// ReplaceTopLevelString locates the string value of a top-level key in the raw JSON bytes
// and replaces only that span, keeping **every other byte** as is - including key order,
// indentation, whitespace, number literals and Unicode escape forms.
//
// Why an Unmarshal/Marshal round trip cannot be used:
//   - map[string]interface{} turns integers into float64 (9007199254740993 → ...92), turns
//     1e10 into 10000000000, and reorders every key
//   - map[string]json.RawMessage keeps the nested bytes, but top-level keys are still
//     reordered and whitespace still compressed
//
// Forwarding a request should be forwarding, not rewriting it on the way.
func ReplaceTopLevelString(body []byte, key, newVal string) ([]byte, error) {
	start, end, err := findTopLevelStringValue(body, key)
	if err != nil {
		return nil, err
	}
	quoted, err := json.Marshal(newVal)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(body)-(end-start)+len(quoted))
	out = append(out, body[:start]...)
	out = append(out, quoted...)
	out = append(out, body[end:]...)
	return out, nil
}

// ReplaceTopLevelRaw replaces the whole value of a top-level key with newRaw (raw JSON
// bytes), keeping every other byte as is. Used to rewrite tools without touching messages.
func ReplaceTopLevelRaw(body []byte, key string, newRaw []byte) ([]byte, error) {
	start, end, err := findTopLevelValue(body, key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(body)-(end-start)+len(newRaw))
	out = append(out, body[:start]...)
	out = append(out, newRaw...)
	out = append(out, body[end:]...)
	return out, nil
}

// TopLevelRaw reads out the raw value bytes of a top-level key.
func TopLevelRaw(body []byte, key string) ([]byte, bool) {
	s, e, err := findTopLevelValue(body, key)
	if err != nil {
		return nil, false
	}
	return body[s:e], true
}

// InsertTopLevelRaw **adds** a key to the top-level object, inserted at the start (right
// after `{`), keeping every other byte as is. It errors if a key of that name already
// exists - the caller must check with TopLevelRaw first and then decide between insert and
// ReplaceTopLevelRaw, never silently produce a duplicate key.
//
// Why insert at the start rather than the end: the end means handling trailing whitespace,
// newlines, and "what sits between the last value and `}`", with a high chance of error;
// the start only needs to check whether the object is empty.
func InsertTopLevelRaw(body []byte, key string, raw []byte) ([]byte, error) {
	if _, _, err := findTopLevelValue(body, key); err == nil {
		return nil, errDupKey
	}
	i := skipWS(body, 0)
	if i >= len(body) || body[i] != '{' {
		return nil, errNotObject
	}
	j := skipWS(body, i+1)
	if j >= len(body) {
		return nil, errNotObject
	}
	quoted, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	ins := make([]byte, 0, len(quoted)+len(raw)+2)
	ins = append(ins, quoted...)
	ins = append(ins, ':')
	ins = append(ins, raw...)
	if body[j] != '}' { // the object is non-empty, so a comma is needed
		ins = append(ins, ',')
	}
	out := make([]byte, 0, len(body)+len(ins))
	out = append(out, body[:i+1]...)
	out = append(out, ins...)
	out = append(out, body[i+1:]...)
	return out, nil
}

// EnsureArrayItemField adds a field to every object element of a top-level array (messages
// and the like) that matches match - **only when that element does not have the field yet**;
// existing ones are never touched.
//
// Still pure byte surgery: collect every insertion point first, then splice in one go.
// Apart from the inserted spans, each message's content blocks, cache_control, Unicode
// escape forms and indentation stay byte-for-byte. That is a hard requirement: a full JSON
// round trip over the body would reorder keys, turn big integers into float64 and compress
// whitespace - forwarding a request should be forwarding.
//
// match receives this element's complete raw bytes (one JSON object), so it can decide with
// TopLevelString(item, "role") and the like.
// It returns how many were added; for "the array is absent or the element is not an object"
// it returns 0 without error - see err.
func EnsureArrayItemField(body []byte, arrayKey, field string, rawVal []byte,
	match func(item []byte) bool) ([]byte, int, error) {

	return EnsureArrayItemFieldFunc(body, arrayKey, field,
		func([]byte) []byte { return rawVal }, match)
}

// EnsureArrayItemFieldFunc is EnsureArrayItemField with the value **computed per item**:
// val receives this element's raw bytes and returns the value to insert; nil skips this
// element.
//
// Why per item: when backfilling reasoning, each assistant message must get **its own
// round's** reasoning, not one constant. Inserting the same reasoning into every message
// hands someone else's thinking to this round - worse than an empty string.
func EnsureArrayItemFieldFunc(body []byte, arrayKey, field string,
	val func(item []byte) []byte, match func(item []byte) bool) ([]byte, int, error) {

	s, e, err := findTopLevelValue(body, arrayKey)
	if err != nil {
		return body, 0, err
	}
	arr := body[s:e]
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return body, 0, errNotArray
	}
	quoted, err := json.Marshal(field)
	if err != nil {
		return body, 0, err
	}

	type point struct {
		off   int    // absolute offset in body (right after the element's `{`)
		raw   []byte // value to insert for this element
		comma bool   // a comma is needed when the element is non-empty
	}
	var points []point

	for _, sp := range spans {
		item := arr[sp[0]:sp[1]]
		if len(item) == 0 || item[0] != '{' {
			continue // not an object: not a shape we handle, skip
		}
		if _, _, err := findTopLevelValue(item, field); err == nil {
			continue // already present, not a single byte is touched
		}
		if match != nil && !match(item) {
			continue
		}
		raw := val(item)
		if raw == nil {
			continue
		}
		j := skipWS(item, 1)
		points = append(points, point{
			off:   s + sp[0] + 1,
			raw:   raw,
			comma: j < len(item) && item[j] != '}',
		})
	}

	if len(points) == 0 {
		return body, 0, nil // nothing to insert: the caller keeps the original bytes
	}

	out := make([]byte, 0, len(body)+len(points)*(len(quoted)+16))
	prev := 0
	for _, p := range points {
		out = append(out, body[prev:p.off]...)
		out = append(out, quoted...)
		out = append(out, ':')
		out = append(out, p.raw...)
		if p.comma {
			out = append(out, ',')
		}
		prev = p.off
	}
	out = append(out, body[prev:]...)
	return out, len(points), nil
}

// EnsureArrayItemArrayHead inserts rawVal at the **start** of the **sub-array** field named
// field (content) of every object element of top-level array arrayKey (messages) that matches
// match, and only when need(childArr) is true. It returns how many were inserted.
//
// Why a separate primitive: EnsureArrayItemField can only add top-level fields to the
// element object and cannot reach the content array inside it. And in the Anthropic protocol
// thinking is one **block** of content[], and it must come before text / tool_use - position
// is part of the protocol, so it cannot just be appended at the end.
//
// Still pure byte surgery: collect every insertion point first, then splice once. Apart from
// the inserted spans, each block's cache_control, tool_use input, indentation and escape
// forms stay byte-for-byte.
//
// Skipped (neither an error nor a change): the element is not an object, it has no such
// field, or the field's value is not an array (that is the case when content is a plain
// string).
func EnsureArrayItemArrayHead(body []byte, arrayKey, field string, rawVal []byte,
	match func(item []byte) bool, need func(childArr []byte) bool) ([]byte, int, error) {

	return EnsureArrayItemArrayHeadFunc(body, arrayKey, field,
		func([]byte) []byte { return rawVal }, match, need)
}

// EnsureArrayItemArrayHeadFunc is the same, with the value computed per item (val returns
// nil = skip). Same reason as EnsureArrayItemFieldFunc: each assistant message must get its
// own round's thinking block.
func EnsureArrayItemArrayHeadFunc(body []byte, arrayKey, field string,
	val func(item []byte) []byte,
	match func(item []byte) bool, need func(childArr []byte) bool) ([]byte, int, error) {

	s, e, err := findTopLevelValue(body, arrayKey)
	if err != nil {
		return body, 0, err
	}
	arr := body[s:e]
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return body, 0, errNotArray
	}

	type point struct {
		off   int    // absolute offset in body (right after the sub-array's `[`)
		raw   []byte // block to insert for this element
		comma bool   // a comma is needed when the sub-array is non-empty
	}
	var points []point

	for _, sp := range spans {
		item := arr[sp[0]:sp[1]]
		if len(item) == 0 || item[0] != '{' {
			continue
		}
		if match != nil && !match(item) {
			continue
		}
		cs, ce, err := findTopLevelValue(item, field)
		if err != nil {
			continue // no content field
		}
		child := item[cs:ce]
		if len(child) == 0 || child[0] != '[' {
			continue // content is a string: no block to insert
		}
		if need != nil && !need(child) {
			continue
		}
		raw := val(item)
		if raw == nil {
			continue
		}
		j := skipWS(child, 1)
		points = append(points, point{
			off:   s + sp[0] + cs + 1,
			raw:   raw,
			comma: j < len(child) && child[j] != ']',
		})
	}

	if len(points) == 0 {
		return body, 0, nil
	}

	out := make([]byte, 0, len(body)+len(points)*32)
	prev := 0
	for _, p := range points {
		out = append(out, body[prev:p.off]...)
		out = append(out, p.raw...)
		if p.comma {
			out = append(out, ',')
		}
		prev = p.off
	}
	out = append(out, body[prev:]...)
	return out, len(points), nil
}

// AppendLastArrayItemArray appends rawVal to the end of the sub-array of the last object
// element of a top-level array. match confirms the last entry really is the shape the caller
// handles. Only that span of bytes is inserted; the rest of the request (big integers, field
// order, unknown fields) stays as is.
func AppendLastArrayItemArray(body []byte, arrayKey, field string, rawVal []byte,
	match func(item []byte) bool) ([]byte, bool, error) {
	s, e, err := findTopLevelValue(body, arrayKey)
	if err != nil {
		return body, false, err
	}
	arr := body[s:e]
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return body, false, errNotArray
	}
	if len(spans) == 0 {
		return body, false, nil
	}
	sp := spans[len(spans)-1]
	item := arr[sp[0]:sp[1]]
	if len(item) == 0 || item[0] != '{' || (match != nil && !match(item)) {
		return body, false, nil
	}
	cs, ce, err := findTopLevelValue(item, field)
	if err != nil {
		return body, false, err
	}
	child := item[cs:ce]
	if len(child) == 0 || child[0] != '[' {
		return body, false, errNotArray
	}
	close := len(child) - 1
	for close > 0 {
		switch child[close] {
		case ' ', '\t', '\r', '\n':
			close--
			continue
		}
		break
	}
	if child[close] != ']' {
		return body, false, errNotArray
	}
	j := skipWS(child, 1)
	ins := rawVal
	if j < len(child) && child[j] != ']' {
		ins = append([]byte(","), rawVal...)
	}
	off := s + sp[0] + cs + close
	out := make([]byte, 0, len(body)+len(ins))
	out = append(out, body[:off]...)
	out = append(out, ins...)
	out = append(out, body[off:]...)
	return out, true, nil
}

// AppendArrayItemArrayAt has the same shape as AppendLastArrayItemArray but picks the
// element by index, rather than "scanning backwards for the first match".
//
// Why it exists (field, 2026-09-17; rechecked 2026-09-18): deepseek's tail shape repair must
// anchor on the **last role:user message**, and its index may not be the array's last entry -
// Claude Code appends a role:"system" remark after a tool_result, and the last entry is that
// system one. Written as "scan backwards", it would modify a mid-history tool_result-only old
// message (such turns are everywhere) and stuff a sentence the user never said into the
// middle of the conversation.
func AppendArrayItemArrayAt(body []byte, arrayKey, field string, itemIndex int,
	rawVal []byte) ([]byte, bool, error) {
	s, e, err := findTopLevelValue(body, arrayKey)
	if err != nil {
		return body, false, err
	}
	arr := body[s:e]
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return body, false, errNotArray
	}
	if itemIndex < 0 || itemIndex >= len(spans) {
		return body, false, nil
	}
	sp := spans[itemIndex]
	item := arr[sp[0]:sp[1]]
	if len(item) == 0 || item[0] != '{' {
		return body, false, nil
	}
	cs, ce, err := findTopLevelValue(item, field)
	if err != nil {
		return body, false, err
	}
	child := item[cs:ce]
	if len(child) == 0 || child[0] != '[' {
		return body, false, errNotArray
	}
	close := len(child) - 1
	for close > 0 {
		switch child[close] {
		case ' ', '\t', '\r', '\n':
			close--
			continue
		}
		break
	}
	if child[close] != ']' {
		return body, false, errNotArray
	}
	j := skipWS(child, 1)
	ins := rawVal
	if j < len(child) && child[j] != ']' {
		ins = append([]byte(","), rawVal...)
	}
	off := s + sp[0] + cs + close
	out := make([]byte, 0, len(body)+len(ins))
	out = append(out, body[:off]...)
	out = append(out, ins...)
	out = append(out, body[off:]...)
	return out, true, nil
}

// AppendTopLevelArrayItem appends one element to **the top-level array**, keeping every
// other byte as is.
//
// The difference from AppendLastArrayItemArray / AppendArrayItemArrayAt is "which array":
// those insert into an array field **inside an element** (messages[i].content), while this
// one inserts into the top-level array itself. The tail shape repair of the Responses dialect
// (Codex) needs exactly the latter - a new top-level input[] item, not a block in some item's
// content.
//
// An empty array gets no comma (`[` right before `]` would yield `[,x]`, which is not valid
// JSON); a non-empty array inserts before the closing `]`, skipping whitespace before it, so
// the existing indentation and newlines stay verbatim.
func AppendTopLevelArrayItem(body []byte, arrayKey string, rawVal []byte) ([]byte, bool, error) {
	s, e, err := findTopLevelValue(body, arrayKey)
	if err != nil {
		return body, false, err
	}
	arr := body[s:e]
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return body, false, errNotArray
	}
	close := len(arr) - 1
	for close > 0 {
		switch arr[close] {
		case ' ', '\t', '\r', '\n':
			close--
			continue
		}
		break
	}
	if close <= 0 || arr[close] != ']' {
		return body, false, errNotArray
	}
	off := s + close
	ins := rawVal
	if len(spans) > 0 {
		ins = append([]byte(","), rawVal...)
	}
	out := make([]byte, 0, len(body)+len(ins))
	out = append(out, body[:off]...)
	out = append(out, ins...)
	out = append(out, body[off:]...)
	return out, true, nil
}

// DropArrayItemBlocks **removes** elements from the sub-array field field (content) of every
// object element of the top-level array arrayKey that match, keeping every other byte as is.
// drop receives one block's raw bytes and decides. It returns how many blocks were removed.
//
// Why removal exists as a primitive (2026-11-19 field event): every primitive before it could
// only add or replace, so the answer to "our request contains a block the upstream refuses"
// was always "write something else in it" - and when the block's content is empty, anything
// written there is **fabrication**. The live case was Claude Code sending
// {"type":"thinking","thinking":" ","signature":""} in seven assistant turns of a long
// session; smt-claude (official Anthropic behind it) answered
//
//	400 … thinking: each thinking block must contain non-whitespace thinking
//
// The block says nothing, so removing it changes no meaning - writing a placeholder into it
// would have made the model read reasoning it never produced.
//
// Two shape rules, both on the safe side:
//   - the element or its field is not a shape we understand (not an object, field not an
//     array): skipped, not an error - the caller treats "unrecognized" as "leave it alone";
//   - **an array whose elements would all be dropped is left untouched** (returns 0 for it):
//     content:[] is a shape most upstreams reject outright, so a repair that empties the
//     array and one that breaks the request are the same thing.
func DropArrayItemBlocks(body []byte, arrayKey, field string,
	match func(item []byte) bool, drop func(block []byte) bool) ([]byte, int, error) {

	s, e, err := findTopLevelValue(body, arrayKey)
	if err != nil {
		return body, 0, err
	}
	arr := body[s:e]
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return body, 0, errNotArray
	}

	type edit struct {
		start, end int
		repl       []byte
	}
	var edits []edit
	dropped := 0

	for _, sp := range spans {
		item := arr[sp[0]:sp[1]]
		if len(item) == 0 || item[0] != '{' {
			continue // not an object: not a shape we handle
		}
		if match != nil && !match(item) {
			continue
		}
		cs, ce, err := findTopLevelValue(item, field)
		if err != nil {
			continue // no such field
		}
		child := item[cs:ce]
		if len(child) == 0 || child[0] != '[' {
			continue // the field is a plain string: nothing to drop
		}
		childSpans, ok := arrayItemSpans(child)
		if !ok {
			continue
		}
		keep := make([]bool, len(childSpans))
		kept, gone := 0, 0
		for i, csp := range childSpans {
			if drop != nil && drop(child[csp[0]:csp[1]]) {
				gone++
				continue
			}
			keep[i] = true
			kept++
		}
		if gone == 0 || kept == 0 {
			continue // nothing to drop, or dropping would empty the array
		}
		edits = append(edits, edit{
			start: s + sp[0] + cs,
			end:   s + sp[0] + ce,
			repl:  rebuildArrayWithout(child, childSpans, keep),
		})
		dropped += gone
	}

	if len(edits) == 0 {
		return body, 0, nil
	}

	out := make([]byte, 0, len(body))
	prev := 0
	for _, ed := range edits { // arrayItemSpans walks left to right, so edits are ordered
		out = append(out, body[prev:ed.start]...)
		out = append(out, ed.repl...)
		prev = ed.end
	}
	out = append(out, body[prev:]...)
	return out, dropped, nil
}

// rebuildArrayWithout rebuilds one array from the kept elements, reusing the original
// separators: `[` + (for each kept element) its own indentation + bytes, `,` between kept
// ones, + the whitespace that followed the last kept element + `]`.
//
// Reusing the original whitespace instead of emitting compact JSON keeps the diff between
// what the client sent and what went upstream to exactly the dropped block.
func rebuildArrayWithout(arr []byte, spans [][2]int, keep []bool) []byte {
	out := make([]byte, 0, len(arr))
	out = append(out, '[')
	first := true
	lastEnd := 1
	for i, sp := range spans {
		if !keep[i] {
			continue
		}
		if !first {
			out = append(out, ',')
		}
		lead := sp[0]
		for lead > 0 && isSpace(arr[lead-1]) {
			lead--
		}
		out = append(out, arr[lead:sp[0]]...)
		out = append(out, arr[sp[0]:sp[1]]...)
		first = false
		lastEnd = sp[1]
	}
	trail := lastEnd
	for trail < len(arr) && isSpace(arr[trail]) {
		trail++
	}
	out = append(out, arr[lastEnd:trail]...)
	return append(out, ']')
}

func isSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// ArrayItems cuts a JSON array's raw bytes into the raw bytes of its elements (for read-only
// checks, e.g. "does this content[] hold a thinking block"). The slices point into the
// original bytes, no copying.
func ArrayItems(arr []byte) ([][]byte, bool) {
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return nil, false
	}
	out := make([][]byte, 0, len(spans))
	for _, sp := range spans {
		out = append(out, arr[sp[0]:sp[1]])
	}
	return out, true
}

// arrayItemSpans returns each element's [start,end) relative to arr.
// When the scan cannot proceed (not an array, or a malformed value) it returns false - the
// caller always treats that as "shape unrecognized, leave it alone" and never guesses.
func arrayItemSpans(arr []byte) ([][2]int, bool) {
	if len(arr) == 0 || arr[0] != '[' {
		return nil, false
	}
	var out [][2]int
	i := 1
	for {
		i = skipWS(arr, i)
		if i >= len(arr) {
			return nil, false
		}
		if arr[i] == ']' {
			return out, true
		}
		if arr[i] == ',' {
			i++
			continue
		}
		ve, ok := scanValue(arr, i)
		if !ok {
			return nil, false
		}
		out = append(out, [2]int{i, ve})
		i = ve
	}
}

var (
	errNotObject = i18n.E("request body top level is not a JSON object", nil)
	errNoKey     = i18n.E("no such key at the top level", nil)
	errNotString = i18n.E("value of the key is not a string", nil)
	errNotArray  = i18n.E("value of the key is not an array", nil)
	errDupKey    = i18n.E("the top level already has this key", nil)
)

// findTopLevelStringValue is findTopLevelValue but requires the value to be a string.
func findTopLevelStringValue(body []byte, key string) (int, int, error) {
	s, e, err := findTopLevelValue(body, key)
	if err != nil {
		return 0, 0, err
	}
	if body[s] != '"' {
		return 0, 0, errNotString
	}
	return s, e, nil
}

// findTopLevelValue returns the byte span [start,end) of a top-level key's value in body.
// It only looks at depth-1 keys, so a nested "model" inside tools is not hit by mistake.
func findTopLevelValue(body []byte, key string) (int, int, error) {
	i := skipWS(body, 0)
	if i >= len(body) || body[i] != '{' {
		return 0, 0, errNotObject
	}
	i++ // enter the object, depth = 1

	for {
		i = skipWS(body, i)
		if i >= len(body) {
			return 0, 0, errNoKey
		}
		if body[i] == '}' {
			return 0, 0, errNoKey
		}
		if body[i] == ',' {
			i++
			continue
		}
		if body[i] != '"' {
			return 0, 0, errNotObject
		}

		// Read the key
		kStart := i
		kEnd, ok := scanString(body, i)
		if !ok {
			return 0, 0, errNotObject
		}
		var k string
		if err := json.Unmarshal(body[kStart:kEnd], &k); err != nil {
			return 0, 0, errNotObject
		}
		i = skipWS(body, kEnd)
		if i >= len(body) || body[i] != ':' {
			return 0, 0, errNotObject
		}
		i = skipWS(body, i+1)
		if i >= len(body) {
			return 0, 0, errNotObject
		}

		vStart := i
		vEnd, ok := scanValue(body, i)
		if !ok {
			return 0, 0, errNotObject
		}
		if k == key {
			return vStart, vEnd, nil
		}
		i = vEnd
	}
}

func skipWS(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// scanString starts at b[i]=='"' and returns the position after the closing quote.
func scanString(b []byte, i int) (int, bool) {
	if i >= len(b) || b[i] != '"' {
		return 0, false
	}
	i++
	for i < len(b) {
		switch b[i] {
		case '\\':
			i += 2 // skip the escape sequence (chars after \uXXXX are never " or \, safe)
		case '"':
			return i + 1, true
		default:
			i++
		}
	}
	return 0, false
}

// scanValue returns the position after the JSON value starting at b[i].
// It only needs to delimit correctly, not validate - validation is the upstream's job; our
// only concern is not breaking anything.
func scanValue(b []byte, i int) (int, bool) {
	if i >= len(b) {
		return 0, false
	}
	switch b[i] {
	case '"':
		return scanString(b, i)
	case '{', '[':
		open, close := b[i], byte('}')
		if open == '[' {
			close = ']'
		}
		depth := 0
		for i < len(b) {
			switch b[i] {
			case '"':
				n, ok := scanString(b, i)
				if !ok {
					return 0, false
				}
				i = n
				continue
			case open:
				depth++
			case close:
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
			i++
		}
		return 0, false
	default:
		// Number / true / false / null: scan up to the delimiter
		for i < len(b) {
			switch b[i] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return i, true
			}
			i++
		}
		return i, true
	}
}

// TopLevelString reads out a top-level string field.
func TopLevelString(body []byte, key string) (string, bool) {
	s, e, err := findTopLevelStringValue(body, key)
	if err != nil {
		return "", false
	}
	var v string
	if json.Unmarshal(body[s:e], &v) != nil {
		return "", false
	}
	return v, true
}

// TopLevelBool reads out a top-level boolean field (used for stream).
func TopLevelBool(body []byte, key string) bool {
	i := skipWS(body, 0)
	if i >= len(body) || body[i] != '{' {
		return false
	}
	i++
	for {
		i = skipWS(body, i)
		if i >= len(body) || body[i] == '}' {
			return false
		}
		if body[i] == ',' {
			i++
			continue
		}
		if body[i] != '"' {
			return false
		}
		kStart := i
		kEnd, ok := scanString(body, i)
		if !ok {
			return false
		}
		var k string
		if json.Unmarshal(body[kStart:kEnd], &k) != nil {
			return false
		}
		i = skipWS(body, kEnd)
		if i >= len(body) || body[i] != ':' {
			return false
		}
		i = skipWS(body, i+1)
		vStart := i
		vEnd, ok := scanValue(body, i)
		if !ok {
			return false
		}
		if k == key {
			return string(body[vStart:vEnd]) == "true"
		}
		i = vEnd
	}
}
