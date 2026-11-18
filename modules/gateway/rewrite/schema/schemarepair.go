package schemarepair

import (
	"encoding/json"
	"fmt"
	"sort"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Repair patches schema problems in the tools array that strict validators reject.
//
// Design principles:
//  1. Only make **semantically no-op** repairs. Per the JSON Schema spec `"required": []` is
//     fully equivalent to omitting required, so adding it changes no behavior; it only lets
//     validators that treat "missing" as null (such as the DeepSeek gateway) pass.
//  2. When there is nothing to repair it returns changed=false and the caller does not touch
//     the original bytes at all - when faithful pass-through is right, not one byte moves.
//  3. Every repair is recorded; the user's request is never silently changed.
//
// Field evidence: of the 35 tools opencode 1.18.19 sends, 4 (background_cancel,
// list_mcp_resources, list_mcp_resource_templates, session_list) have
// parameters of {"type":"object","properties":{…}} with no required key, and the
// DeepSeek upstream answers 400 `Invalid schema for function 'background_cancel':
// null is not of type "array"`. The Claude upstream is lenient about it.
func Repair(toolsRaw []byte) (out []byte, changes []string, err error) {
	var tools []interface{}
	if err := json.Unmarshal(toolsRaw, &tools); err != nil {
		return nil, nil, i18n.Ef(err, "tools is not an array: {err}", nil)
	}

	var fixed []string
	for _, t := range tools {
		tm, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		fn, ok := tm["function"].(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		params, ok := fn["parameters"].(map[string]interface{})
		if !ok {
			continue
		}
		if n := repairObjectSchema(params); n > 0 {
			fixed = append(fixed, fmt.Sprintf("%s(+%d)", nameOr(name), n))
		}
	}

	if len(fixed) == 0 {
		return nil, nil, nil // nothing to repair: the caller keeps the original bytes
	}
	sort.Strings(fixed)

	b, err := json.Marshal(tools)
	if err != nil {
		return nil, nil, err
	}
	return b, fixed, nil
}

func nameOr(s string) string {
	if s == "" {
		return i18n.T("(anonymous)", nil)
	}
	return s
}

// repairObjectSchema recursively adds the missing required to every type:"object"
// sub-schema. It returns how many were added.
func repairObjectSchema(schema map[string]interface{}) int {
	n := 0
	if t, _ := schema["type"].(string); t == "object" {
		if _, has := schema["required"]; !has {
			schema["required"] = []interface{}{}
			n++
		} else if schema["required"] == nil {
			// An explicit null is fixed into an empty array too
			schema["required"] = []interface{}{}
			n++
		}
	}

	// Every sub-schema under properties
	if props, ok := schema["properties"].(map[string]interface{}); ok {
		for _, v := range props {
			if sub, ok := v.(map[string]interface{}); ok {
				n += repairObjectSchema(sub)
			}
		}
	}
	// An array's items
	if items, ok := schema["items"].(map[string]interface{}); ok {
		n += repairObjectSchema(items)
	}
	// Combinator keywords
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := schema[key].([]interface{}); ok {
			for _, v := range arr {
				if sub, ok := v.(map[string]interface{}); ok {
					n += repairObjectSchema(sub)
				}
			}
		}
	}
	// $defs / definitions
	for _, key := range []string{"$defs", "definitions"} {
		if defs, ok := schema[key].(map[string]interface{}); ok {
			for _, v := range defs {
				if sub, ok := v.(map[string]interface{}); ok {
					n += repairObjectSchema(sub)
				}
			}
		}
	}
	return n
}
