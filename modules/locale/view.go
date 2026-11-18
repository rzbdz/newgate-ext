package locale

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/i18n"
)

// 本文件是「这门进程说哪门语言」交给 web 界面的那一面（`newgate lang` 的等价物）。
//
// 与 CLI 那边（command.go）是**同一份知识的两个入口**：语言住哪、怎么解析、改了
// 之后谁压着它，都是本模块的事；界面只是把这张卡画出来、把用户挑的值递回来。
//
// 它挂在**语言模块自己**身上而不是让 web-dashboard 特判一个「语言」：界面一旦
// 认识「语言」这件事，它就同时认识了「语言住在 state.json 的 module_config.locale」
// 与「换语言要让进程重新装表」——那两件事都只有本模块知道（见 docs/09 的那条
// 「谁的状态谁自己报」）。

// registerView 挂上语言卡。没有 web 界面时什么都不做（`newgate lang` 照常）。
func registerView(v view.Service) (modules.Release, error) {
	return v.Register("locale",
		view.Title(func() string { return i18n.T("Language", nil) }).Of(view.FieldSettings),
		concepts)
}

// concepts 是**有人来问的时候**才算的（见 lib/view 的包注释）：登记那一刻一个
// 文件都不读。
func concepts() ([]view.Concept, error) {
	return []view.Concept{languageConcept()}, nil
}

// languageConcept 是那张卡。
//
// 它和 config 的「全局设置」卡**写的是同一份文件**（state.json），但各报各的字段
// ——那一份文件里住着好几个模块的段，整份端上去让用户改，等于让界面替所有模块做
// 决定（与 config 那边同一条规矩）。所以两张卡都带 File=state.json：界面按「这份
// 概念说的是哪份文件」把控件与原文两半配成一对并排（见前端 nav.ts 的 fileOf）。
func languageConcept() view.Concept {
	file := paths.StateFile()
	return view.Concept{
		ID: "locale.language", Kind: view.KindToggles,
		Title: i18n.T("Language", nil),
		Data: view.Toggles{
			File: paths.RelToRoot(file), Base: store.Revision(file),
			Items: []view.ToggleItem{{
				ID:      "lang",
				Kind:    view.ToggleSelect,
				Value:   i18n.Current(),
				Options: tags(),
				Label:   i18n.T("Language", nil),
				Why: i18n.T("Which language this process speaks. Saved to state.json and "+
					"applied at once — no restart.", nil),
			}},
		},
		Apply: applyLanguage,
	}
}

// tags 是可选语言（`newgate lang` 那份名单的机器取值）。
func tags() []string {
	avail := i18n.Available()
	out := make([]string, 0, len(avail))
	for _, info := range avail {
		out = append(out, info.Language)
	}
	return out
}

// applyLanguage 写进 state.json，并**当场生效**。
//
// 两步缺一不可，而第二步正是这张卡存在的理由：CLI 那边写完就结束（下一次进程自己
// 会读到），而 daemon 是长命的——只写盘不换表的话，用户挑完中文，界面会一直停在
// 原来那门语言上，直到某次重启。那看起来只像「保存没生效」。
//
// 换表走 `i18n.Use`（不重装目录表），不是 `i18n.Install`：发行版的译文是装配期
// Extend 进来的，重装会把它们冲掉（见 Use 的注释）。
func applyLanguage(edit json.RawMessage, base string) (string, error) {
	var patch struct {
		Lang *string `json:"lang"`
	}
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the language in this request is not readable: {err}", i18n.A{"err": err})
	}
	if patch.Lang == nil {
		return "", i18n.E("the request says nothing about the language", nil)
	}
	match := i18n.Match(*patch.Lang, tags())
	if match == "" {
		return "", i18n.E("No such language: {tag}. Available: {list}",
			i18n.A{"tag": *patch.Lang, "list": strings.Join(tags(), " ")})
	}

	file := paths.StateFile()
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	// 只动 module_config.locale 那一格：state.json 里住着好几个模块各自的段，
	// 整份重写会拿这一处的知识覆盖别人的（与 config 的 applyStateDefaultProfile
	// 同一个理由）。
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": filepath.Base(file)})
	}
	var mods map[string]json.RawMessage
	if raw, ok := doc["module_config"]; ok {
		if err := json.Unmarshal(raw, &mods); err != nil {
			return "", i18n.Ef(err, "the module_config section is not an object: {err}", nil)
		}
	}
	if mods == nil {
		mods = map[string]json.RawMessage{}
	}
	raw, err := json.Marshal(persisted{Lang: match})
	if err != nil {
		return "", err
	}
	mods[StateKey] = raw
	modsRaw, err := json.Marshal(mods)
	if err != nil {
		return "", err
	}
	doc["module_config"] = modsRaw
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}

	rev, err := store.WriteIfUnchanged(file, base, append(out, '\n'))
	if err != nil {
		return "", conflict("locale.language", file, err, out)
	}
	// 落盘成功之后才换表：反过来（先换表后写盘）会让写盘失败时**这一次进程**的
	// 语言与文件里记的不一致，而用户看到的界面已经变了——那种不一致最难归因。
	i18n.Use(match)
	return rev, nil
}

// conflict 把「基线对不上」翻成界面认识的那种冲突（带两边的原文）。
//
// 这六行与 modules/config/view.go、modules/pluginmanager/view.go 里那两段是同一件
// 事，抄三份是**刻意**的（理由写在 pluginmanager 那一份上：另一个选择是让 store
// 认识 view 契约，那更糟）。判据是 `Conflict` 加字段时三处都要跟。
func conflict(conceptID, file string, err error, yours []byte) error {
	var stale *store.StaleError
	if errors.As(err, &stale) {
		return &view.Conflict{
			Concept: conceptID, Path: paths.RelToRoot(file),
			Base: stale.Base, Current: stale.Current,
			Yours: string(yours), Theirs: string(stale.Disk),
		}
	}
	return err
}
