package hermes

import (
	"bytes"
	"io/ioutil"
	"os"
	"strings"

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/store"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
)

// 本文件是 **hermes 的接管**：改写 `$HERMES_HOME/config.yaml`，把它指到本地代理。
//
// 与 codex 同一种机制（MechConfig）：hermes 的模型与 provider 只认那份 YAML，没有
// 环境变量入口。写什么见 plan.go；怎么写（字节手术 + hermes 自己的解析器复核）见
// yamledit.go 与 verify.go。这里只管顺序：
//
//	读原文 → 算改动 → 手术 → 复核 → 备份 → 写之前再读一次（没被别人改过）→ 原子写

// Takeover 是交给 confighook 的那份接管。没有字段：要用到的值都在 Apply 那一刻现算
// （理由见 codex 的 Takeover）。
type Takeover struct{}

var _ agentapi.ConfigTakeover = Takeover{}

func (Takeover) Targets() []string { return []string{ConfigPath()} }

// IsTakenOver 判据是 `providers.newgate` 那一行在不在，不是「文件里出现过 newgate」：
// 后者会被用户自己起的名字命中（理由同 codex 的 IsTakenOver）。
func (Takeover) IsTakenOver(target string) bool {
	b, err := ioutil.ReadFile(target)
	if err != nil {
		return false
	}
	return hasMarker(b)
}

func hasMarker(b []byte) bool {
	_, ok := parseDoc(string(b)).lookup("providers", ProviderID)
	return ok
}

// Apply 完整接管一次。找不到文件时**不创建**（hermes 第一次运行时自己会写一份；
// 没有 = 这台机器上没跑过 hermes，那是该说出来的事）。
func (Takeover) Apply(port int) ([]*agentapi.TakeoverReport, error) {
	if _, err := os.Stat(ConfigPath()); err != nil {
		return []*agentapi.TakeoverReport{(&result{File: ConfigPath(), Missing: true}).report()}, nil
	}
	in, err := currentInputs(port)
	if err != nil {
		return nil, err
	}
	rep, err := rewriteFile(ConfigPath(), true, func(d *doc) ([]edit, catalog) { return fullEdits(d, in) })
	if rep != nil && err == nil && !rep.Missing {
		noteApply(rep)
	}
	return []*agentapi.TakeoverReport{rep.report()}, err
}

// Restore 把接管碰过的每一处放回**原件里的样子**，其余的不动。
//
// # 为什么不是整份文件换回去（codex 是）
//
// hermes 会自己写这份文件：`/model … --global`、`hermes config set`、`hermes setup`、
// 版本迁移都会落盘。接管之后这些改动是用户的；整份换回原件，等于把接管期间他在
// hermes 里改过的一切都丢掉——而 `newgate off hermes` 的承诺只是「把 newgate 拿掉」。
// 所以按路径还原：接管写过的那几处取原件的值（原件里没有就删掉），我们的
// `providers.newgate*` 删掉，别的一个字节都不碰。同样经过 hermes 的解析器复核。
//
// 原件不在（从没接管过）= 无事发生，不是错误。
func (Takeover) Restore() ([]string, error) {
	target := ConfigPath()
	orig, err := ioutil.ReadFile(agentapi.OriginalPath(target))
	if err != nil {
		return nil, nil
	}
	cur, err := ioutil.ReadFile(target)
	if err != nil {
		// 文件没了（用户删了 hermes 的配置）：原件就是我们手上唯一一份，原样放回。
		done, err := agentapi.RestoreFile(target)
		if err != nil || !done {
			return nil, err
		}
		return []string{target}, nil
	}
	od := parseDoc(string(orig))
	d := parseDoc(string(cur))
	var edits []edit
	for _, p := range takenOverPaths(d, od) {
		edits = append(edits, edit{Path: p, FromOrig: true})
		if lines, ind, ok := od.raw(p...); ok {
			if err := d.putRaw(lines, ind, p...); err != nil {
				return nil, restoreErr(target, err)
			}
		} else {
			d.remove(p...)
		}
	}
	out := d.String()
	if out == string(cur) {
		return nil, nil
	}
	if _, err := verifyEdits(string(cur), out, edits, string(orig)); err != nil {
		return nil, restoreErr(target, err)
	}
	if err := writeIfUnchanged(target, cur, []byte(out)); err != nil {
		return nil, err
	}
	return []string{target}, nil
}

func restoreErr(target string, err error) error {
	return i18n.Ef(err, "cannot restore {file} key by key ({err}); the untouched original is at {orig}",
		i18n.A{"file": target, "err": err, "orig": agentapi.OriginalPath(target)})
}

// result 是一次改写的账：改了哪几处、谁复核的、哪些东西没能列进去。
type result struct {
	File    string
	Changes []string
	Parser  string
	Skipped []string
	// Missing 文件不在（没跑过 hermes）。
	Missing bool
	// Paths 真的改了的那些路径（判断要不要重启网关用，见 gateway.go）。
	Paths [][]string
}

func (r *result) report() *agentapi.TakeoverReport {
	if r == nil {
		return &agentapi.TakeoverReport{File: ConfigPath()}
	}
	rep := &agentapi.TakeoverReport{File: r.File, Rewrites: r.Changes}
	if r.Missing {
		rep.Skipped = i18n.T("no such file — run hermes once so it writes its config, then take over again", nil)
	}
	if r.Parser == "" && len(r.Changes) > 0 {
		rep.Rewrites = append(rep.Rewrites, i18n.T("(not cross-checked by hermes' YAML parser: its interpreter was not found)", nil))
	}
	for _, s := range r.Skipped {
		rep.Rewrites = append(rep.Rewrites, i18n.T("not listed in /model: {what}", i18n.A{"what": s}))
	}
	return rep
}

// rewriteFile 是 Apply 与后台同步共用的那条写路径。backup = 这一次要不要留一份带
// 时间戳的快照（完整接管要；后台同步不要——它可能一天跑很多次，会把有用的快照挤掉，
// 而它只碰我们自己那几行）。
func rewriteFile(target string, backup bool, plan func(*doc) ([]edit, catalog)) (*result, error) {
	res := &result{File: target}
	raw, err := ioutil.ReadFile(target)
	if err != nil {
		res.Missing = true
		return res, nil
	}
	d := parseDoc(string(raw))
	edits, c := plan(d)
	res.Skipped = c.Skipped
	var applied []edit
	for _, e := range edits {
		changed, err := d.put(e)
		if err != nil {
			return res, i18n.Ef(err, "{file} has a shape newgate will not edit by hand ({err}); nothing was written",
				i18n.A{"file": target, "err": err})
		}
		if changed {
			applied = append(applied, e)
			res.Paths = append(res.Paths, e.Path)
			res.Changes = append(res.Changes, describe(e))
		}
	}
	if len(applied) == 0 {
		return res, nil
	}
	out := d.String()
	// 复核的是**全部**改动而不只是变了的那些：没变的那几处也必须在结果里是想要的值。
	v, err := verifyEdits(string(raw), out, edits, "")
	if err != nil {
		return res, i18n.Ef(err, "refusing to write {file}: {err}", i18n.A{"file": target, "err": err})
	}
	res.Parser = v.Parser
	if backup {
		if err := agentapi.BackupFile(target, hasMarker); err != nil {
			return res, err
		}
	}
	return res, writeIfUnchanged(target, raw, []byte(out))
}

// writeIfUnchanged 写之前再读一次：这期间 hermes 自己写过这份文件（`/model --global`
// 就会），就**不写**——我们手上那份改动是按旧内容算的，写下去等于把它刚写的东西
// 覆盖掉。报一句话，下一次再来（后台同步会自己重试，CLI 重敲一次即可）。
func writeIfUnchanged(target string, base, out []byte) error {
	now, err := ioutil.ReadFile(target)
	if err != nil || !bytes.Equal(now, base) {
		return i18n.E("{file} changed while newgate was editing it; nothing was written — try again",
			i18n.A{"file": target})
	}
	return agentapi.WriteAtomic(target, out, 0o600)
}

// describe 一处改动写成给人看的一行（值里的密钥不会出现：我们写的只有占位值）。
func describe(e edit) string {
	p := strings.Join(e.Path, ".")
	switch v := e.Value.(type) {
	case nil:
		return i18n.T("{path} removed", i18n.A{"path": p})
	case omap:
		return i18n.T("{path} written", i18n.A{"path": p})
	default:
		s, _ := scalarLit(v)
		return p + " -> " + s
	}
}

// currentInputs 问出这一刻该写的全部事实。
//
// newgate 自己的配置读不出来时**返回错误，不返回一份空清单**：空清单算出来的改动是
// 「把每一份 profile 那一行都删掉」——一次手误改坏 providers.json，后台同步就会把
// hermes 里能选的模型清空。读不出来 = 这一轮什么都不做。
func currentInputs(port int) (inputs, error) {
	in := inputs{Port: port, Tiers: map[string]string{}}
	for _, s := range Agent().Slots {
		in.Tiers[s.Name] = tierFor(s.Name)
	}
	snap, err := store.Load()
	if err != nil || snap == nil {
		return in, i18n.Ef(err, "cannot read the newgate config ({err}); hermes' config.yaml was left alone",
			i18n.A{"err": err})
	}
	in.Active = snap.State.ActiveFor(ID)
	in.Profiles = append([]*domain.Profile(nil), snap.Profiles...)
	// store.Load 跳过读不出来的 profile（坏一份不挡别的）。对我们来说「读不出来」
	// ≠「没有」：那一行留着别动，等它修好了再同步（见 catalogEdits）。
	if names, err := store.ListProfiles(); err == nil {
		loaded := map[string]bool{}
		for _, p := range snap.Profiles {
			loaded[p.Name] = true
		}
		for _, n := range names {
			if !loaded[n] {
				in.Unreadable = append(in.Unreadable, n)
			}
		}
	}
	return in, nil
}

// syncCatalog 把 /model 的清单同步一次（后台、命令行、卡片三处共用）。
func syncCatalog() (*result, error) {
	in, err := currentInputs(proxyPort())
	if err != nil {
		return nil, err
	}
	return rewriteFile(ConfigPath(), false, func(d *doc) ([]edit, catalog) { return catalogEdits(d, in) })
}

// proxyPort 是接管要写进 base_url 的端口，与命令行同一个来源（理由见 codex 的 proxyPort）。
func proxyPort() int {
	if st := store.LoadState(); st != nil && st.Port != 0 {
		return st.Port
	}
	return domain.ProxyPort
}
