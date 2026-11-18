package quirk

import (
	"testing"
)

// 认不出的一律不猜：宁可让用户看到原始报错，也不能瞎给请求加字段。
func TestLearnNeverGuesses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"unrelated 400", 400, `{"error":{"message":"invalid request: max_tokens too large"}}`},
		{"5xx means the upstream itself is down, unrelated to the request shape", 500, `{"error":{"message":"always thinks"}}`},
		{"429", 429, `{"error":{"message":"rate limit"}}`},
		{"empty body", 400, ``},
		{"just one ordinary sentence with no signature at all", 400, `{"error":{"message":"bad request"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := NewTable()
			if _, err := table.RegisterSignature(Signature{
				Flag: NoThinkingDisable, Any: []string{"does not support disabling thinking"}, Label: "this model always thinks",
			}); err != nil {
				t.Fatal(err)
			}
			if got := table.Learn("prov", "model-y", tt.status, []byte(tt.body)); got != nil {
				t.Fatalf("it learned something it should not have: %v", got)
			}
			if table.Has("prov", "model-y", NoThinkingDisable) {
				t.Fatal("it flagged something it should not have")
			}
		})
	}
}

// 记账是按 (provider, model) 分的：给 A 记的东西不能漏给 B，也不能串到 B。
//
// 用 Mark 而不是 Learn：**签名表现在是空的**（判据由拥有补丁的模块注册，见
// modules/thinking/signatures.go），所以这里考的是这张表的记账粒度，与签名无关。
func TestFlagsAreScopedToProviderAndModel(t *testing.T) {
	table := NewTable()
	table.Mark("kimi", "kimi-k2.7-code", NoThinkingDisable)
	if !table.Has("kimi", "kimi-k2.7-code", NoThinkingDisable) {
		t.Fatal("it did not record it itself")
	}
	if table.Has("kimi", "kimi-k2.7", NoThinkingDisable) || table.Has("glm", "kimi-k2.7-code", NoThinkingDisable) {
		t.Fatal("it leaked to another provider/model")
	}
}

// Snapshot 要能看见已经学到的 flag（CLI/诊断读它）。
//
// 它用 Mark 直接记，不再走 Learn：**签名表现在是空的**（判据由拥有补丁的模块注册，
// 见 modules/thinking/signatures.go），所以 Learn 在一张新表上什么也学不到。这条测
// 的是「记下来的东西在快照里看得见」，与签名内容无关。
func TestSnapshotShowsLearnedFlags(t *testing.T) {
	table := NewTable()
	table.Mark("kimi", "kimi-k2.7-code", NoThinkingDisable)
	found := false
	for _, e := range table.Snapshot() {
		if e.Provider == "kimi" && e.Model == "kimi-k2.7-code" &&
			e.Flags&NoThinkingDisable != 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the flag just recorded is not visible in the snapshot: %v", table.Snapshot())
	}
}
