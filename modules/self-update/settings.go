package selfupdate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"

	"github.com/rzbdz/newgate-ext/manifest/distinfo"
)

// stateKey 是 state.json 里属于本模块的那一段（core 不认识这个键，见
// domain.State.ModuleConfig）。
const stateKey = "self-update"

const (
	modeRelease = "release"
	modeGit     = "git"
)

// settings 是本模块在 state.json 里自己那一段。
type settings struct {
	// Mode 是 release 或 git。
	Mode string `json:"mode"`
	// Repo 是 git 模式的本地 checkout（仓库根，绝对路径）。
	Repo string `json:"repo,omitempty"`
	// GitHub 覆盖规格书里的发布仓库（`owner/repo`）。空 = 用规格书的——fork 的人
	// 一般改规格书；这里是给「同一个二进制、换个仓库拉」留的口子。
	GitHub string `json:"github,omitempty"`
	// DecidedBy 记这个决定是谁做的：first-run（第一次运行时推断）或 user。
	// 只给人看——「我从没设过，怎么是 git」的答案。
	DecidedBy string `json:"decided_by,omitempty"`
}

// github 是这次要用的发布仓库：自己那段的覆盖值，否则规格书声明的。
func (s settings) github() string {
	if s.GitHub != "" {
		return s.GitHub
	}
	return distinfo.Current().GitHub
}

// sourceDir 是构建期注入的源码目录（测试换掉它）。
var sourceDir = distinfo.Source

func loadSettings() (settings, bool) {
	raw := store.LoadState().ModuleConfig[stateKey]
	if len(raw) == 0 {
		return settings{}, false
	}
	var s settings
	if err := json.Unmarshal(raw, &s); err != nil || (s.Mode != modeRelease && s.Mode != modeGit) {
		// 写坏了的一段当作没有：重新推断比拿一段读不懂的东西去换二进制好。
		return settings{}, false
	}
	return s, true
}

// saveSettings 写回自己那一段。拿配置目录的写锁：state.json 是大家共用的文件，
// 读-改-写之间别人的改动不能被这一次整段盖掉。
func saveSettings(s settings) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = store.WithLock(func() error {
		st := store.LoadState()
		if st.ModuleConfig == nil {
			st.ModuleConfig = map[string][]byte{}
		}
		st.ModuleConfig[stateKey] = raw
		return store.SaveState(st)
	})
	return err
}

// ensureSettings 返回生效的设置；还没有那一段时推断一份，**推断得有把握才写下来**。
//
// 第二个返回值说的是「这份是不是落了盘」。没把握的情况只有一种：构建期注入的源码
// 目录**看不见**（不是不存在，而是没权限 stat，比如 daemon 以另一个用户跑、源码在
// /root 下）。这时写下 release 会把开发机永久改成 release，所以只在内存里用推断值，
// 等一个看得见的进程来写。
func ensureSettings() (settings, bool) {
	if s, ok := loadSettings(); ok {
		return s, true
	}
	s, sure := infer(sourceDir(), distinfo.Spec())
	if !sure {
		return s, false
	}
	// state.json 还不存在就不写：`newgate init` 看「文件在不在」决定要不要种一份
	// 缺省状态（端口、缺省 profile），抢先写进去一段会让它以为已经初始化过了。
	if _, err := os.Stat(paths.StateFile()); err != nil {
		return s, false
	}
	s.DecidedBy = "first-run"
	if err := saveSettings(s); err != nil {
		return s, false
	}
	return s, true
}

// infer 按构建期的源码目录猜这台机器走哪条路。
//
//   - 目录在、是一份带 build/build.sh 与这份规格书的 git checkout → git；
//   - 目录不在（CI 编的产物带的是 runner 的路径）→ release；
//   - 没注入（`go run`、测试）或者在临时目录里（`upgrade@<ref>` 那份临时 clone
//     编出来的产物，第一次跑 `version` 时那个目录还在）→ 不下结论；
//   - 看不见（权限）→ 不下结论。
func infer(source, spec string) (settings, bool) {
	fallback := settings{Mode: modeRelease}
	if source == "" || underTemp(source) {
		return fallback, false
	}
	if _, err := os.Stat(source); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fallback, true
		}
		return fallback, false
	}
	if checkRepo(source, spec) != nil {
		// 目录在但不是本仓库（被别的东西占了这个路径）：那就不是「自己编的」。
		return fallback, true
	}
	return settings{Mode: modeGit, Repo: source}, true
}

func underTemp(dir string) bool {
	tmp := filepath.Clean(os.TempDir()) + string(filepath.Separator)
	return strings.HasPrefix(filepath.Clean(dir)+string(filepath.Separator), tmp)
}

// checkRepo 核对一个目录能不能当 git 模式的仓库用：是 git 仓库的根、带构建入口、
// 带这份规格书。三条缺一条，`build/build.sh` 就编不出**这个**发行版。
func checkRepo(dir, spec string) error {
	if !filepath.IsAbs(dir) {
		return i18n.E("{dir} is not an absolute path", i18n.A{"dir": dir})
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return i18n.E("{dir} is not the root of a git checkout", i18n.A{"dir": dir})
	}
	if _, err := os.Stat(filepath.Join(dir, "build", "build.sh")); err != nil {
		return i18n.E("{dir} has no build/build.sh (is it a newgate distribution repository?)", i18n.A{"dir": dir})
	}
	if _, err := os.Stat(filepath.Join(dir, spec)); err != nil {
		return i18n.E("{dir} has no {spec} (the specification this binary was built from)",
			i18n.A{"dir": dir, "spec": spec})
	}
	return nil
}
