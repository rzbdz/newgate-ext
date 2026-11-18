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

// The reference data downloader.
//
// # Why not let the SDK download
//
// The SDK's `Load` gives `bundle.json` only 10 seconds. Enough on most networks, but this
// machine on 2026-10-28: bundle.json is 7 KB yet took 11 seconds - so the SDK timeout fired
// first, `newgate upgrade` always failed, while curl downloaded fine. Reference data is not
// hot path: tens of seconds for a download is normal, and the timeout should follow "how
// big is this file", not "one API call".
//
// So this does three things itself, still touching only the SDK's **public contract**:
//  1. download with a longer timeout sized by file size;
//  2. verify the sha256 the bundle declares (the SDK only uses it as a version, not a check);
//  3. write in the SDK's cache format atomically, then read via `lmfpd.Load` - readable
//     means success, otherwise the old file stays exactly as it was.
//
// The cache format (`u32 LE length + bundle.json + detector bytes`) is the SDK's on-disk
// format, not a Go API; if it changes, `lmfpd.Load` rejects our file, we fall back to the old
// cache and error - never silently feed wrong data. The integration test does a real download.
const (
	referenceDataSchema     = "lmfpd-data-v1"
	referenceDetectorSchema = "shared-detector-v2"
	referenceDetectorFile   = "shared_detector.bin"

	// Timeout of a single HTTP request. The bundle is small but this link can take ten-odd
	// seconds; chunks are a few MB, so give 3 minutes. Total time is still bounded by context.
	bundleTimeout = 60 * time.Second
	chunkTimeout  = 3 * time.Minute
)

// referenceSources defaults to the SDK's two sources; tests point it at httptest. A var, not a
// const, so download logic (timeout, verify, same-version short circuit) runs in offline tests.
var referenceSources = lmfpd.DefaultDataURLs

type referenceBundle struct {
	Schema         string `json:"schema"`
	DetectorSchema string `json:"detector_schema"`
	Files          map[string]struct {
		Parts  []string `json:"parts"`
		SHA256 string   `json:"sha256"`
	} `json:"files"`
}

// referenceClient uses HTTP/1.1.
//
// 2026-10-28: on the same CDN, curl (HTTP/1.1) downloads while Go's HTTP/2 client reports
// `stream ID 1; INTERNAL_ERROR`. Reference data is not a high-frequency request needing
// multiplexing; falling back to HTTP/1.1 buys "this link works".
func referenceClient() *http.Client {
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{}
	}
	clone := tr.Clone()
	clone.ForceAttemptHTTP2 = false
	return &http.Client{Transport: clone}
}

// fetchReference downloads and extracts one reference data bundle.
//
// When knownVersion is non-empty and the bundle declares the same version, it returns
// changed=false: only the "checked at" time moves, no 3.6 MB download (the SDK does the same).
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
		// Same criterion as the SDK: a schema change is rejected, not read by guessing.
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

// writeReferenceCache writes in the SDK's format atomically: temp file, then rename. The old
// cache survives until that rename; a failed download does not destroy an offline ruler.
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
	// CreateTemp gives 0600; reference data matches what the SDK wrote, so another process
	// in the same group (say a CLI run as another user) can read it too.
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
