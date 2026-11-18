package config

import (
	"testing"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// 这一对锁的是**当前配置那一张卡**在界面上长什么样，因为它的第一版是错的，
// 而且错得很不容易发现。
//
// 事情的经过（2026-09-21 用户指出）：已经生效的那一份档位不挂那对 apply 按钮
// ——这个判断是对的（「把当前这份设为默认」显示在它自己身上是纯噪音）。但当时
// 的实现是 `return nil`，于是**整张卡上什么都没有**：十六张档位卡里十五张有两个
// 按钮、一张没有，两张卡长得几乎一样。用户看不出哪一份在生效，而那恰恰是他打开
// 这一节最想知道的事。他的原话：
//
//	你应该注入的是一个 info only 的绿色：当前配置 / Active profile 的效果啊！
//
// 所以现在那一张卡上有一句绿色的 Note，而**按钮照旧不挂**。这两件事要一起断言：
// 只断言「有 Note」会漏掉「按钮又长回来了」，只断言「没按钮」会漏掉「什么都没说」
// ——那正是原来那个 bug 的形状。

func TestTheActiveProfileSaysSoAndTheOthersOfferToApply(t *testing.T) {
	testkit.Sandbox(t)
	seedFile(t, "demo.kv", "desc=demo base\nnormal=p/demo-model\n")
	seedFile(t, "alt.kv", "desc=alt base\nnormal=p/alt-model\n")
	if err := store.SetDefaultProfile("demo", false); err != nil {
		t.Fatalf("设默认档位失败: %v", err)
	}
	all := []string{"demo", "alt"}

	active := profileConcept("demo", all)
	other := profileConcept("alt", all)

	// 1. 生效的那一张：一句绿色的陈述，且**没有** apply 按钮。
	if active.Note == nil {
		t.Error("当前配置那一张卡上没有 Note——它在界面上与别的卡长得一样，" +
			"用户看不出哪一份在生效（这正是这一格存在的理由，见 profileNote）")
	} else {
		if active.Note.Text == "" {
			t.Error("Note 的 Text 是空的：卡片上会画出一个空的白框")
		}
		if active.Note.Tone != view.ToneOK {
			t.Errorf("Note 的语气是 %q，应当是 %q——它是「一切正常，这就是现在生效的"+
				"那一份」，不是警告也不是错误", active.Note.Tone, view.ToneOK)
		}
	}
	if len(active.Actions) != 0 {
		t.Errorf("当前配置那一张卡上挂了 %d 个按钮，应当是 0 个：把自己的那一张"+
			"「设为默认」是纯噪音", len(active.Actions))
	}

	// 2. 别的那一张：反过来——两个按钮，且**没有** Note。
	if len(other.Actions) != 2 {
		t.Errorf("别的档位卡上有 %d 个按钮，应当是 2 个（use / use globally）", len(other.Actions))
	}
	if other.Note != nil {
		t.Errorf("不是当前配置的那一张卡上也写了 Note（%q）：这句话是一句判词，"+
			"写在不生效的卡上就是在骗人", other.Note.Text)
	}
}

// TestExactlyOneCardCarriesTheActiveNote 是上一条的**集合形状**那一半。
//
// 上一条逐张问「这一张对不对」，这一条问的是「一共几张说了这句话」。两者不是
// 同一件事：判据若被改成恒为真（或者拿别的字段去比），「每一张自己看都对」仍然
// 成立，红的只会是数量。而这句话的语义是**「就是这一份」**——一个装配里只该有
// 一张卡说得出它，多一张就是判据写错了。
//
// 三张卡而不是两张，是为了让「恰好一张」与「全都画上」在数量上分得开。
func TestExactlyOneCardCarriesTheActiveNote(t *testing.T) {
	testkit.Sandbox(t)
	names := []string{"demo", "alt", "third"}
	for _, n := range names {
		seedFile(t, n+".kv", "desc="+n+"\nnormal=p/"+n+"-model\n")
	}
	if err := store.SetDefaultProfile("alt", false); err != nil {
		t.Fatalf("设默认档位失败: %v", err)
	}

	var marked []string
	for _, n := range names {
		if c := profileConcept(n, names); c.Note != nil {
			marked = append(marked, n)
		}
	}
	if len(marked) != 1 || marked[0] != "alt" {
		t.Errorf("带「当前配置」这句话的卡是 %v，应当恰好是 [alt]——这句话是一句判词，"+
			"一个装配里只该有一张卡说得出它（多一张是判据写错了，少一张是没人说）", marked)
	}
}
