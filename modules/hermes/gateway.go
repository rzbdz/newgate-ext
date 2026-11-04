package hermes

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/store"
)

// 本文件是 hermes 的**消息网关**那一面：它在不在跑、什么时候起的、要不要重启。
//
// # 什么时候需要重启（2026-11-04 读 gateway/run.py 的结论）
//
// 大多数时候**不需要**：网关每一轮都按 mtime 重读 config.yaml（`_load_gateway_config`
// → `read_raw_config`），主循环的 model/provider、`/model` 的列表、delegation、多数
// 辅助任务都是现读的。所以 profile 变了、我们把模型清单同步进文件之后，下一条消息
// 就看得见——不用重启。
//
// 需要重启的只有一处：网关**启动时**把 `auxiliary.{vision,web_extract,approval}` 的
// provider/model/base_url/api_key 抄进了 `AUXILIARY_*` 环境变量（run.py 489-526），
// 而工具那边优先读环境变量。这三格变了（第一次接管一定会变），已经在跑的网关仍然
// 按旧值把看图、网页摘要、审批发到原来的上游——绕过 newgate，而且看不出来。
//
// 所以「要不要重启」不是猜的：每次真的改了那三格，就记下时刻；网关的启动时刻早于它，
// 就提示一次。重启与否由用户决定（2026-11-04 用户：「用户可以选择不重启」）——不重启
// 的代价只是那三类辅助调用暂时还走旧路。

// bridged 是网关启动时抄进环境变量的那几个辅助任务。
var bridged = map[string]bool{"vision": true, "web_extract": true, "approval": true}

// stateKey 是 state.json 的 `module_config` 里本模块记账的那一格。
const stateKey = "hermes"

// record 是那一格的形状。
type record struct {
	// RestartAfter 网关要在这个时刻之后启动过，才看得见我们写的东西（unix 秒，0 = 不需要）。
	RestartAfter int64 `json:"restart_after,omitempty"`
	// SyncedAt 后台最近一次把模型清单同步进文件的时刻（unix 秒）。
	SyncedAt int64 `json:"synced_at,omitempty"`
	// SyncNote 那一次改了什么（给 status / 卡片看）。
	SyncNote string `json:"sync_note,omitempty"`
	// Target 接管时写的是哪份文件。后台同步认它，不认自己算出来的路径：守护进程
	// 的环境不是用户 shell 的环境——用户 `HERMES_HOME=… newgate on hermes` 接管的
	// 那份，守护进程自己算出来的永远是 `~/.hermes`，于是会去同步一份没人在用的文件
	// （或者干脆不在），而接管的那份清单从此再也不更新。
	Target string `json:"target,omitempty"`
}

func loadRecord() record {
	var r record
	if raw, ok := store.LoadState().ModuleConfig[stateKey]; ok {
		_ = json.Unmarshal(raw, &r)
	}
	return r
}

func saveRecord(r record) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	s := store.LoadState()
	if s.ModuleConfig == nil {
		s.ModuleConfig = map[string][]byte{}
	}
	s.ModuleConfig[stateKey] = raw
	return store.SaveState(s)
}

// noteApply 记下这次完整接管：写的是哪份文件（见 record.Target），以及——这次碰了
// 网关启动时才读的那几格的话——「此刻之后要重启过」。
func noteApply(r *result) {
	rec := loadRecord()
	rec.Target = r.File
	for _, p := range r.Paths {
		if len(p) >= 2 && p[0] == "auxiliary" && bridged[p[1]] {
			rec.RestartAfter = time.Now().Unix()
			break
		}
	}
	_ = saveRecord(rec)
}

// gatewayInfo 是网关此刻的样子。
type gatewayInfo struct {
	Running bool
	PID     int
	// Started 启动时刻；零值 = 不知道（不是 Linux、或 pid 文件不认识）。
	Started time.Time
}

// gateway 读 `$HERMES_HOME/gateway.pid`（hermes 自己写的 JSON：pid + /proc 里的
// starttime），确认那个进程还是它（starttime 对得上——pid 会被复用），换算出启动时刻。
func gateway() gatewayInfo {
	raw, err := ioutil.ReadFile(filepath.Join(Home(), "gateway.pid"))
	if err != nil {
		return gatewayInfo{}
	}
	var pf struct {
		PID       int   `json:"pid"`
		StartTime int64 `json:"start_time"`
	}
	if json.Unmarshal(raw, &pf) != nil || pf.PID <= 0 {
		// 旧版只写一个数字。
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || pid <= 0 {
			return gatewayInfo{}
		}
		pf.PID = pid
	}
	if syscall.Kill(pf.PID, 0) != nil {
		return gatewayInfo{}
	}
	g := gatewayInfo{Running: true, PID: pf.PID}
	ticks, ok := procStartTicks(pf.PID)
	if !ok {
		return g
	}
	if pf.StartTime != 0 && ticks != pf.StartTime {
		return gatewayInfo{} // pid 被别的进程复用了：那不是网关
	}
	if boot, ok := bootTime(); ok {
		// CLK_TCK 在 Linux 上实际都是 100（glibc 的 sysconf 也这么报）；Go 拿不到它
		// 而不引 cgo。差一个数量级的风险只会让「要不要重启」那一句不准，不会改任何东西。
		g.Started = boot.Add(time.Duration(ticks) * 10 * time.Millisecond)
	}
	return g
}

// procStartTicks 读 /proc/<pid>/stat 的第 22 列（进程启动时刻，开机后的 tick 数）。
func procStartTicks(pid int) (int64, bool) {
	b, err := ioutil.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	s := string(b)
	// 第 2 列是带括号的进程名，里面可能有空格：从最后一个 ')' 之后再数。
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:])
	// ')' 之后第一列是第 3 列（state），所以第 22 列是 f[19]。
	if len(f) < 20 {
		return 0, false
	}
	n, err := strconv.ParseInt(f[19], 10, 64)
	return n, err == nil
}

func bootTime() (time.Time, bool) {
	b, err := ioutil.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "btime ") {
			n, err := strconv.ParseInt(strings.TrimSpace(ln[6:]), 10, 64)
			if err == nil {
				return time.Unix(n, 0), true
			}
		}
	}
	return time.Time{}, false
}

// restartPending 网关在跑、而且起得比「要重启过」那一刻早。启动时刻不知道时按
// 「需要」报——宁可多提示一次，也不让人以为看图那类调用已经走 newgate 了。
func restartPending() bool {
	rec := loadRecord()
	if rec.RestartAfter == 0 {
		return false
	}
	g := gateway()
	if !g.Running {
		return false
	}
	if g.Started.IsZero() {
		return true
	}
	return g.Started.Unix() < rec.RestartAfter
}

// restartGateway 让 hermes 自己重启它的网关：`hermes gateway restart`。
//
// 为什么借它的命令而不是自己发信号 / 调 systemctl：那条命令知道网关是怎么被托管的
// （systemd 用户单元、launchd、还是前台进程），会先发 SIGUSR1 让正在跑的会话**收尾**
// （drain）再换进程——我们自己 kill 一下，正在回消息的那一轮就断在半路。
//
// detach = 不等它结束（界面按钮用：drain 最长要等两分多钟，按钮不能卡那么久）。
func restartGateway(detach bool) error {
	bin := realHermes()
	if bin == "" {
		return i18n.E("hermes is not installed on this machine", nil)
	}
	cmd := exec.Command(bin, "gateway", "restart")
	// HERMES_HOME 显式带上：守护进程（网页上那颗按钮）的环境里多半没有它，而我们
	// 要重启的是**接管的那一份**的网关（见 Home）。
	cmd.Env = append(os.Environ(), "HERMES_HOME="+Home())
	if detach {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return clearRestart()
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return i18n.Ef(err, "hermes gateway restart failed: {err}", i18n.A{"err": err})
	}
	return clearRestart()
}

func clearRestart() error {
	rec := loadRecord()
	if rec.RestartAfter == 0 {
		return nil
	}
	rec.RestartAfter = 0
	return saveRecord(rec)
}
