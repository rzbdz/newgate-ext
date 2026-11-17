package setup

// providers.json 的「原样读、改一处、写回去」。
//
// 内核的 store.LoadProviders 会把 api_key_env 解析成 api_key 明文（转发层要用），
// 所以**它的结果不能写回磁盘**——那等于把每个用环境变量管 key 的用户都变成
// 「密钥躺在文件里」（内核 SaveProviders 的注释专门警告了这一条）。
//
// 而向导又必须能更新 providers.json 里的**一个键**，同时一字不动别人那些。
// 于是这里自己读一次原始 JSON：json.RawMessage 保住每一段原文，只替换我们那一格。
// 字段名与内核 domain.Provider 的 tag 对齐（那是落盘 schema，见 core/.../domain）。

import (
	"encoding/json"
	"io/ioutil"
	"os"

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/paths"
)

// loadProvidersRaw 读 providers.json 的原始形态：
//
//	provs  强类型视图（向导要往里加/改一个键）
//	raw    原文（哪些 provider 用了 api_key_env —— 那几个的明文不能被写回去）
//
// 文件不存在时给一份空的表（首次 bootstrap 就是这种情形），不报错。
func loadProvidersRaw() (*domain.Providers, map[string]bool, error) {
	resolved := map[string]bool{}
	provs := &domain.Providers{Providers: map[string]domain.Provider{}}

	b, err := ioutil.ReadFile(paths.ProvidersFile())
	if err != nil {
		if os.IsNotExist(err) {
			return provs, resolved, nil
		}
		return nil, nil, i18n.Ef(err, "cannot read {file}: {err}", i18n.A{"file": paths.ProvidersFile()})
	}
	if len(b) == 0 {
		return provs, resolved, nil
	}
	var doc struct {
		Providers map[string]json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, nil, i18n.Ef(err, "{file} is not valid JSON: {err}", i18n.A{"file": paths.ProvidersFile()})
	}
	for name, rawMsg := range doc.Providers {
		var p domain.Provider
		if err := json.Unmarshal(rawMsg, &p); err != nil {
			return nil, nil, i18n.Ef(err, "provider {name} in {file} is malformed: {err}",
				i18n.A{"name": name, "file": paths.ProvidersFile()})
		}
		if p.APIKey == "" && p.APIKeyEnv != "" {
			resolved[name] = true
		}
		provs.Providers[name] = p
	}
	return provs, resolved, nil
}

// stripResolvedKeys 把「靠环境变量取 key」的那些 provider 的明文抹掉。
//
// 为什么需要它：上面的 doc 走的是 json 解码，没有把 api_key_env 求值——但
// **调用之前**如果同一份结构被 LoadProviders 碰过就会带上明文。向导里不碰它，
// 这一道是给「将来有人改了这条路径却没看见这个注释」留的闸：宁可把明文丢掉
// （环境变量仍在，行为不变），也不能把它写回磁盘。
func stripResolvedKeys(p *domain.Providers, resolved map[string]bool) {
	for name := range resolved {
		v, ok := p.Providers[name]
		if !ok {
			continue
		}
		v.APIKey = ""
		p.Providers[name] = v
	}
}
