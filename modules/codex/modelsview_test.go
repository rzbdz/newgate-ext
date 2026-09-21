package codex

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/testing/testkit"
)

// 这一族是那张映射表的**写入侧**：界面上按一次「应用」之后，盘上到底发生了什么。
//
// 它是这张卡唯一会毁掉东西的地方（用户的表、以及表里那些他手写的行），而它的
// 失败方式全是**静默**的：写空了、把 mode 抹了、把别人刚改的那份盖了——三种在
// 界面上都只表现为「保存成功」。
//
// 为什么压在这一层而不是浏览器那一层（2026-09-21 用户定的规矩）：**UI 的测试主要
// 覆盖 BFF 与它下面这层，e2e 尽量少动**——那条浏览器检查跑一次要几分钟，而这几条
// 是纯函数与文件读写，毫秒级。真正只有浏览器能验的（点了按钮界面变没变）留在
// ui_check 里，别的都下来。

// editOf 拼一份界面回传的载荷：`{items:[{id, values:{slug,tier}}]}`。
func editOf(rows ...[2]string) json.RawMessage {
	type item struct {
		ID     string            `json:"id"`
		Values map[string]string `json:"values"`
	}
	items := make([]item, 0, len(rows))
	for _, r := range rows {
		items = append(items, item{ID: r[0], Values: map[string]string{"slug": r[0], "tier": r[1]}})
	}
	b, _ := json.Marshal(map[string]any{"items": items})
	return b
}

func readFile(t *testing.T) Models {
	t.Helper()
	b, err := os.ReadFile(ModelsFile())
	if err != nil {
		return Models{}
	}
	var m Models
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("盘上那份读不出来: %v", err)
	}
	return m
}

// TestApplyWritesTheWholeTableAndKeepsTheMode：整表写回，而 mode 那一格不许被抹掉。
//
// mode 是**另一件事**（它说的是接管怎么写 config.toml），保存这张表与它无关——
// 抹掉的症状是「改完映射，模式悄悄变回 takeover」，而界面上那句 Note 也跟着变了，
// 用户只会以为是自己点错了。
func TestApplyWritesTheWholeTableAndKeepsTheMode(t *testing.T) {
	testkit.Sandbox(t)
	if err := SetMode(ModeRename); err != nil {
		t.Fatal(err)
	}
	if _, err := applyModels(editOf([2]string{"gpt-9-a", "heavy"}, [2]string{"gpt-9-b", "light"}), store.Revision(ModelsFile())); err != nil {
		t.Fatal(err)
	}
	m := readFile(t)
	if m.Mode != ModeRename {
		t.Errorf("保存那张表之后模式变成了 %q——它不属于这张表，不该被动", m.Mode)
	}
	if len(m.Models) != 2 || m.Models[0].Slug != "gpt-9-a" || m.Models[1].Tier != "light" {
		t.Errorf("盘上那张表不是界面上交回来的那份: %+v", m.Models)
	}
}

// TestApplyRejectsRowsThatWouldSilentlyDoNothing 锁的是三种「存下去但不算数」。
//
// 这三种都不会让界面报错，而症状全都是「加了、没了、也没说为什么」：
//
//	名字空   → 谁也匹配不上；
//	档位空   → effectiveModels 会把它丢掉，于是那一行保存之后自己消失；
//	两行同名 → 后一行永远轮不到，而界面上两行都画着。
func TestApplyRejectsRowsThatWouldSilentlyDoNothing(t *testing.T) {
	cases := []struct {
		name string
		rows [][2]string
	}{
		{"名字空", [][2]string{{"", "heavy"}}},
		{"档位空", [][2]string{{"gpt-9-a", ""}}},
		{"两行同名", [][2]string{{"gpt-9-a", "heavy"}, {"gpt-9-a", "light"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			testkit.Sandbox(t)
			if _, err := applyModels(editOf(c.rows...), store.Revision(ModelsFile())); err == nil {
				t.Fatal("这一行该被拒（它会静静地什么都不做），却保存成功了")
			}
			if _, err := os.Stat(ModelsFile()); err == nil {
				t.Error("被拒的那次不该动盘上那份")
			}
		})
	}
}

// TestApplyRefusesToClobberAFileChangedUnderneath：别人（手改、另一个标签页）动过
// 那份文件时，回一句冲突**且什么都不写**。
//
// 这条与别的模块的 CAS 是同一条规矩，但这里多一层必要：那张表是**用户手写得到**
// 的（它住在他的配置目录里），所以「命令行刚改过它」不是假想。
func TestApplyRefusesToClobberAFileChangedUnderneath(t *testing.T) {
	testkit.Sandbox(t)
	if _, err := applyModels(editOf([2]string{"gpt-9-a", "heavy"}), ""); err != nil {
		t.Fatal(err)
	}
	// 拿一份**过期**的基线（文件刚建之前那份，也就是空串）再保存一次。
	stale := store.RevisionOf([]byte("{}"))
	if _, err := applyModels(editOf([2]string{"gpt-9-b", "light"}), stale); err == nil {
		t.Fatal("基线对不上时该拒绝，而不是把别人那份盖掉")
	}
	m := readFile(t)
	if len(m.Models) != 1 || m.Models[0].Slug != "gpt-9-a" {
		t.Errorf("被拒的那次把盘上那份改了: %+v", m.Models)
	}
}

// TestApplyKeepsUnknownKeys：只换 models 那一段，文件里别的键原样留着。
//
// 今天只有 mode，而这条测试是为**将来**写的：往那份文件里加一个键（比如某个版本
// 的迁移标记）时，谁都不该因为「保存了一下映射表」而把它弄丢。
func TestApplyKeepsUnknownKeys(t *testing.T) {
	testkit.Sandbox(t)
	seed := `{"mode":"rename","something_else":{"a":1}}`
	if err := os.WriteFile(ModelsFile(), []byte(seed), 0o660); err != nil {
		t.Fatal(err)
	}
	if _, err := applyModels(editOf([2]string{"gpt-9-a", "heavy"}), store.Revision(ModelsFile())); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(ModelsFile())
	if !strings.Contains(string(b), "something_else") {
		t.Errorf("保存那张表把一个不认识的键弄丢了:\n%s", b)
	}
}
