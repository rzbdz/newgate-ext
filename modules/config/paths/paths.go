package paths

import (
	"os"
	"path/filepath"
)

// Home 目录下的 pid / lock（按需求：一旦 start 就在 ~ 创建）
func Home() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return "."
	}
	return h
}

// Root 是整套运行时文件的根目录：`$NEWGATE_HOME` > `$XDG_CONFIG_HOME/newgate`
// > `~/.config/newgate`。pid / lock / 日志 / 配置全在它下面。
//
// 它对外公开（2026-09-20 起）是给**发行版**用的：产品要在这里放自己的东西时，
// 该由它自己拼子路径（`filepath.Join(paths.Root(), "configshare")`），而不是让
// 内核替它把每个文件名都写一遍——那些名字是产品的知识，写在内核里就是内核在
// 认识产品。内核自己用到的名字仍然在下面这一串函数里。
func Root() string {
	if v := os.Getenv("NEWGATE_HOME"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "newgate")
	}
	return filepath.Join(Home(), ".config", "newgate")
}

func Config() string         { return Root() }
func Mappings() string       { return filepath.Join(Root(), "mappings") }
func ProvidersFile() string  { return filepath.Join(Root(), "providers.json") }
func StateFile() string      { return filepath.Join(Root(), "state.json") }
func HealthFile() string     { return filepath.Join(Root(), "health.json") }
func ProbeCacheFile() string { return filepath.Join(Root(), "probe-capabilities.json") }
func BackupDir() string      { return filepath.Join(Root(), "backups") }

// ShimDir 是 PATH shim 的目录：`newgate on <agent>` 往这里放一个与客户端同名的
// 链接，接管就是「让 PATH 前面的这个名字指向我们」。
//
// 它住在这里而不是 modules/runtime/injection：**路径布局归本包**，而这份布局有两个
// 消费者——放链接的那一位（injection）与**判断「这个工具到底装没装」的那一位**
// （confighook 的 OnPath：查 PATH 时必须跳过这个目录，否则那条判据永远为真，因为
// 目录里那个同名文件正是我们自己放的）。两个消费者各拼一遍路径，就是两份会漂移的
// 知识，而漂移的症状是「接管状态又开始撒谎」。
func ShimDir() string { return filepath.Join(Config(), "bin") }

// RelToRoot 把一个绝对路径写成**相对配置根**的样子（写不出来就退回文件名）。
//
// 它服务的是界面契约里那个 `File`/`Path` 字段：概念按「说的是哪份文件」两半配对
// （控件半与原文半，见 lib/view 的 Toggles.File），所以**同一份文件在两处必须给出
// 同一种写法**——一处绝对、一处相对，配不上对，而界面上看起来只像「右栏没出来」。
//
// 与 ShimDir 同一条：路径布局归本包。这段逻辑 2026-09-21 之前在 config 与 locale
// 里各有一份（第二份是抄的），而它就是上面那句话说的「会漂移的知识」。
func RelToRoot(path string) string {
	rel, err := filepath.Rel(Root(), path)
	if err != nil {
		return filepath.Base(path)
	}
	return rel
}
func LogFile() string { return filepath.Join(Root(), "newgate.log") }

// ThinkCacheFile thinkcache 的落盘冷层文件：滚动 100MB，撑过 daemon 重启。
// 内容是推理原文（= 对话内容），和 dump 一样是明文，权限 0660 随目录的
// 组共享模型走（developer 组内可见）。
func ThinkCacheFile() string { return filepath.Join(Root(), "thinkcache.bin") }

// pid / lock 放共享配置目录，便于同一组的多用户管理同一个 daemon。
// NEWGATE_HOME 仍可用于测试时隔离整套运行时文件。
func runtimeDir() string {
	return Root()
}

func PidFile() string  { return filepath.Join(runtimeDir(), ".newgate.pid") }
func LockFile() string { return filepath.Join(runtimeDir(), ".newgate.lock") }

// LegacyPidFile / LegacyLockFile 老版本的 pid / lock 位置（$HOME 根下）。
// 只在真实部署（没设 NEWGATE_HOME）时兜底**读**它：升级到共享目录后，
// 正在跑的老 daemon 还把 pid 写在那儿——不兜底的话它就「失踪」了，
// stop 不到、start 又撞端口。
// 沙箱里必须返回空：绝不能让测试里的 stop 顺藤摸瓜摸到真实 daemon。
func LegacyPidFile() string {
	if os.Getenv("NEWGATE_HOME") != "" {
		return ""
	}
	return filepath.Join(Home(), ".newgate.pid")
}

func LegacyLockFile() string {
	if os.Getenv("NEWGATE_HOME") != "" {
		return ""
	}
	return filepath.Join(Home(), ".newgate.lock")
}

// EnsureDirs 2770（组可读写 + setgid）：共享部署模型下，同组的其他用户
// （developer 组里的 claude 用户）也要能读配置、写 pid/lock 管理同一个
// daemon。新文件继承组的 setgid 靠目录位，umask 剥掉的组写用 chmod 补回。
// 已存在的目录不碰——权限是装它的人的事。
func EnsureDirs() error {
	for _, d := range []string{Config(), Mappings(), BackupDir()} {
		if _, err := os.Stat(d); err == nil {
			continue
		}
		if err := os.MkdirAll(d, 0o2770); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o2770); err != nil {
			return err
		}
	}
	return nil
}
