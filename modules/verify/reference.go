package verify

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	lmfpd "github.com/Ikaleio/lm-detector/sdk/go"
	"github.com/klauspost/compress/zstd"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// 参考数据的下载器。
//
// # 为什么不直接让 SDK 下载
//
// SDK 的 `Load` 对 `bundle.json` 只给 10 秒。这在大多数网络上够用，但
// 2026-10-28 这台机器实测：bundle.json 只有 7 KB，却要 11 秒才回来——于是
// SDK 的超时先到，`newgate upgrade` 永远失败，而 curl 明明能下。参考数据不是
// 热路径：一次下载几十秒是正常的，超时应该按「这份文件多大」给，不该按
// 「一次 API 调用」给。
//
// 所以这里自己做三件事，仍然只碰 SDK 的**公开契约**：
//  1. 用更长的、按文件大小给的超时下载；
//  2. 校验 bundle 声明的 sha256（SDK 只拿它当版本号，不校验内容）；
//  3. 按 SDK 的缓存格式原子写入，再由 `lmfpd.Load` 读一遍——读得进来才
//     算成功，读不进来就把旧文件原样留着。
//
// 缓存格式（`u32 LE 长度 + bundle.json + detector 字节`）是 SDK 的落盘格式，
// 不是 Go API；它变了的话 `lmfpd.Load` 会拒绝我们写的文件，我们回落到旧缓存
// 并报错，不会静默喂错数据。集成测试会走一遍真实下载。
const (
	referenceDataSchema     = "lmfpd-data-v1"
	referenceDetectorSchema = "shared-detector-v2"
	referenceDetectorFile   = "shared_detector.bin"

	// 单次 HTTP 的超时。bundle 很小，但这条链路的延迟可以到十几秒；chunk
	// 是几 MB，按 3 分钟给。总时长仍由调用方的 context 约束。
	bundleTimeout = 60 * time.Second
	chunkTimeout  = 3 * time.Minute
)

// referenceSources 默认就是 SDK 的两个源；测试把它指到 httptest。它是变量而不是
// 常量，正是为了让下载逻辑（超时、校验、同版短路）能在离线测试里跑一遍。
var referenceSources = lmfpd.DefaultDataURLs

type referenceBundle struct {
	Schema         string `json:"schema"`
	DetectorSchema string `json:"detector_schema"`
	Files          map[string]struct {
		Parts  []string `json:"parts"`
		SHA256 string   `json:"sha256"`
	} `json:"files"`
}

// referenceClient 走 HTTP/1.1。
//
// 2026-10-28 实测：同样的 CDN，curl（HTTP/1.1）能下，Go 的 HTTP/2 客户端报
// `stream ID 1; INTERNAL_ERROR`。参考数据不是需要多路复用的高频请求，退回
// HTTP/1.1 换来的是「这条链路能用」。
func referenceClient() *http.Client {
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{}
	}
	clone := tr.Clone()
	clone.ForceAttemptHTTP2 = false
	return &http.Client{Transport: clone}
}

// fetchReference 下载并解出一份参考数据。
//
// knownVersion 非空且 bundle 声明的是同一版时返回 changed=false：只更新
// 「检查时间」，不重复下 3.6 MB（SDK 也是这么做的）。
func fetchReference(ctx context.Context, knownVersion string) (bundleBytes, detectorBytes []byte, version string, changed bool, err error) {
	client := referenceClient()
	failures := make([]string, 0, len(referenceSources))
	for _, source := range referenceSources {
		if ctx.Err() != nil {
			break
		}
		bundleBytes, detectorBytes, version, changed, err = fetchFrom(ctx, client, source, knownVersion)
		if err == nil {
			return bundleBytes, detectorBytes, version, changed, nil
		}
		failures = append(failures, source+" ("+err.Error()+")")
	}
	if len(failures) == 0 && ctx.Err() != nil {
		failures = append(failures, ctx.Err().Error())
	}
	return nil, nil, "", false, i18n.E("cannot download the fingerprint reference data from {sources}",
		i18n.A{"sources": strings.Join(failures, ", ")})
}

func fetchFrom(ctx context.Context, client *http.Client, source, knownVersion string) (bundleBytes, detectorBytes []byte, version string, changed bool, err error) {
	bundleBytes, err = httpGet(ctx, client, strings.TrimSuffix(source, "/")+"/bundle.json", bundleTimeout)
	if err != nil {
		return nil, nil, "", false, err
	}
	var b referenceBundle
	if err := json.Unmarshal(bundleBytes, &b); err != nil {
		return nil, nil, "", false, err
	}
	if b.Schema != referenceDataSchema || b.DetectorSchema != referenceDetectorSchema {
		// 与 SDK 的判据同一条：schema 变了就拒绝，而不是猜着读。
		return nil, nil, "", false, i18n.E("reference data schema {schema}/{detector_schema} is not supported",
			i18n.A{"schema": b.Schema, "detector_schema": b.DetectorSchema})
	}
	file, ok := b.Files[referenceDetectorFile]
	if !ok {
		return nil, nil, "", false, i18n.E("reference data does not list {file}",
			i18n.A{"file": referenceDetectorFile})
	}
	if knownVersion != "" && file.SHA256 == knownVersion {
		return bundleBytes, nil, knownVersion, false, nil
	}

	var compressed []byte
	for _, part := range file.Parts {
		chunk, err := httpGet(ctx, client, strings.TrimSuffix(source, "/")+"/chunks/"+part, chunkTimeout)
		if err != nil {
			return nil, nil, "", false, err
		}
		compressed = append(compressed, chunk...)
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, nil, "", false, err
	}
	detectorBytes, err = decoder.DecodeAll(compressed, nil)
	decoder.Close()
	if err != nil {
		return nil, nil, "", false, err
	}
	if file.SHA256 != "" {
		sum := sha256.Sum256(detectorBytes)
		if got := hex.EncodeToString(sum[:]); got != file.SHA256 {
			return nil, nil, "", false, i18n.E("reference data checksum is {got}, bundle says {want}",
				i18n.A{"got": got, "want": file.SHA256})
		}
	}
	return bundleBytes, detectorBytes, file.SHA256, true, nil
}

func httpGet(ctx context.Context, client *http.Client, url string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, i18n.E("the fingerprint reference data server answered HTTP {status}",
			i18n.A{"status": resp.StatusCode})
	}
	return io.ReadAll(resp.Body)
}

// writeReferenceCache 按 SDK 的落盘格式原子写入：先写临时文件，再 rename。
// 旧缓存直到 rename 那一刻都还在；下载失败不会把一台离线机器仅有的尺子弄没。
func writeReferenceCache(path string, bundleBytes, detectorBytes []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o2770); err != nil {
		return i18n.Ef(err, "create {dir}: {err}", i18n.A{"dir": dir, "err": err})
	}
	tmp, err := os.CreateTemp(dir, ".data-*.tmp")
	if err != nil {
		return i18n.Ef(err, "create a temporary file in {dir}: {err}", i18n.A{"dir": dir, "err": err})
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	// CreateTemp 给的是 0600；参考数据与 SDK 原来写的一样，让同组的另一个
	// 进程（例如以另一个用户跑的 CLI）也能读。
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	content := make([]byte, 4, 4+len(bundleBytes)+len(detectorBytes))
	binary.LittleEndian.PutUint32(content, uint32(len(bundleBytes)))
	content = append(append(content, bundleBytes...), detectorBytes...)
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return i18n.Ef(err, "write {path}: {err}", i18n.A{"path": tmpName, "err": err})
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return i18n.Ef(err, "replace {path}: {err}", i18n.A{"path": path, "err": err})
	}
	return nil
}
