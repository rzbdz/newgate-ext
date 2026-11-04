package hermes

import (
	"io/ioutil"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rzbdz/newgate/lib/i18n"
)

// 本文件是**模型清单的后台同步**：newgate 这边的 profile 变了（加了一份、改了一个
// 绑定、删了一份），就把 config.yaml 里我们那几行 `providers.newgate*` 改成此刻的样子。
//
// # 为什么自动做、又为什么只做这一件
//
// 「跟着 newgate 走」的那条用法（缺省模型 = 档位名）本来就不需要同步：档位名不变，
// 落到哪个模型是 newgate 在请求那一刻决定的。需要同步的只有第二条用法——`/model`
// 里列出来的那份**具体模型清单**，它是写进文件的快照，profile 一变就过期。过期的
// 症状是「hermes 里能选的模型 newgate 早就没有了」（选了 404）或者「newgate 新加的
// 模型 hermes 里选不到」。这件事用户不该需要记得手动做。
//
// 但同步**只碰那几行**，不碰槽位那几格（model.default、delegation、auxiliary）：
// 那些是接管时写的、之后用户可能在 hermes 里亲手改过（`/model … --global` 就会写
// model.default 与 model.provider）。后台把它们改回去，等于替用户撤销了他刚做的
// 选择。槽位的改写只在用户明确要求时发生（`newgate on hermes`、卡片上的保存）。
//
// # 通知
//
// hermes 的网关按 mtime 重读这份文件，所以同步完下一条消息就能看见新清单，**不用
// 重启**（见 gateway.go 文件头）。同步本身记一笔（state.json 的 hermes 那一格 +
// 日志一行），`newgate status` 与卡片上都看得见。真正要重启的那一种情况由
// gateway.go 判断，同样只提示、不替人重启。

// syncInterval 多久看一次。profile 的变化是人手动做的，十秒的延迟没有人会注意到；
// 而每一次看都要读一遍全部 profile + 这份 YAML（几十 KB），太勤没有意义。
const syncInterval = 10 * time.Second

// startSync 是 serving.OnServe 的 starter：只在守护进程里跑（理由见内核
// docs/09-extension-guide.md §3b2）。
func startSync() (func(), error) {
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(syncInterval)
		defer t.Stop()
		for {
			syncOnce()
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()
	return func() { once.Do(func() { close(stop) }) }, nil
}

// lastSyncErr 同一个错误只记一次日志：后台每十秒跑一次，一个改不动的文件不该刷屏。
var (
	syncMu      sync.Mutex
	lastSyncErr string
)

// syncOnce 跑一次同步。没接管 / 没装 / 没变化都是正常的「什么都不做」。
func syncOnce() {
	target := ConfigPath()
	if !(Takeover{}).IsTakenOver(target) {
		return
	}
	// 只同步**指着我自己**的那份文件。2026-11-04 实测：同一台机器上起了第二个
	// 守护进程（另一个 NEWGATE_HOME、另一个端口），两边每十秒各把 base_url 改成
	// 自己的端口，hermes 的请求在两个代理之间来回跳。文件此刻指着谁，谁才是这份
	// 接管的主人；端口要换，是 `newgate on hermes` 的事（显式、一次）。
	if !pointsAt(target, proxyPort()) {
		return
	}
	res, err := syncCatalog()

	syncMu.Lock()
	defer syncMu.Unlock()
	if err != nil {
		if msg := err.Error(); msg != lastSyncErr {
			lastSyncErr = msg
			log.Printf("[hermes] %s", i18n.T("model list sync skipped: {err}", i18n.A{"err": err}))
		}
		return
	}
	lastSyncErr = ""
	if res == nil || len(res.Changes) == 0 {
		return
	}
	note := strings.Join(res.Changes, ", ")
	log.Printf("[hermes] %s", i18n.T("model list synced into {file}: {changes} (the gateway picks it up on the next message)",
		i18n.A{"file": target, "changes": note}))
	rec := loadRecord()
	rec.SyncedAt = time.Now().Unix()
	rec.SyncNote = note
	_ = saveRecord(rec)
}

// staleCatalog 文件里那份清单此刻是不是过期了（status / 卡片用；只读，不写）。
func staleCatalog() (bool, error) {
	target := ConfigPath()
	if !(Takeover{}).IsTakenOver(target) {
		return false, nil
	}
	in, err := currentInputs(proxyPort())
	if err != nil {
		return false, err
	}
	d, err := readDoc(target)
	if err != nil {
		return false, err
	}
	edits, _ := catalogEdits(d, in)
	for _, e := range edits {
		changed, err := d.put(e)
		if err != nil {
			return false, err
		}
		if changed {
			return true, nil
		}
	}
	return false, nil
}

func readDoc(path string) (*doc, error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseDoc(string(b)), nil
}

// pointsAt 文件里 `providers.newgate` 的 base_url 是不是本机这个端口。
func pointsAt(target string, port int) bool {
	d, err := readDoc(target)
	if err != nil {
		return false
	}
	u, ok := d.get("providers", ProviderID, "base_url")
	return ok && strings.HasPrefix(u, "http://127.0.0.1:"+strconv.Itoa(port)+"/")
}
