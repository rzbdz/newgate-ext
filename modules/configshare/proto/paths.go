package proto

import (
	"path/filepath"

	paths "github.com/rzbdz/newgate/modules/config/paths"
)

// 这个文件原来是内核的 `modules/config/paths` 里的一段（2026-09-20 跟着配置共享
// 一起搬过来）。搬家的判据很简单：**文件名是产品的知识，目录根是内核的机制**。
// 内核现在只公开 `paths.Root()`（运行时文件的根目录在哪），至于在它下面叫什么、
// 放几个文件，由用到的人自己决定——内核不该为了一个产品功能在自己的 paths 包里
// 维护六个函数。
//
// 另一半是顺序：原来这些帮手与「内核自己的文件」并排躺着，于是「这是谁的文件」
// 读代码时看不出来。现在它们和唯一的使用者住在同一个包里。

// ConfigShareDir 是本模块自己的记账目录。
//
// 与配置目录分开，是因为这里的东西**都不该被 watcher 看见**：轮询的失败计数
// 每 30 秒就可能变一次，放进去就是一个恒定的热更新源。同理它也不在内核的
// EnsureDirs 里——没用这个功能的机器不该多出目录，用到时才建。
func ConfigShareDir() string { return filepath.Join(paths.Root(), "configshare") }

// ConfigShareStateFile 副本侧记账：已应用的 generation、我们写下去的每个文件
// 的 sha256（漂移守卫的基准）、失败计数。
func ConfigShareStateFile() string { return filepath.Join(ConfigShareDir(), "state.json") }

// ConfigShareHostFile 宿主侧记账：权威身份 + 单调 generation。
//
// host_id 与 generation 在**同一个文件**里，因为「换了权威」和「代数重置」必须
// 一起生效：分成两个文件就会有一段两个 rename 之间的撕裂状态，而副本正是靠
// 这两个值共同判断「这份快照该不该应用」。
func ConfigShareHostFile() string { return filepath.Join(ConfigShareDir(), "host.json") }

// ConfigShareLockFile 后台轮询的建议锁。优雅交接时有几百毫秒新旧 daemon 同时
// 在，两个 poller 同时写同一批文件——用 flock 让后到的那个安静跳过这一轮
// （拿不到锁不是错误，见 poller.go）。
func ConfigShareLockFile() string { return filepath.Join(ConfigShareDir(), "lock") }

// ConfigShareRootKeyFile 预共享根密钥（`newgate config trust` 写进来的那 32 字节）。
// 0600：拿到它就等于拿到 AES-GCM 密钥和 bearer token 两个派生值。
func ConfigShareRootKeyFile() string { return filepath.Join(ConfigShareDir(), "root.key") }

// secretsPath 是密钥落盘处：`{"version":1,"keys":{"<provider>":"sk-…"}}`。
//
// 为什么在根目录而不在 configshare/ 里：它是**宿主推过来的密钥的去处**，由
// 配置层在解析 provider 时读取，生命周期与那份配置同级，不属于本模块的记账。
//
// 为什么不放 state.json：state.json 被 watcher 的签名盯着（内核的 watch.go 的
// signature()），每次写都会触发一次全量 reload 加一行日志。密钥跟着轮询周期改，
// 写在那里等于给配置系统装了个节拍器。
//
// 0600 而不是 providers.json 的 0660：providers 里只有绑定关系（组内可见是
// 有意的），明文 key 不是——谁能读它谁就能直接花掉那份订阅。唯一的例外是共享
// 部署里 daemon 以另一个用户跑（见 CLAUDE.md §3.1 的权限坑），那时按需要
// chmod 0660 并保证组边界就是信任边界。
func secretsPath() string { return filepath.Join(paths.Root(), "secrets.json") }
