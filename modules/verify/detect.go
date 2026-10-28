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
// 它同时是「怎么升级参考库」的逃生口：SDK 默认从 lm.ikale.io 拉取并缓存 24 小时，
// 而离线部署只能人工带一份数据过来。名字是机器标记，不翻译；SDK 自己的离线模式
// 就是「Sources 里放一个本地目录」（见它的 README）。
const DataEnv = "NEWGATE_FINGERPRINT_DATA"

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
}

// fingerprintDetector 是真实实现：按需加载参考库，之后复用。
//
// 加载一次大约 4.5 MB 参数（首次还要下载约 3.6 MB 压缩数据），远贵于一次打分，
// 所以进程内只读一次。SDK 自己的 Load 带 24 小时 TTL 的 stale-while-revalidate
// 缓存；这里再压一层进程内单例，避免每次验证都走一遍解析。
type fingerprintDetector struct {
	cache *cacheStore

	mu    sync.Mutex
	det   *lmfpd.Detector
	build string
	// loading 非 nil = 有人在下载/解析参考库。**锁不在加载期间持有**：界面的
	// State() 每几秒会问一次 Loaded()，若那也排在 90 秒的下载后面，整张快照
	// 会跟着卡住（而这一屏的其余部分与服务没有任何关系）。
	loading chan struct{}
	lastErr error
}

func newFingerprintDetector(c *cacheStore) *fingerprintDetector {
	return &fingerprintDetector{cache: c}
}

func (d *fingerprintDetector) Loaded() (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.build, d.det != nil
}

func (d *fingerprintDetector) get(ctx context.Context) (*lmfpd.Detector, string, error) {
	d.mu.Lock()
	if d.det != nil {
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
	ch := make(chan struct{})
	d.loading, d.lastErr = ch, nil
	d.mu.Unlock()

	// 首次加载要下载数据。给它一个上限，但尊重调用方更早的 deadline：用户关掉
	// 页面时这次下载就该停。
	lctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	opts := lmfpd.LoadOptions{
		Cache: d.cache.dataPath(),
		TTL:   24 * time.Hour,
	}
	// 离线部署：有人把参考数据目录带上来了，就只读它，不碰网络也不写缓存
	//（SDK 对「单个非 http 源」的定义就是本地目录）。
	if src := strings.TrimSpace(os.Getenv(DataEnv)); src != "" {
		opts.Sources = []string{src}
	}
	det, err := lmfpd.Load(lctx, opts)
	if err != nil {
		// 失败不记成永久状态：断网的人把网接上之后，下一次 verify 应该能成功。
		// 但这一轮**等着同一份加载**的调用者要一起拿到这个错误，不然它们会
		// 一个接一个再下一遍 3.6 MB。
		err = i18n.Ef(err, "cannot load the fingerprint reference data: {err}",
			i18n.A{"err": err})
		d.mu.Lock()
		d.lastErr = err
		d.loading = nil
		close(ch)
		d.mu.Unlock()
		return nil, "", err
	}
	build := det.BuiltAt()
	d.mu.Lock()
	d.det, d.build = det, build
	d.loading = nil
	close(ch)
	d.mu.Unlock()
	return det, build, nil
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
