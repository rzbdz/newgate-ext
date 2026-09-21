package codex

import (
	"encoding/json"
	"os"
	"strings"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/store"
)

// modelsConceptID 是这张卡的稳定身份。
const modelsConceptID = "codex.models"

// 本文件是**那张能填的表**：codex 的哪个模型名落到我们哪一档。
//
// # 为什么它必须是一张能填的表，而不是一个 JSON 文件
//
// 第一版把这张表做成了 `$NEWGATE_HOME/codex-models.json` 这样一个文件——「动态」
// 是动态了，但用户的原话是：
//
//	我用了改名模式，为什么他妈的，没有映射表让用户填写啊
//
// 他说得对，而且指出的是一个**界面缺口**：切到改名模式之后，用户在 codex 里换一个
// 模型就 404，而他能做的只有「去编辑一个 JSON 文件」——那不是一个产品该给人的答案。
// 这张表是**产品的一部分**（哪个名字算哪一档是用户自己的取舍），它就该和别的取舍
// 一样住在界面上。
//
// 文件仍然是它的家（那张表要能被手写、被脚本改、被带进版本控制），但界面这一份是
// **主要入口**：读的时候文件里没有 `models` 就用出厂那五条，有就以它为准（见
// models.go 的 effectiveModels），写的时候把整张表写回去。
//
// # 写入语义：整表覆盖
//
// 保存时写的是**界面上那一整张表**，不是「改了哪几行」。所以删掉一行就是删掉了
// （出厂值也不会再把它顶回来）——这是这张卡能成立的前提：所见即所得。文件里
// `models` 一旦存在，它就说了算；不存在（或从没被这张卡保存过）才用出厂那五条。
//
// `mode` 那一格**原样保留**：它不属于这张表（见 models.go），保存表不该把它抹掉。

// modelsConcept 是那张映射表。
func modelsConcept() view.Concept {
	entries := effectiveModels()
	items := make([]view.Record, 0, len(entries))
	for _, e := range entries {
		items = append(items, modelRecord(e))
	}
	return view.Concept{
		ID: modelsConceptID, Kind: view.KindRecords,
		Title: i18n.T("Codex model names", nil),
		// Order 21：紧跟「Codex 模型」那张卡（20）。这两张说的是同一件事的两半
		// ——那一张说「走哪一档」，这一张说「codex 的哪个名字算哪一档」。
		Order: 21,
		Data: view.Records{
			Items:    items,
			Base:     store.Revision(ModelsFile()),
			CanAdd:   true,
			AddLabel: i18n.T("+ model", nil),
		},
		Apply: applyModels,
		// 模式这一句**住在这里**，与那张表同一张卡：改名模式下这张表才真的在生效，
		// 而 takeover 模式下它一个字节都不影响（codex 只发档位名）。分开放的话，
		// 用户看着一张表却不知道它此刻算不算数。
		Note: modeNote(),
		// 切换的按钮：只画「另一个」（理由见 modeActions）。
		Actions: modeActions(),
	}
}

// modelRecord 把一条映射摆成一张小表单：左边 codex 认的名字，右边我们哪一档。
func modelRecord(e modelTier) view.Record {
	return view.Record{
		// ID 用 slug：它是这一行此刻的身份。改名字（左边那一格）= 改的是**值**，
		// 不是新加一条——与 providers 那张表同一条规矩。
		ID: e.Slug, Label: e.Slug, Removable: true,
		Fields: []view.Field{
			{
				ID: "slug", Label: i18n.T("codex model", nil), Kind: view.FieldText, Value: e.Slug,
				Placeholder: i18n.T("e.g. gpt-5.6-luna", nil),
				Why:         i18n.T("The model name codex itself sends — copy it out of `codex debug models` (the slug).", nil),
			},
			{
				ID: "tier", Label: i18n.T("tier", nil), Kind: view.FieldSelect, Value: e.Tier,
				Options: append([]string{""}, domain.Roles...),
				Why:     i18n.T("Which tier a request naming that model resolves to. Required — a row without one is rejected on save.", nil),
			},
		},
	}
}

// applyModels 把界面上那张表写回文件。
//
// 语义见文件头：**整表覆盖**。校验只有两条，都是「不校验就会静默出错」的那种：
// 名字空（那一行什么也匹配不到，而用户会以为加上了）、两行同名（后一行永远轮不到，
// 而界面上两行都画着）。
func applyModels(edit json.RawMessage, base string) (string, error) {
	var patch struct {
		Items []struct {
			ID     string            `json:"id"`
			Values map[string]string `json:"values"`
		} `json:"items"`
	}
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the table in this request is not readable: {err}", i18n.A{"err": err})
	}

	out := make([]modelTier, 0, len(patch.Items))
	seen := map[string]bool{}
	for _, it := range patch.Items {
		slug := strings.TrimSpace(it.Values["slug"])
		tier := strings.TrimSpace(it.Values["tier"])
		if slug == "" {
			return "", i18n.E("a row has no model name — nothing would ever match it", nil)
		}
		if tier == "" {
			// 空档位**不许保存**，而不是「存下来但不算数」：effectiveModels 会把
			// 没有档位的行丢掉（它匹配不上任何东西），于是界面上刚加的那一行会在
			// 保存之后**自己消失**——用户看到的是「加了、没了、也没报错」。
			return "", i18n.E("the row for {slug} has no tier — pick one, or remove the row",
				i18n.A{"slug": slug})
		}
		if seen[slug] {
			return "", i18n.E("two rows are both named \"{slug}\" — only one of them could ever match",
				i18n.A{"slug": slug})
		}
		seen[slug] = true
		out = append(out, modelTier{Slug: slug, Tier: tier})
	}

	// 从**盘上那份的原文**起手，只换 models 这一段。
	//
	// 为什么不是「解成 Models 再序列化」：那样会把**不认识的键丢掉**（今天的
	// `mode`、将来往这份文件里加的任何一个键），而丢掉它们在界面上只表现为
	// 「保存成功」——2026-09-21 的一条单测就是这么抓到第一版的（`something_else`
	// 没了）。所以按原文只改一段，与 providers 那条路同一个做法。
	doc := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(ModelsFile()); err == nil && len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return "", i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": "codex-models.json"})
		}
	}
	body, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	doc["models"] = body
	// `mode` 缺席时补上**这一刻生效的那个**：文件里 `models` 一旦存在它就是权威，
	// 而模式若还是空的，读的人（Mode()）会当 takeover——那与用户刚才看到的界面
	// 不一致。写全是为了「文件说的事 = 界面说的事」。
	if _, ok := doc["mode"]; !ok {
		b, err := json.Marshal(Mode())
		if err != nil {
			return "", err
		}
		doc["mode"] = b
	}
	text, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	next, err := store.WriteIfUnchanged(ModelsFile(), base, append(text, '\n'))
	if err != nil {
		return "", modelsErr(err)
	}
	return next, nil
}

// modelsErr 是那张表写不进去时给人的一句话。
//
// 冲突那一支单独写：**磁盘上那份不是我们弄坏的**（用户手改过、或者另一个标签页
// 先保存了），而界面上那份表看起来完全正常——不说清就只能靠猜。
func modelsErr(err error) error {
	if sc, ok := err.(*store.StaleError); ok {
		return i18n.E("{file} changed on disk since this page loaded it — reload and apply again ({err})",
			i18n.A{"file": "codex-models.json", "err": sc.Error()})
	}
	return i18n.Ef(err, "cannot write {file}: {err}", i18n.A{"file": "codex-models.json"})
}
