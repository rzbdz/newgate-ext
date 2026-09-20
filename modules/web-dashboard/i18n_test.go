package webdashboard

import (
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 前端那两本字典（`web/src/i18n.ts` 的 en / zh-Hans）必须**键对齐**，而且每一句
// 都要真的出现在**发出去的那个 bundle** 里。
//
// 为什么要测试守着：`t()` 查不到时**原样返回英文那句**，而那是刻意的（界面上出现
// 一句英文，比空白或者一个键名好）。代价是「漏了一条译文」与「这条故意不翻」在
// 运行时长得一模一样——只有对齐测试能区分它们。
//
// 用 Go 读 TS/JS 而不是装一个前端测试运行器：CI 必须**完全离线**（前端工具链只在
// 开发机上出现，见 web_test.go 那段说明）。这里做的是**文本层面**的检查，不需要
// node——和「产物进版本控制 + Go 侧看门」是同一套取舍。
//
// 边界说清楚：查不了「译文对不对」（那要人看），也查不了「bundle 是不是最新的」
// 全部——只查得住**字典这一半**（见 TestShippedBundleHasEverySentence）。

var dictStart = regexp.MustCompile(`(?m)^const (en|zhHans): Record<string, string> = \{`)

// keyLine 抓一行开头的键。两种写法都要认：
//
//	"no rows": "没有数据",
//	tail: "尾部",
//
// 也认**键与值分行**的那种（长句子会这么排），因为键那一行仍然以冒号结尾。
var keyLine = regexp.MustCompile(`(?m)^\s*(?:"((?:[^"\\]|\\.)*)"|([A-Za-z_$][A-Za-z0-9_$]*))\s*:`)

func TestFrontendDictionariesHaveTheSameKeys(t *testing.T) {
	en := dictionaryKeys(t, "en")
	zh := dictionaryKeys(t, "zhHans")

	var missingZh, missingEn []string
	for k := range en {
		if !zh[k] {
			missingZh = append(missingZh, k)
		}
	}
	for k := range zh {
		if !en[k] {
			missingEn = append(missingEn, k)
		}
	}
	sort.Strings(missingZh)
	sort.Strings(missingEn)

	for _, k := range missingZh {
		t.Errorf("zh-Hans 缺这条（界面上会静默显示英文）: %q", k)
	}
	for _, k := range missingEn {
		t.Errorf("en 缺这条（键就是那句英文，缺了说明它从别处被删了）: %q", k)
	}
}

// TestShippedBundleHasEverySentence 是「改了前端、忘了 `pnpm build`」的看门人。
//
// web_test.go 那条说得很清楚：它查不了「产物是不是最新的」（那要跑 node，而 CI
// 必须离线）。这条补上其中**最常见的一半**：界面上的每一句话都必须出现在真正发
// 出去的那个 bundle 里。加了一句文案而没重新构建时，源文件里有、bundle 里没有
// ——用户界面上那句话根本不会出现（回落成键名本身，也就是英文原文）。
//
// 查不了的另一半照旧：改样式、改布局、改逻辑时这条一无所知（那些不动字典）。
// 诚实的边界比一个看着很全的假保证有用。
func TestShippedBundleHasEverySentence(t *testing.T) {
	bundle := shippedJS(t)
	if bundle == "" {
		t.Fatal("产物里一个 .js 都没有——web/dist 没构建就提交了？")
	}

	var missing []string
	for k := range dictionaryKeys(t, "en") {
		if !strings.Contains(bundle, jsStringLiteral(k)) {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	for _, k := range missing {
		t.Errorf("这句话在源字典里，却不在发出去的 bundle 里: %q\n"+
			"    ——改完前端要 `cd web && pnpm build` 再提交（产物进版本控制，见 README）", k)
	}
}

// dictionaryKeys 把 `web/src/i18n.ts` 里某个字典的键抓出来。
func dictionaryKeys(t *testing.T, want string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("web/src/i18n.ts")
	if err != nil {
		t.Fatalf("读不到前端字典（%v）——路径变了就把这条测试一起改", err)
	}
	src := string(raw)
	locs := dictStart.FindAllStringSubmatchIndex(src, -1)
	if len(locs) != 2 {
		t.Fatalf("该找到 en 与 zhHans 两个字典，实际 %d 个——"+
			"写法变了（换名字、换类型标注）就要跟着改这两个正则", len(locs))
	}
	for _, loc := range locs {
		if src[loc[2]:loc[3]] != want {
			continue
		}
		body := src[loc[1]:]
		if end := strings.Index(body, "\n};"); end >= 0 {
			body = body[:end]
		}
		keys := map[string]bool{}
		for _, m := range keyLine.FindAllStringSubmatch(body, -1) {
			k := m[1]
			if k == "" {
				k = m[2]
			}
			keys[strings.ReplaceAll(k, `\"`, `"`)] = true
		}
		if len(keys) == 0 {
			t.Fatalf("字典 %s 一条键都没解析出来——正则与文件对不上了", want)
		}
		return keys
	}
	t.Fatalf("没有名为 %s 的字典", want)
	return nil
}

// shippedJS 把**发出去的**那份 JS 读出来。
//
// 读的是嵌入的那份（`assets`），不是磁盘上的源：这是用户真正拿到的东西，也正是
// 「产物进版本控制」这个取舍下唯一有意义的对象。产物按内容哈希命名，所以文件名
// 每次构建都变——按后缀遍历，别写死。
func shippedJS(t *testing.T) string {
	t.Helper()
	sub, err := fs.Sub(assets, AssetDir)
	if err != nil {
		t.Fatalf("嵌入的前端目录读不出来（%s 不存在？）: %v", AssetDir, err)
	}
	var out strings.Builder
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return walkErr
		}
		raw, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		out.Write(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("遍历产物: %v", err)
	}
	return out.String()
}

// jsStringLiteral 把一句话写成 JS 字面量的样子（加引号、转义内部引号），用来在
// bundle 里做**字面**查找。
//
// 用 Contains 而不是正则：键里有 `.`、`{`、`(`、`—` 这类字符，当正则用会误判
// （轻则漏，重则把不该匹配的匹配上）。
func jsStringLiteral(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// TestFrontendDictionaryKeysAreSentences 钉住「键 = 那句英文」这条约定。
//
// 前端的键与后端 i18n 的 msgid 是同一条传统（见 core/docs 的 i18n 一节）：
// 没有「消息 ID」这层间接。所以一条**像标识符**的键（`saveButton`）说明有人
// 顺手开了个新写法——那会让「界面上这句话是哪来的」重新变成一个要先查表的
// 问题，而这正是这套写法要消掉的东西。
//
// 例外是那几个短词（`tail` / `follow` / `revert` / `unsaved` …）：它们本身就是
// 英文那句，不是给程序看的名字。判据放在形态上，不列名单。
func TestFrontendDictionaryKeysAreSentences(t *testing.T) {
	// camelCase / snake_case 才是「标识符式」的键。单个小写单词（tail、follow）
	// 是句子，放行。
	camel := regexp.MustCompile(`[a-z][A-Z]|_`)
	for k := range dictionaryKeys(t, "en") {
		if camel.MatchString(k) {
			t.Errorf("键 %q 像个标识符——键该是那句英文本身（见 web/src/i18n.ts 的开头说明）", k)
		}
	}
}
