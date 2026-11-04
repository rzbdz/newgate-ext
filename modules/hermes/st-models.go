package hermes

import (
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/modules/gateway/special"
)

// modelsLister 替 hermes 的 URL 回答 `GET …/v1/models`（内核的 special.ModelLister）。
//
// # 为什么要它（2026-11-04 本机实测）
//
// hermes 的 `/model` 对每个具名 provider 都去问一次 `<base>/models`，问到了就**用问到
// 的替换**我们写在 config.yaml 里的模型表。代理的缺省清单是档位 + omo 那批动态角色键：
// 钉住某份 profile 的那几行（`newgate-<profile>`）于是只剩档位、具体模型全没了，而
// 跟着走的那一行多出二十来个对 hermes 毫无意义的 `omo-*` / `cat-*`。
//
// 第一版的绕法是在每一行写 `discover_models: false`（picker 认它）——代价是 hermes 的
// 配置归一化**不认**这个键，每归一化一次就往 errors.log 里写一行 warning，一行
// provider 一条、每一轮好几次（实测一次 `hermes chat` 九条）。让代理答对这份清单，
// 那个键就不需要了；顺带的好处是清单**现问现答**：profile 里的绑定改了，下一次
// `/model` 就看得见，不必等文件同步。
//
// 答的东西与写进文件的那份是**同一个函数**算的（concreteModels），所以两边不会分家。
//
// 它不改任何请求：Match 永远是 false。名字出现在 `newgate st` 里，关掉它
// （`newgate st off hermes-models`）就回到缺省清单——hermes 照样能用，只是 /model 里
// 只剩档位。
type modelsLister struct{}

var (
	_ special.Plugin      = modelsLister{}
	_ special.ModelLister = modelsLister{}
)

func (modelsLister) Name() string { return "hermes-models" }

func (modelsLister) Why() string {
	return i18n.T("hermes' /model replaces its configured model table with whatever <base>/models returns; "+
		"answer the tiers for the follow row and tiers + that profile's bound models for each pinned row", nil)
}

func (modelsLister) Match(*special.Request) bool { return false }

func (modelsLister) Apply(body []byte, _ *special.Request) ([]byte, []string, error) {
	return body, nil, nil
}

func (modelsLister) ListModels(agent, profile string) ([]string, bool) {
	if agent != ID {
		return nil, false
	}
	if profile == "" {
		return append([]string(nil), domain.Roles...), true
	}
	snap, err := store.Load()
	if err != nil || snap == nil {
		return nil, false
	}
	for _, p := range snap.Profiles {
		if p.Name == profile {
			ids, _ := concreteModels(p)
			return append(append([]string(nil), domain.Roles...), ids...), true
		}
	}
	return nil, false
}
