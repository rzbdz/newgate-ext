package codex

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentapi "github.com/rzbdz/newgate/modules/confighook"
	"github.com/rzbdz/newgate/testing/testkit"
)

// 接管 codex = 改写一份**人手写的** TOML。这一族测的就是那句话里最容易坏的三处：
// 别人写的东西一个都不许动、我们自己写的东西要能原样认出来、以及逃生舱真的能还原。

// sample 是一份尽量像真实用户配置的 TOML：注释、散落的段、以及它自己记的账。
//
// 逐项都是实测里见过的（`~/.codex/config.toml`，2026-09-21）：`[projects.*]` 是
// codex 每信任一个目录就自己长一段，`[notice.model_migrations]` 是它记的改名账。
const sample = `# 我的 codex 配置
model = "gpt-5.6-terra"
model_reasoning_effort = "medium"
personality = "pragmatic"

[projects."/root"]
trust_level = "trusted"

[notice.model_migrations]
"gpt-5.3-codex" = "gpt-5.4"
`

// TestRewriteKeepsEverythingElse：只动该动的三行，别的**逐字节**不动。
//
// 这是这套实现存在的全部理由。做一次 TOML 解析→序列化的往返，注释、段的顺序、
// 空行位置全会变——而那种损失不会让任何测试变红，只会让用户发现「我写的注释没了」。
func TestRewriteKeepsEverythingElse(t *testing.T) {
	out, reps := rewrite(sample, 8899, want{Model: "normal", ContextWindow: 200000})

	for _, keep := range []string{
		"# 我的 codex 配置",
		`personality = "pragmatic"`,
		`model_reasoning_effort = "medium"`,
		"[projects.\"/root\"]",
		`trust_level = "trusted"`,
		"[notice.model_migrations]",
		`"gpt-5.3-codex" = "gpt-5.4"`,
	} {
		if !strings.Contains(out, keep) {
			t.Errorf("这一行不该被动：%q\n--- 实际 ---\n%s", keep, out)
		}
	}

	if !strings.Contains(out, `model = "normal"`) {
		t.Errorf("model 该被改成档位名\n%s", out)
	}
	if !strings.Contains(out, `model_provider = "newgate"`) {
		t.Errorf("model_provider 该指向我们\n%s", out)
	}
	if strings.Contains(out, "gpt-5.6-terra") {
		t.Errorf("原来的具体模型名该被换掉\n%s", out)
	}
	if len(reps) == 0 {
		t.Error("改了东西就要记账——接管不许静默")
	}
}

// TestRewriteDoesNotTouchKeysInsideTables：表里的同名键不许被当成顶层键改掉。
//
// TOML 里各表的键互不相干，而这份文件里确实有别的段。第一版按「行里有 `model`」
// 找，会把 `[notice.model_migrations]` 里那些改名账当成模型名去改——症状是用户的
// 改名记录被我们悄悄换成了档位名。
func TestRewriteDoesNotTouchKeysInsideTables(t *testing.T) {
	const src = `[projects."/root"]
model = "user-set-this"
trust_level = "trusted"
`
	out, _ := rewrite(src, 8899, want{Model: "normal"})
	if !strings.Contains(out, `model = "user-set-this"`) {
		t.Errorf("表里的 model 不是我们的，不许动\n%s", out)
	}
	// 顶层缺 model，所以要补在最前面——而**不能**补在表头之后（那会变成那个表的键）。
	head := strings.SplitN(out, "[projects", 2)[0]
	if !strings.Contains(head, `model = "normal"`) {
		t.Errorf("顶层缺的键该补在第一个表头之前\n%s", out)
	}
}

// TestRewriteIsIdempotent：接管两次，结果一样。
//
// 幂等不是洁癖：`newgate on codex` 本来就允许重复跑（改了档位之后要重跑一次，
// 见 docs 的「重新接管」那条）。不幂等的话，每跑一次文件里就多一段
// `[model_providers.newgate]`，而 TOML 的同名表重复是**解析错误**——用户下一次
// 启动 codex 直接起不来。
func TestRewriteIsIdempotent(t *testing.T) {
	once, _ := rewrite(sample, 8899, want{Model: "normal", ContextWindow: 200000})
	twice, _ := rewrite(once, 8899, want{Model: "normal", ContextWindow: 200000})
	if once != twice {
		t.Errorf("第二次接管改了东西\n--- 一次 ---\n%s\n--- 两次 ---\n%s", once, twice)
	}
	if n := strings.Count(twice, providerTable); n != 1 {
		t.Errorf("那段配置该只有一份，实际 %d 份", n)
	}
	if n := strings.Count(twice, "model_context_window"); n != 1 {
		t.Errorf("窗口声明该只有一行，实际 %d 行", n)
	}
	// 幂等的同时它还得真的改了东西——两边都成立才算数。
	if !strings.Contains(twice, `wire_api = "responses"`) {
		t.Errorf("接管完该指向本地代理\n%s", twice)
	}
}

// TestRewriteRefreshesAnOlderTakeover：上一版写的那段（端口变了）要被换掉，不是叠加。
//
// 端口是会变的（配置里能改），而旧的那段如果留着，症状是 codex 一直打一个**没有
// 在听的端口**——用户看到的是「连接被拒绝」，而配置文件里明明写着 newgate。
func TestRewriteRefreshesAnOlderTakeover(t *testing.T) {
	old, _ := rewrite(sample, 8899, want{Model: "normal"})
	moved, _ := rewrite(old, 9999, want{Model: "mid"})

	if strings.Contains(moved, "127.0.0.1:8899") {
		t.Errorf("旧端口该被换掉\n%s", moved)
	}
	if !strings.Contains(moved, "127.0.0.1:9999") {
		t.Errorf("新端口该在\n%s", moved)
	}
	if !strings.Contains(moved, `model = "mid"`) {
		t.Errorf("档位该跟着变\n%s", moved)
	}
}

// TestTheWindowIsOnlyWrittenWhenWeKnowIt：不知道窗口就**不写**那一行。
//
// 写一个猜来的数比不写更糟：codex 会拿它决定什么时候自动 compact，而「猜小了」
// 的症状是长会话被过早截断——看起来像模型变笨了，没人会怀疑到配置里多出来的
// 那一行。
func TestTheWindowIsOnlyWrittenWhenWeKnowIt(t *testing.T) {
	without, _ := rewrite(sample, 8899, want{Model: "normal"})
	if strings.Contains(without, "model_context_window") {
		t.Errorf("不知道窗口时不该写这一行\n%s", without)
	}
	with, _ := rewrite(sample, 8899, want{Model: "normal", ContextWindow: 200000})
	if !strings.Contains(with, "model_context_window = 200000") {
		t.Errorf("知道窗口时要写上\n%s", with)
	}
}

// TestApplyThenRestoreGivesBackTheOriginalBytes：逃生舱。
//
// 这是整套接管唯一不能出错的地方：用户 `newgate off codex`（或者干脆卸载 newgate）
// 之后，拿回来的必须**就是**他原来那份，一个字节不差。所以断言打在字节相等上，
// 不是「看起来差不多」。
func TestApplyThenRestoreGivesBackTheOriginalBytes(t *testing.T) {
	testkit.Sandbox(t)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	target := filepath.Join(codexHome, "config.toml")
	if err := ioutil.WriteFile(target, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}

	tk := Takeover{}
	reps, err := tk.Apply(8899)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 1 || reps[0].Skipped != "" {
		t.Fatalf("接管该成功，实际 %+v", reps)
	}
	if !tk.IsTakenOver(target) {
		t.Error("接管之后 IsTakenOver 该为真")
	}
	if b, _ := ioutil.ReadFile(target); !strings.Contains(string(b), "127.0.0.1:8899") {
		t.Errorf("接管之后该指向本地代理\n%s", b)
	}

	done, err := tk.Restore()
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 {
		t.Fatalf("该还原一个文件，实际 %v", done)
	}
	got, err := ioutil.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sample {
		t.Errorf("还原出来的不是原来那份\n--- 期望 ---\n%s\n--- 实际 ---\n%s", sample, got)
	}
	if tk.IsTakenOver(target) {
		t.Error("还原之后不该还算被接管")
	}
}

// TestATaintedFileIsNotUsedAsTheOriginal：original/ 被删掉之后，不许把已接管的
// 文件当原件存进去。
//
// 这是 opencode 那边实测踩过的坑，codex 这边一个字都不能少：一旦存错，用户的原
// 配置就**永久**没了（original/ 是唯一那份没被我们碰过的）。判据落在「必须报错」
// 上——安静地存下去才是真正的损失。
func TestATaintedFileIsNotUsedAsTheOriginal(t *testing.T) {
	testkit.Sandbox(t)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	target := filepath.Join(codexHome, "config.toml")

	taken, _ := rewrite(sample, 8899, want{Model: "normal"})
	if err := ioutil.WriteFile(target, []byte(taken), 0o600); err != nil {
		t.Fatal(err)
	}
	// original/ 不存在（第一次接管就被误删的场景）。
	if err := os.RemoveAll(filepath.Dir(agentapi.OriginalPath(target))); err != nil {
		t.Fatal(err)
	}
	tk := Takeover{}
	if _, err := tk.Apply(8899); err == nil {
		t.Fatal("已接管的文件 + 没有原件 = 该报错，而不是把它当原件存下去")
	}
}

// TestTheSecondModelKeyIsWritten：codex 的模型**不止一个**，两个都要写对。
//
// `review_model` 是 `codex review` 用的那一个（codex 0.155 的 ConfigToml 里与
// `model` 并列，2026-09-21 查证）。用户的原话：「为什么他的模型只有一个槽位？整改！」
//
// 判据打在**两个键各写各的**上：一处写错（比如按下标取槽位，把 review 的档位写进
// 主模型）在界面上完全看不出来——两个框都填着合法的档位名，只有 review 走了主模型的
// 档位，而那要等到真跑一次 review 才会发现。
func TestTheSecondModelKeyIsWritten(t *testing.T) {
	out, _ := rewrite(sample, 8899, want{Model: "heavy", ReviewModel: "mid"})
	if !strings.Contains(out, `model = "heavy"`) {
		t.Errorf("主模型该走 heavy\n%s", out)
	}
	if !strings.Contains(out, `review_model = "mid"`) {
		t.Errorf("review 该走 mid\n%s", out)
	}
}

// TestAnEmptyValueIsNotWrittenAtAll：没值的键**一行都不写**，不是写一个空值。
//
// 这条是实测踩出来的：`values()` 只给要写的键，而补键的循环按「全部可能写的键」
// 走了一遍，于是没值的那些被写成了 `review_model = `（一个**空字符串**）。
// TOML 里那不是「没配」——codex 会把它当模型名发到上游。红的是幂等那条测试
// （第二次接管看见空值，又改写一次），而这条直接把判据摆在明面上。
func TestAnEmptyValueIsNotWrittenAtAll(t *testing.T) {
	out, _ := rewrite(sample, 8899, want{Model: "normal"})
	for _, k := range []string{"review_model", "model_context_window", "model_auto_compact_token_limit"} {
		if strings.Contains(out, k) {
			t.Errorf("没值的键 %s 不该出现在文件里\n%s", k, out)
		}
	}
	// 而且**已有**的那一行也不能被我们清空：只写我们要写的键。
	const src = "model_auto_compact_token_limit = 12345\nmodel = \"x\"\n"
	kept, _ := rewrite(src, 8899, want{Model: "normal"})
	if !strings.Contains(kept, "model_auto_compact_token_limit = 12345") {
		t.Errorf("用户自己配的那一行不许被我们碰\n%s", kept)
	}
}

// TestTheCompactLimitComesFromTheProfile：自动压缩阈值写得进去（拿得到的时候）。
//
// 它是 claude 那边 `CLAUDE_CODE_AUTO_COMPACT_WINDOW` 的对位物：codex 按它自己
// 以为的模型元数据估什么时候压缩，而那份元数据对档位名是不存在的。给个 0 是
// 「不声明」——codex 自己的缺省比我们猜一个数更对。
func TestTheCompactLimitComesFromTheProfile(t *testing.T) {
	out, _ := rewrite(sample, 8899, want{Model: "normal", AutoCompact: 800000})
	if !strings.Contains(out, "model_auto_compact_token_limit = 800000") {
		t.Errorf("阈值该写进去\n%s", out)
	}
	none, _ := rewrite(sample, 8899, want{Model: "normal"})
	if strings.Contains(none, "model_auto_compact_token_limit") {
		t.Errorf("0 表示不声明，不该写这一行\n%s", none)
	}
}
