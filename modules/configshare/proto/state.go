package proto

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	"github.com/rzbdz/newgate/lib/i18n"
	paths "github.com/rzbdz/newgate/modules/config/paths"
)

// 角色。这是一台机器的**本地事实**，不是共享配置的一部分：同一份共享配置
// 在宿主上是权威、在其余机器上是副本。
const (
	RoleHost   = "host"
	RoleClient = "client"
)

// SettingsKey 是 configshare 在主 state.json 里拥有的顶层键。
//
// 它在 configshare 模块里通过 confighook.RegisterStateField("configshare",
// SettingsKey) 登记（同 classifier_naked 的做法），登记只负责**跨模块键冲突**
// 的提前拒绝；值的读写仍由模块自己做——proto 这里只读，写回在主 state.json 上
// 走 store.SaveState（它负责把各模块的 ModuleConfig 无损合并回去，那件事只能有
// 一个实现）。
const SettingsKey = "config_share"

// 轮询间隔的默认值与钳位范围。钳位是为了挡住手写配置里的 0（会变成忙轮询）
// 和 86400（等于关掉了还留着个 goroutine）。
const (
	DefaultInterval = 30 * time.Second
	MinInterval     = 10 * time.Second
	MaxInterval     = time.Hour
)

// Settings 是这台机器的角色与拉取参数（主 state.json 的 config_share 键）。
type Settings struct {
	Role            string `json:"role,omitempty"`             // host | client
	Endpoint        string `json:"endpoint,omitempty"`         // 可拉取的 endpoint，不是"宿主的 ssh 地址"
	IntervalSeconds int    `json:"interval_seconds,omitempty"` // 0 = DefaultInterval
	AutoPull        *bool  `json:"auto_pull,omitempty"`        // nil = 开
}

// IsClient 这台机器是副本（要拉配置）。
func (s Settings) IsClient() bool { return s.Role == RoleClient }

// IsHost 这台机器是权威（要分发配置）。
func (s Settings) IsHost() bool { return s.Role == RoleHost }

// Configured 角色已经设过了。没设 = 这台机器没在用配置共享，一切相关动作
// （后台轮询、首次自举的门）都该走"没在用"的分支。
func (s Settings) Configured() bool { return s.IsClient() || s.IsHost() }

// Auto 是否自动拉取（没显式关就是开）。
func (s Settings) Auto() bool { return s.AutoPull == nil || *s.AutoPull }

// Interval 生效的轮询间隔，钳在 [MinInterval, MaxInterval]。
func (s Settings) Interval() time.Duration {
	d := time.Duration(s.IntervalSeconds) * time.Second
	if d <= 0 {
		return DefaultInterval
	}
	if d < MinInterval {
		return MinInterval
	}
	if d > MaxInterval {
		return MaxInterval
	}
	return d
}

// LoadSettings 从主 state.json 里读 config_share。
//
// 为什么直接读文件而不走 store.LoadState：proto 不认识 store 之外的任何东西，
// 而这里要的只是一个键的原文。走 store 会把"读一个字段"变成"装配一份完整
// domain.State + 迁移旧字段"，对后台轮询这种要在**任何**状态下都能安全调用的
// 路径没有好处。
func LoadSettings() (Settings, error) {
	var s Settings
	raw, err := ioutil.ReadFile(paths.StateFile())
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil // 还没有 state.json = 没在用
		}
		return s, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return s, i18n.Ef(err, "cannot parse state.json: {err}", nil)
	}
	blob, ok := fields[SettingsKey]
	if !ok {
		return s, nil
	}
	if err := json.Unmarshal(blob, &s); err != nil {
		return s, i18n.Ef(err, "cannot parse {key} in state.json: {err}", i18n.A{"key": SettingsKey})
	}
	return s, nil
}

// State 是副本侧的记账，落在 configshare/state.json。
//
// 为什么不放主 state.json：那份额外的文件被 store/watch.go 的 signature()
// 盯着（它把 state.json 也 stat 进指纹），每 30 秒写一次失败计数就会每 30 秒
// 触发一次全量 reload 加一行日志。轮询的记账是高频的、且与配置内容无关，
// 必须和配置目录分开。
type State struct {
	// HostID 上次应用的权威身份。与快照里的 HostID 不同 = 换了权威，代数
	// 基线重置（否则新宿主的计数从 0 开始时副本会永久拒绝更新，见 types.go）。
	HostID     string    `json:"host_id,omitempty"`
	Generation int       `json:"generation,omitempty"`
	Version    string    `json:"version,omitempty"`
	AppliedAt  time.Time `json:"applied_at,omitempty"`

	// Applied 是"我们上次写下去的每个文件长什么样"：相对路径 → 内容 sha256。
	// **漂移守卫的基准就是它**。为什么不用 mtime：watch.go 自己的注释就写着
	// mtime+size 的盲区（同一秒内的原地改写可能漏检），而这里要判的是"人改了
	// 内容"，必须哈希内容。
	Applied map[string]string `json:"applied,omitempty"`

	// 密钥通道的独立记账。两个通道的代数各走各的：合并成一个的话，每次改
	// provider 都会让副本以为密钥也变了，把"空转完全静默"打破。
	SecretsGen     int       `json:"secrets_generation,omitempty"`
	SecretsApplied string    `json:"secrets_applied,omitempty"` // secrets.json 的 sha256
	SecretsAt      time.Time `json:"secrets_at,omitempty"`

	// 失败记账。给日志抑制（同一错误不刷屏）与 config status 用。
	FailCount     int       `json:"fail_count,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	LastErrorAt   time.Time `json:"last_error_at,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`

	// Drift 非 nil = 配置通道上一轮因为本地有改动而拒绝应用。它是**持续状态**
	// （不是一次性事件）：用户得先解决它，副本才会恢复接受远端配置，期间 status
	// 要一直显示。
	Drift *DriftReport `json:"drift,omitempty"`

	// SecretsDrift 是密钥通道的漂移，**与配置通道分开存**。
	//
	// 为什么不能共用一处：配置通道每轮成功都会重算并重写 Drift，共用一个字段
	// 的话它会把密钥通道记下的那条漂移顺手抹掉——而用户还没处理它。两个通道有
	// 各自的记账、各自的代数、各自的漂移，这是同一件事的第三遍。
	SecretsDrift *DriftReport `json:"secrets_drift,omitempty"`

	// Warnings 是最近一次应用带出来的警告（版本不匹配、没有 schema 声明…）。
	// 只增不减地留到下一次成功应用为止，让 status 能显示。
	Warnings []string `json:"warnings,omitempty"`
}

// DriftReport 把两个通道的漂移合并成一份，给 status 这类展示用。
//
// 显示要合并（用户关心的是"我这台机器上有什么没进共享"），但**存储必须分开**
// ——见 SecretsDrift 的注释。
func (s *State) DriftReport() *DriftReport { return mergeDrift(s.Drift, s.SecretsDrift) }

// PendingDrift 是否有任何通道因为漂移而停摆（status 据此显示成需要处理）。
func (s *State) PendingDrift() bool {
	return s.Drift.Blocking() || s.SecretsDrift.Blocking()
}

// LoadState 读副本记账。文件不存在算正常的初始状态（零值），不是错误。
func LoadState() (State, error) {
	var s State
	raw, err := ioutil.ReadFile(ConfigShareStateFile())
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		// 记账文件坏了不该让同步彻底停摆：当成初始状态重来（代价是丢一次
		// 漂移基准，下一轮 apply 会重新建立）。但要说出来——静默吞掉的话，
		// 用户会看到"漂移守卫忽然不工作了"而没有任何线索。
		return State{}, i18n.Ef(err, "the configshare bookkeeping file is corrupt; starting over from the initial state: {err}", nil)
	}
	return s, nil
}

// SaveState 写副本记账（原子替换）。
func SaveState(s State) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(ConfigShareStateFile(), append(raw, '\n'), 0o660)
}

// writeFileAtomic 是"同目录临时文件 + rename"的原子替换。
//
// 两个细节都是有来历的：
//   - **同目录**：跨文件系统的 rename 会退化成一个非原子的 copy。
//   - **显式 chmod 而不是靠 umask**：CLAUDE.md §3.1 记着这个坑——root 的 CLI
//     写出的文件被 umask 削成 0640 root:developer，之后以 claude 跑的 daemon
//     就写不了（表现为优雅交接 500、thinkcache 落盘 permission denied）。
//     0660 + 目录的 setgid 位才让"同组多用户管同一个 newgate"成立。
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o2770); err != nil {
		return err
	}
	f, err := ioutil.TempFile(dir, "."+filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp) // 提前返回时别留垃圾
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	tmp = "" // rename 成功，别再删
	return nil
}
