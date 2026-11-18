package locale

import (
	"context"
	"os"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/i18n"
)

// TypeInfra 是这个模块在图里的分类标签（内核不定义取值，产品自己填）。
const TypeInfra = "infra"

// service 是端口的实现：它只持有「这次解析出来的结果」，不持有目录——目录在
// lib/i18n 那一层（谁都能查，不必拿到端口）。
type service struct {
	lang      string
	requested string
	source    Source
}

func (s *service) Lang() string           { return s.lang }
func (s *service) Requested() string      { return s.requested }
func (s *service) Source() Source         { return s.source }
func (s *service) Available() []i18n.Info { return i18n.Available() }

// New 装配语言这件事。
//
// 依赖方向：**只依赖 confighook**（登记 state.json 字段必须走它的端口，与
// plugin-manager 同款），加一条对界面的弱依赖（有界面就把 `newgate lang` 与那行
// status 挂上去，没界面就跳过——本模块最重要的工作「装目录」不依赖任何东西）。
//
// 它必须**可摘**：摘掉之后 lib/i18n 里没有目录，每句话原样输出源语言文本，
// 进程照常（`app/matrix_test.go` 的摘除矩阵会真的摘一次）。
func New() modules.Component {
	svc := &service{}
	var releases []modules.Release
	return modules.Component{
		Name: "locale",
		Desc: func() string {
			return i18n.T("which language this process speaks: resolve it, install the tables, honour disk overrides", nil)
		},
		Type: TypeInfra,
		Requires: []modules.Requirement{
			modules.Optional(cliapi.Capability),
			// web 界面：在就把「语言」那张卡挂上去（见 view.go），不在就跳过。
			// 与 CLI 那条同为**弱依赖**——本模块最重要的工作「装目录」谁也不依赖。
			modules.Optional(viewapi.Capability),
			modules.Need(confighookapi.ConfigHooksCapability),
		},
		Provides: []modules.Provision{modules.Provide(Capability, Service(svc))},
		Start: func(_ context.Context, ctx modules.Context) error {
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			field, err := hooks.RegisterStateField("locale", StateKey)
			if err != nil {
				return err
			}
			releases = append(releases, field)

			req := resolve(os.Getenv, loadConfigured())
			led, cats, err := i18n.Builtin()
			if err != nil {
				return err
			}
			// 磁盘覆盖：`$NEWGATE_HOME/locale/<tag>.json` 能改内置译文里的任何一条，
			// 不必重新编译（同 mappings/ 那条路子）。
			eff, err := i18n.Install(req.Tag, led, cats, i18n.OverlayDir(paths.Root()))
			if err != nil {
				return err
			}
			svc.lang, svc.requested, svc.source = eff, i18n.Normalize(req.Tag), req.Source

			// web 界面：登记那张语言卡（`newgate lang` 的等价物，见 view.go）。
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				rel, err := registerView(v)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}

			cli, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil // 没装界面：语言照常生效，只是没有入口
			}
			cmd := &command{svc: svc}
			for _, reg := range []func() (modules.Release, error){
				func() (modules.Release, error) { return cli.RegisterCommand(cmd) },
				func() (modules.Release, error) { return cli.RegisterStatus(cmd) },
				func() (modules.Release, error) { return cli.RegisterDiagnostics(cmd) },
				func() (modules.Release, error) { return cli.RegisterGlossary(cmd) },
			} {
				rel, err := reg()
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
