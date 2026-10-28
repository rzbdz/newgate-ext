package verify

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	lmfpd "github.com/Ikaleio/lm-detector/sdk/go"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
)

// DataEnv 指向一份**本地**参考数据目录（`unified_bank.json` + `shared_detector.bin`），
// 用于没有外网、也不想在运行时下载的机器。
//
// 它同时是「怎么升级参考库」的逃生口：SDK 默认从 lm.ikale.io 拉取并缓存，而离线
// 部署只能人工带一份数据过来。名字是机器标记，不翻译；SDK 自己的离线模式就是
// 「Sources 里放一个本地目录」（见它的 README）。
const DataEnv = "NEWGATE_FINGERPRINT_DATA"

// DataTTLEnv 是参考库的**网络检查**间隔（`time.ParseDuration` 的写法，如 `2h`）。
//
// 参考库不是推送式的：没有人在你机器上开一条长连接等新模型。策略是
// stale-while-revalidate——有缓存就先用缓存，超过 TTL 才在后台问一次
// `bundle.json`，只有 detector 的 sha 变了才下 3.6 MB。默认 2 小时；想要更新得
// 更勤（或更省），改这个环境变量。
const DataTTLEnv = "NEWGATE_FINGERPRINT_TTL"

const (
	defaultDataTTL = 2 * time.Hour
	// 自动刷新失败后的冷却：断网时不该每分钟都去撞一次墙。手动 `newgate
	// upgrade` 不受它约束。
	refreshBackoff = time.Hour
	// 进程内重新走一遍 Load 的间隔。它必须比网络 TTL 小一个量级：长命 daemon
	// 如果只在启动时 Load 一次，参考库会永远停在启动时那一版。每分钟重走一次
	// Load 的代价是读一次缓存文件，换来「TTL 到了之后，下一次验证就能拿到新
	// 数据」。
	memoryTTL = time.Minute
	// SDK 自己的后台刷新交给本模块做：它的 bundle 超时只有 10 秒（见
	// reference.go），而我们有更合适的超时。给一个足够大的值，让 Load 只读
	// 缓存，不另起一个会失败的 goroutine。
	sdkRefreshDisabled = 10 * 365 * 24 * time.Hour
)

// detector 是本模块与指纹库之间唯一的缝。
//
// 为什么是一个接口而不是直接调 SDK：web/CLI 的行为（匹配、缓存、报错）要能
// 在没有网络、没有参考库的测试里跑一遍。真实实现只有下面这一个，测试给一个
// 固定回答的假货——这不是「为了扩展」，是为了**测得到**（与内核探针拿
// testing/upstream 起假上游同一条）。
type detector interface {
	// Detect 跑一轮指纹。返回结果、参考库版本、实际使用的 API。
	Detect(ctx context.Context, p domain.Provider, model, api string, count int) (*lmfpd.DetectResult, string, string, error)
	// Loaded 报告参考库有没有在这个进程里读进来，以及它的 built_at。
	Loaded() (build string, ok bool)
	// Refresh 强制重新下载参考库（`newgate upgrade` 的动作）。返回一句给人看的结果。
	Refresh(ctx context.Context) (string, error)
}

// fingerprintDetector 是真实实现：按需加载参考库，之后复用。
//
// # 为什么内存里还要再管一层 TTL
//
// SDK 的 Load 在**被调用时**才检查缓存文件的 mtime：超过 TTL 就起一个后台
// goroutine 拉新数据，本次仍然返回旧的那份。但那个后台 goroutine 用的是 SDK
// 自己的 10 秒 bundle 超时，在慢链路上必然失败；而且如果进程只 Load 一次
// （第一版就是），它根本没机会跑。所以这里自己管：内存里只缓存 memoryTTL，
// 之后重新 Load；缓存过期时由本模块用**更合适的超时**在后台刷新。
type fingerprintDetector struct {
	cache *cacheStore

	mu       sync.Mutex
	det      *lmfpd.Detector
	build    string
	loadedAt time.Time
	// loading 非 nil = 有人在下载/解析参考库。**锁不在加载期间持有**：界面的
	// State() 每几秒会问一次 Loaded()，若那也排在下载后面，整张快照会跟着卡住
	// （而这一屏的其余部分与服务没有任何关系）。
	loading chan struct{}
	// refreshing 是「后台自动刷新」的单飞标记，与 loading 分开：手动 upgrade
	// 和一次 verify 触发的加载不该被它挡住。
	refreshing  bool
	lastAttempt time.Time
	lastErr     error
}

func newFingerprintDetector(c *cacheStore) *fingerprintDetector {
	return &fingerprintDetector{cache: c}
}

func (d *fingerprintDetector) Loaded() (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.build, d.det != nil
}

// dataTTL 是配置的网络检查间隔。非法值回落默认值——这个环境变量是运维旋钮，
// 写错了不该把验证功能整个关掉。
func dataTTL() time.Duration {
	if raw := strings.TrimSpace(os.Getenv(DataTTLEnv)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return defaultDataTTL
}

// loadOptions 是每次 Load 共用的选项。
func (d *fingerprintDetector) loadOptions() lmfpd.LoadOptions {
	opts := lmfpd.LoadOptions{Cache: d.cache.dataPath(), TTL: sdkRefreshDisabled}
	// 离线部署：有人把参考数据目录带上来了，就只读它，不碰网络也不写缓存
	//（SDK 对「单个非 http 源」的定义就是本地目录）。
	if src := strings.TrimSpace(os.Getenv(DataEnv)); src != "" {
		opts.Sources = []string{src}
	}
	return opts
}

func (d *fingerprintDetector) get(ctx context.Context) (*lmfpd.Detector, string, error) {
	d.mu.Lock()
	if d.det != nil && time.Since(d.loadedAt) < memoryTTL {
		det, build := d.det, d.build
		d.mu.Unlock()
		return det, build, nil
	}
	if ch := d.loading; ch != nil {
		// 已经有人在加载：等它，而不是再下 3.6 MB。等待也尊重调用方的 deadline。
		d.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
		d.mu.Lock()
		det, build, err := d.det, d.build, d.lastErr
		d.mu.Unlock()
		if det != nil {
			return det, build, nil
		}
		if err != nil {
			return nil, "", err
		}
		return nil, "", i18n.E("the fingerprint reference data could not be loaded", nil)
	}
	oldDet, oldBuild := d.det, d.build
	ch := make(chan struct{})
	d.loading, d.lastErr = ch, nil
	d.mu.Unlock()

	// 首次加载要下载数据。给它一个上限，但尊重调用方更早的 deadline：用户关掉
	// 页面时这次下载就该停。
	lctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var det *lmfpd.Detector
	var build string
	var err error
	switch {
	case strings.TrimSpace(os.Getenv(DataEnv)) != "":
		// 本地目录：不碰网络。
		det, err = lmfpd.Load(lctx, d.loadOptions())
		if err == nil {
			build = det.BuiltAt()
		}
	case !d.cache.dataReady():
		// 没有缓存 = 第一次运行：用本模块的下载器（超时按文件大小给）。
		det, build, _, err = d.loadFresh(lctx, "")
	default:
		det, err = lmfpd.Load(lctx, d.loadOptions())
		if err == nil {
			build = det.BuiltAt()
		} else if ctx.Err() == nil {
			// 缓存读不进来（损坏、格式变了）：整份重下一次，而不是让验证功能
			// 永远卡在一个坏文件上。knownVersion 传空 = 即使远端同版也重写。
			det, build, _, err = d.loadFresh(lctx, "")
		}
	}

	d.mu.Lock()
	d.loading = nil
	if err != nil {
		// 失败不记成永久状态：断网的人把网接上之后，下一次 verify 应该能成功。
		// 但这一轮**等着同一份加载**的调用者要一起拿到这个错误，不然它们会
		// 一个接一个再下一遍。手里已经有旧参考库时继续用它（fail-open）
		// ——「尺子过期」比「验证功能整个不可用」好。
		err = i18n.Ef(err, "cannot load the fingerprint reference data: {err}",
			i18n.A{"err": err})
		d.lastErr = err
		close(ch)
		if oldDet != nil {
			d.det, d.build = oldDet, oldBuild
			d.mu.Unlock()
			return oldDet, oldBuild, nil
		}
		d.mu.Unlock()
		return nil, "", err
	}
	d.det, d.build, d.loadedAt = det, build, time.Now()
	d.lastErr = nil
	close(ch)
	d.mu.Unlock()
	d.maybeRefresh()
	return det, build, nil
}

// loadFresh 自己下载、校验、原子落盘，再由 SDK 读一遍确认格式没变。
//
// knownVersion 是当前缓存里的 detector sha；远端同版时只 touch 缓存，不重复
// 下 3.6 MB。传空则强制重下。
func (d *fingerprintDetector) loadFresh(ctx context.Context, knownVersion string) (*lmfpd.Detector, string, bool, error) {
	bundleBytes, detectorBytes, _, changed, err := fetchReference(ctx, knownVersion)
	if err != nil {
		return nil, "", false, err
	}
	if !changed {
		d.cache.touchData()
	} else if err := writeReferenceCache(d.cache.dataPath(), bundleBytes, detectorBytes); err != nil {
		return nil, "", false, err
	}
	det, err := lmfpd.Load(ctx, lmfpd.LoadOptions{
		Cache: d.cache.dataPath(),
		TTL:   sdkRefreshDisabled,
	})
	if err != nil {
		return nil, "", false, i18n.Ef(err, "read the refreshed fingerprint reference data: {err}",
			i18n.A{"err": err})
	}
	return det, det.BuiltAt(), changed, nil
}

// Refresh 是 `newgate upgrade` 调的那一下：**强制**检查并下载，而不是等 TTL。
//
// 下载先落在临时文件里，只有 SDK 能读进来才 rename 覆盖旧缓存；失败时旧文件
// 一个字节都没动——升级失败不该顺手把一台离线机器仅有的尺子也弄没。本地数据
// 目录（DataEnv）没有「下载」这件事，返回一句说明即可。
func (d *fingerprintDetector) Refresh(ctx context.Context) (string, error) {
	if src := strings.TrimSpace(os.Getenv(DataEnv)); src != "" {
		return i18n.T("reference data is read from the local directory {dir}; nothing to refresh",
			i18n.A{"dir": src}), nil
	}

	d.mu.Lock()
	// 等正在进行的加载结束，再独占「加载中」这个位子。
	for d.loading != nil {
		ch := d.loading
		d.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		d.mu.Lock()
	}
	ch := make(chan struct{})
	d.loading = ch
	d.lastAttempt = time.Now()
	oldDet, oldBuild := d.det, d.build
	d.mu.Unlock()

	// 下载比日常 Load 慢：3.6 MB 压缩数据。给 5 分钟，仍然尊重调用方更早的 deadline。
	lctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	det, build, changed, err := d.loadFresh(lctx, d.cache.dataVersion())

	d.mu.Lock()
	defer d.mu.Unlock()
	d.loading = nil
	if err != nil {
		d.det, d.build = oldDet, oldBuild
		d.lastErr = err
		close(ch)
		return "", i18n.Ef(err, "refresh the fingerprint reference data: {err}",
			i18n.A{"err": err})
	}
	d.det, d.build, d.loadedAt = det, build, time.Now()
	d.lastErr = nil
	close(ch)
	if !changed {
		return i18n.T("fingerprint reference data is already up to date ({build})",
			i18n.A{"build": build}), nil
	}
	return i18n.T("fingerprint reference data is now {build}", i18n.A{"build": build}), nil
}

// maybeRefresh 在缓存过期时起一次后台刷新（stale-while-revalidate）。
//
// 它是自动更新与手动 upgrade 之间的分工：手动 upgrade 是阻塞的、会报错；这里
// 只负责「别让 daemon 里的尺子停在两天前」，失败就退回旧数据，下一次 verify
// 照常工作。
func (d *fingerprintDetector) maybeRefresh() {
	if strings.TrimSpace(os.Getenv(DataEnv)) != "" {
		return
	}
	at, ok := d.cache.dataCheckedAt()
	if !ok || time.Since(at) < dataTTL() {
		return
	}
	d.mu.Lock()
	if d.refreshing || d.loading != nil || time.Since(d.lastAttempt) < refreshBackoff {
		d.mu.Unlock()
		return
	}
	d.refreshing = true
	d.lastAttempt = time.Now()
	d.mu.Unlock()

	go func() {
		defer func() {
			d.mu.Lock()
			d.refreshing = false
			d.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		// 失败是可接受的：旧数据继续用，冷却期之后再试。
		_, _ = d.Refresh(ctx)
	}()
}

func (d *fingerprintDetector) Detect(ctx context.Context, p domain.Provider, model, api string, count int) (*lmfpd.DetectResult, string, string, error) {
	base, sdkAPI, apiName, err := endpointFor(p, api)
	if err != nil {
		return nil, "", "", err
	}
	det, build, err := d.get(ctx)
	if err != nil {
		return nil, "", apiName, err
	}
	if count <= 0 {
		count = 3
	}
	if count > 3 {
		count = 3
	}
	res, err := det.Detect(ctx, lmfpd.DetectConfig{
		BaseURL: base,
		APIKey:  p.Key(),
		Model:   model,
		API:     sdkAPI,
		// Count/Parallel 都是 1–3；一次指纹不需要把上游打到并发 3 以上。
		Count:    count,
		Parallel: count,
		// 指纹靠的是完整输出（默认要 292–332 个整数）。给足时间；这个上限只
		// 约束「首字节/整份 JSON」，不是整轮。
		Timeout: 120 * time.Second,
	})
	if err != nil {
		return nil, build, apiName, i18n.Ef(err, "fingerprint request failed: {err}",
			i18n.A{"err": err})
	}
	return res, build, apiName, nil
}

// endpointFor 把 newgate 的 provider 定义翻译成 SDK 的 base_url + api。
//
// # 两种方言的 base 不是同一个
//
// provider.URL 已经把「anthropic 方言走 anthropic_url、并按 ANTHROPIC_BASE_URL
// 的惯例补 /v1」这件事说清楚了（方舟的现场，见 domain.Provider.Base 的注释）。
// SDK 收到带路径的 base 时只会在**路径后面**追加 /messages，不会再补 /v1。
// 所以这里去掉结尾的 /messages，把已经补好 /v1 的那一段交给它。
//
// auto 的判据是 provider 声明的 protocol，不是「猜哪家支持什么」：协议是配置里
// 写下来的事实；猜错只会得到一个和真实流量无关的结论。
func endpointFor(p domain.Provider, api string) (string, lmfpd.API, string, error) {
	if strings.TrimSpace(p.Key()) == "" {
		return "", "", "", i18n.E("this provider has no api key (set api_key or api_key_env): {base}",
			i18n.A{"base": p.BaseURL})
	}
	switch api {
	case "", "auto":
		if p.Protocol == "anthropic" {
			api = "messages"
		} else {
			api = "chat_completions"
		}
	}
	switch api {
	case "messages":
		base := strings.TrimSuffix(p.URL("/messages"), "/messages")
		if strings.TrimSpace(base) == "" {
			return "", "", "", i18n.E("provider has no base_url for the anthropic dialect", nil)
		}
		return base, lmfpd.APIMessages, api, nil
	case "responses":
		if strings.TrimSpace(p.BaseURL) == "" {
			return "", "", "", i18n.E("provider has no base_url", nil)
		}
		return strings.TrimRight(p.BaseURL, "/"), lmfpd.APIResponses, api, nil
	case "chat_completions":
		if strings.TrimSpace(p.BaseURL) == "" {
			return "", "", "", i18n.E("provider has no base_url", nil)
		}
		return strings.TrimRight(p.BaseURL, "/"), lmfpd.APIChatCompletions, api, nil
	default:
		return "", "", "", i18n.E("unknown fingerprint api {api} (use auto, responses, chat_completions or messages)",
			i18n.A{"api": api})
	}
}
