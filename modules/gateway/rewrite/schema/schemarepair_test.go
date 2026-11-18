package schemarepair

import (
	"encoding/json"
	"strings"
	"testing"
)

// 现场原样：opencode 1.18.19 的 background_cancel，parameters 里没有 required。
const realTool = `[
 {"type":"function","function":{
   "name":"background_cancel",
   "description":"Cancel running background task(s).",
   "parameters":{"$schema":"https://json-schema.org/draft/2020-12/schema",
     "type":"object",
     "properties":{
       "taskId":{"description":"Task ID","type":"string"},
       "all":{"description":"Cancel all","type":"boolean"}}}}},
 {"type":"function","function":{
   "name":"bash",
   "parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}}
]`

func TestRepairsMissingRequired(t *testing.T) {
	out, changes, err := Repair([]byte(realTool))
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || !strings.HasPrefix(changes[0], "background_cancel") {
		t.Fatalf("应只修 background_cancel，得到 %v", changes)
	}

	var tools []map[string]interface{}
	if err := json.Unmarshal(out, &tools); err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools {
		fn := tl["function"].(map[string]interface{})
		p := fn["parameters"].(map[string]interface{})
		req, has := p["required"]
		if !has {
			t.Errorf("%v 修完还是没有 required", fn["name"])
			continue
		}
		if _, ok := req.([]interface{}); !ok {
			t.Errorf("%v 的 required 不是数组: %T", fn["name"], req)
		}
	}
	// 已有的 required 不能被动
	bash := tools[1]["function"].(map[string]interface{})["parameters"].(map[string]interface{})
	if got := bash["required"].([]interface{}); len(got) != 1 || got[0] != "cmd" {
		t.Errorf("bash 原有的 required 被改了: %v", got)
	}
	// 其它字段保留
	bc := tools[0]["function"].(map[string]interface{})["parameters"].(map[string]interface{})
	if bc["$schema"] == nil {
		t.Error("$schema 丢了")
	}
	if len(bc["properties"].(map[string]interface{})) != 2 {
		t.Error("properties 丢了")
	}
}

// 没东西可补时必须返回 changed=false，让调用方保持原始字节不动。
func TestNoChangeMeansNoRewrite(t *testing.T) {
	clean := `[{"type":"function","function":{"name":"x","parameters":{"type":"object","properties":{},"required":[]}}}]`
	out, changes, err := Repair([]byte(clean))
	if err != nil {
		t.Fatal(err)
	}
	if out != nil || len(changes) != 0 {
		t.Errorf("干净的 tools 不该被重写，得到 changes=%v", changes)
	}
}

func TestRepairsNested(t *testing.T) {
	nested := `[{"type":"function","function":{"name":"n","parameters":{
	  "type":"object","required":[],
	  "properties":{
	    "obj":{"type":"object","properties":{"a":{"type":"string"}}},
	    "arr":{"type":"array","items":{"type":"object","properties":{"b":{"type":"string"}}}}
	  }}}}]`
	_, changes, err := Repair([]byte(nested))
	if err != nil {
		t.Fatal(err)
	}
	// obj 和 items 里的 object 各补一处
	if len(changes) != 1 || !strings.Contains(changes[0], "+2") {
		t.Errorf("嵌套 object 应补 2 处，得到 %v", changes)
	}
}

func TestExplicitNullRequired(t *testing.T) {
	withNull := `[{"type":"function","function":{"name":"z","parameters":{"type":"object","properties":{},"required":null}}}]`
	out, changes, err := Repair([]byte(withNull))
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) == 0 {
		t.Fatal("显式 null 的 required 应被修成 []")
	}
	if strings.Contains(string(out), "null") {
		t.Errorf("修完还有 null: %s", out)
	}
}

func TestMalformedToolsIsNotFatal(t *testing.T) {
	if _, _, err := Repair([]byte(`{"not":"array"}`)); err == nil {
		t.Error("非数组应报错，让调用方按原样透传")
	}
}
