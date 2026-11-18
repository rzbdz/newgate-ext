package confighook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/store"
)

// 「我就是要它」与「我什么都没选、而缺省正好是它」是**两个状态**，在配置上就不是
// 一回事：前者在 state.json 里有一条记录，以后改全局缺省它**不会**跟着走。
//
// 这一条实测踩出来的（2026-09-29）：网页首屏上给某一家点「就用 ds」，而自动恰好
// 也是 ds——那一下明明写进了一条记录，界面却把这一家报成「跟随自动」，高亮于是从
// 这张卡跳到「自动」那张上。用户的原话是「我点 ds，怎么跳到选中自动了」。
func TestStoredChoiceCountsEvenWhenItMatchesTheDefault(t *testing.T) {
	sandbox(t)
	// 两份档位文件：SetDefaultProfile 会校验这一份存在（指到一个不存在的名字是
	// 用户笔误，该报出来，所以这里先把它们建出来）。
	dir := filepath.Join(os.Getenv("NEWGATE_HOME"), "mappings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ds", "ark"} {
		if err := os.WriteFile(filepath.Join(dir, n+".kv"), []byte("normal = smt/"+n+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetDefaultProfile("ds", false); err != nil {
		t.Fatal(err)
	}
	p := AgentProfile{AgentID: "claude"}

	if p.IsOverride() {
		t.Error("什么都没设过时不该报「单独设过」——它就该是跟着缺省的那一家")
	}
	if err := p.Write("ds"); err != nil {
		t.Fatal(err)
	}
	if !p.IsOverride() {
		t.Error("明确选了 ds（而缺省也是 ds）之后**必须**报「单独设过」：" +
			"否则界面会把它说成「跟随缺省」，而用户想固定住的东西看起来像没固定")
	}
	if got := p.Stored(); got != "ds" {
		t.Errorf("存下来的那个该是 ds，实际 %q", got)
	}
	// 反方向也要对：把缺省改掉，固定住的那一家**不该**跟着走。
	if err := store.SetDefaultProfile("ark", false); err != nil {
		t.Fatal(err)
	}
	if got := p.Active(); got != "ds" {
		t.Errorf("固定过之后改缺省，这一家该还走 ds，实际 %q", got)
	}
	// 空串 = 松手、回到跟随。那时才不该再报「单独设过」。
	if err := p.Write(""); err != nil {
		t.Fatal(err)
	}
	if p.IsOverride() {
		t.Error("松开之后不该再报「单独设过」")
	}
	if got := p.Active(); got != "ark" {
		t.Errorf("松开之后该跟着缺省走 ark，实际 %q", got)
	}
}
