package webdashboard

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 前端那两本字典（`web/src/i18n.ts` 的 en / zh-Hans）必须**键对齐**。
//
// 为什么要一条测试守着：`t()` 查不到时**原样返回英文那句**，而那是刻意的
// （界面上出现一句英文，比空白或者一个键名好）。代价是「漏了一条译文」与
// 「这条故意不翻」在运行时长得一模一样——只有对齐测试能区分它们。
//
// 用 Go 读 TS 而不是装一个前端测试运行器：CI 必须**完全离线**（前端工具链只在
// 开发机上出现，见 web_test.go 那段说明）。这里做的是**文本层面**的检查，
// 不需要 node——和「产物进版本控制 + Go 侧看门」是同一套取舍。
//
// 边界说清楚：它查的是「两本字典的键集合一样」，查不了「译文对不对」（那要人看）。
// 它能拦的是最常见的那个错——加了一句文案只改了英文那本。

var dictStart = regexp.MustCompile(`(?m)^const (en|zhHans): Record<string, string> = \{`)

// keyLine 抓一行开头的键。两种写法都要认：
//
//	"no rows": "没有数据",
//	tail: "尾部",
//
// 也认**键与值分行**的那种（长句子会这么排），因为键那一行仍然以冒号结尾。
var keyLine = regexp.MustCompile(`(?m)^\s*(?:"((?:[^"\\]|\\.)*)"|([A-Za-z_$][A-Za-z0-9_$]*))\s*:`)

func TestFrontendDictionariesHaveTheSameKeys(t *testing.T) {
	raw, err := os.ReadFile("web/src/i18n.ts")
	if err != nil {
		t.Fatalf("读不到前端字典（%v）——路径变了就把这条测试一起改", err)
	}
	src := string(raw)

	// 先按 `const en: … = {` / `const zhHans: … = {` 切块，块结束在行首那个 `};`。
	locs := dictStart.FindAllStringSubmatchIndex(src, -1)
	if len(locs) != 2 {
		t.Fatalf("该找到 en 与 zhHans 两个字典，实际 %d 个——"+
			"文件的写法变了（换名字、换类型标注）就要跟着改这两个正则", len(locs))
	}
	dicts := map[string]map[string]bool{}
	for i, loc := range locs {
		name := src[loc[2]:loc[3]]
		body := src[loc[1]:]
		if end := strings.Index(body, "\n};"); end >= 0 {
			body = body[:end]
		} else if i == len(locs)-1 {
			t.Fatalf("字典 %s 找不到结尾的 `};`", name)
		}
		keys := map[string]bool{}
		for _, m := range keyLine.FindAllStringSubmatch(body, -1) {
			k := m[1]
			if k == "" {
				k = m[2]
			}
			k = strings.ReplaceAll(k, `\"`, `"`)
			keys[k] = true
		}
		if len(keys) == 0 {
			t.Fatalf("字典 %s 一条键都没解析出来——正则与文件对不上了", name)
		}
		dicts[name] = keys
	}

	en, zh := dicts["en"], dicts["zhHans"]
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

// TestFrontendDictionaryKeysAreSentences 钉住「键 = 那句英文」这条约定。
//
// 前端的键与后端 i18n 的 msgid 是同一条传统（见 core/docs 的 i18n 一节）：
// 没有「消息 ID」这层间接。所以一条**像标识符**的键（`saveButton`）说明有人
// 顺手开了个新写法——那会让「界面上这句话是哪来的」重新变成一个要先查表的
// 问题，而这正是这套写法要消掉的东西。
//
// 例外是那几个短词（`tail` / `follow` / `revert` / `unsaved` …）：它们本身就是
// 英文那句，不是给程序看的名字。判据放在长度与形态上，不列名单。
func TestFrontendDictionaryKeysAreSentences(t *testing.T) {
	raw, err := os.ReadFile("web/src/i18n.ts")
	if err != nil {
		t.Fatal(err)
	}
	loc := dictStart.FindStringIndex(string(raw))
	if loc == nil {
		t.Fatal("找不到 en 字典")
	}
	body := string(raw)[loc[1]:]
	if end := strings.Index(body, "\n};"); end >= 0 {
		body = body[:end]
	}

	// camelCase / snake_case 才是「标识符式」的键。单个小写单词（tail、follow）
	// 是句子，放行。
	camel := regexp.MustCompile(`[a-z][A-Z]|_`)
	for _, m := range keyLine.FindAllStringSubmatch(body, -1) {
		k := m[1]
		if k == "" {
			k = m[2]
		}
		if camel.MatchString(k) {
			t.Errorf("键 %q 像个标识符——键该是那句英文本身（见 i18n.ts 的开头说明）", k)
		}
	}
}
