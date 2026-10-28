package verify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// 一个最小的参考数据服务：bundle.json 指向一个 zstd chunk。测试只关心下载器
// 的形状（同版短路、校验和、解压），不关心 detector 本身——那份数 MB 的文件
// 不该进测试仓库。
func referenceServer(t *testing.T, detector []byte) (*httptest.Server, string, *atomic.Int32) {
	t.Helper()
	var compressed bytes.Buffer
	zw, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(detector); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(detector)
	version := hex.EncodeToString(sum[:])
	bundle := fmt.Sprintf(`{"schema":%q,"detector_schema":%q,"files":{%q:{"parts":[%q],"sha256":%q}}}`,
		referenceDataSchema, referenceDetectorSchema, referenceDetectorFile, "chunk.0.zst", version)

	var chunks atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/bundle.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(bundle))
	})
	mux.HandleFunc("/chunks/chunk.0.zst", func(w http.ResponseWriter, r *http.Request) {
		chunks.Add(1)
		_, _ = w.Write(compressed.Bytes())
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, version, &chunks
}

func TestFetchFromDownloadsAndVerifies(t *testing.T) {
	detector := []byte("detector bytes for the test")
	srv, version, chunks := referenceServer(t, detector)

	bundleBytes, detectorBytes, gotVersion, changed, err := fetchFrom(context.Background(), srv.Client(), srv.URL+"/", "")
	if err != nil {
		t.Fatalf("fetchFrom: %v", err)
	}
	if !changed || gotVersion != version {
		t.Fatalf("changed=%v version=%q, want true/%q", changed, gotVersion, version)
	}
	if len(bundleBytes) == 0 || !bytes.Equal(detectorBytes, detector) {
		t.Fatalf("bundle=%d detector=%q", len(bundleBytes), detectorBytes)
	}
	if got := chunks.Load(); got != 1 {
		t.Fatalf("chunk downloads=%d, want 1", got)
	}

	// 同一个版本再问一次：只读 bundle，不重复下 chunk。
	_, detectorBytes, gotVersion, changed, err = fetchFrom(context.Background(), srv.Client(), srv.URL+"/", version)
	if err != nil {
		t.Fatalf("fetchFrom same version: %v", err)
	}
	if changed || detectorBytes != nil || gotVersion != version {
		t.Fatalf("same version: changed=%v detector=%d version=%q", changed, len(detectorBytes), gotVersion)
	}
	if got := chunks.Load(); got != 1 {
		t.Fatalf("chunk downloads=%d after same-version check, want 1", got)
	}
}

func TestFetchFromRejectsBadChecksum(t *testing.T) {
	var compressed bytes.Buffer
	zw, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write([]byte("tampered")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/bundle.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema":"lmfpd-data-v1","detector_schema":"shared-detector-v2","files":{"shared_detector.bin":{"parts":["c"],"sha256":"deadbeef"}}}`))
	})
	mux.HandleFunc("/chunks/c", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(compressed.Bytes())
	})
	bad := httptest.NewServer(mux)
	defer bad.Close()

	if _, _, _, _, err := fetchFrom(context.Background(), bad.Client(), bad.URL+"/", ""); err == nil {
		t.Fatal("fetchFrom accepted a checksum mismatch")
	}
}

func TestFetchReferenceTriesEverySource(t *testing.T) {
	detector := []byte("detector bytes")
	srv, _, _ := referenceServer(t, detector)
	old := referenceSources
	referenceSources = []string{"http://127.0.0.1:1/", srv.URL + "/"}
	t.Cleanup(func() { referenceSources = old })

	bundle, got, version, changed, err := fetchReference(context.Background(), "")
	if err != nil {
		t.Fatalf("fetchReference: %v", err)
	}
	if !changed || len(bundle) == 0 || !bytes.Equal(got, detector) || version == "" {
		t.Fatalf("bundle=%d detector=%q version=%q changed=%v", len(bundle), got, version, changed)
	}
}

func TestWriteReferenceCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := &cacheStore{path: filepath.Join(dir, "cache.json")}
	bundle := []byte(`{"schema":"lmfpd-data-v1","detector_schema":"shared-detector-v2","files":{"shared_detector.bin":{"parts":["c"],"sha256":"abc"}}}`)
	detector := []byte("detector bytes")
	if err := writeReferenceCache(c.dataPath(), bundle, detector); err != nil {
		t.Fatalf("writeReferenceCache: %v", err)
	}
	if got := c.dataVersion(); got != "abc" {
		t.Fatalf("dataVersion=%q, want abc", got)
	}
	info, err := os.Stat(c.dataPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode=%o, want 644", info.Mode().Perm())
	}
	if !c.dataReady() {
		t.Fatal("dataReady=false after write")
	}
}
