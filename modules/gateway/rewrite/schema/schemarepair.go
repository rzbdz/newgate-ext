package schemarepair

import (
	"encoding/json"
	"fmt"
	"sort"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Repair 修补 tools 数组里会被严格校验器拒绝的 schema 问题。
//
// 设计原则：
//  1. 只做**语义无操作**的修补。按 JSON Schema 规范 `"required": []` 与
//     不写 required 完全等价，所以补上它不改变任何行为，只是让把「缺失」
//     当 null 处理的校验器（如 DeepSeek 网关）能通过。
//  2. 没有任何要补的东西时返回 changed=false，调用方就完全不碰原始字节——
//     该走忠实透传的时候一个字节都不动。
//  3. 每一处修补都记录下来，绝不静默改用户的请求。
//
// 现场依据：opencode 1.18.19 发的 35 个 tool 里有 4 个（background_cancel、
// list_mcp_resources、list_mcp_resource_templates、session_list）的
// parameters 是 {"type":"object","properties":{…}} 但没有 required 键，
// DeepSeek 上游报 400 `Invalid schema for function 'background_cancel':
// null is not of type "array"`。Claude 上游对此宽容。
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
		return nil, nil, nil // 无需修补：调用方保持原字节
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

// repairObjectSchema 递归给所有 type:"object" 的子 schema 补上缺失的 required。
// 返回补了几处。
func repairObjectSchema(schema map[string]interface{}) int {
	n := 0
	if t, _ := schema["type"].(string); t == "object" {
		if _, has := schema["required"]; !has {
			schema["required"] = []interface{}{}
			n++
		} else if schema["required"] == nil {
			// 显式 null 也一并修成空数组
			schema["required"] = []interface{}{}
			n++
		}
	}

	// properties 下的每个子 schema
	if props, ok := schema["properties"].(map[string]interface{}); ok {
		for _, v := range props {
			if sub, ok := v.(map[string]interface{}); ok {
				n += repairObjectSchema(sub)
			}
		}
	}
	// 数组的 items
	if items, ok := schema["items"].(map[string]interface{}); ok {
		n += repairObjectSchema(items)
	}
	// 组合关键字
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
