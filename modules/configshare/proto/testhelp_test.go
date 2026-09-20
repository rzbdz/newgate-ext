package proto

import (
	"encoding/hex"
	"encoding/json"
	paths "github.com/rzbdz/newgate/modules/config/paths"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// sandbox 把 NEWGATE_HOME 指到一个临时目录并建好子目录。
//
// paths.Root() 每次调用都读环境变量，所以 t.Setenv 之后所有路径函数立刻
// 指向沙箱——这也是仓库里其它测试的同一套做法（testkit.Sandbox）。
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("NEWGATE_HOME", home)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	return home
}

// writeLive 往配置目录写一个文件（造"本机现状"）。
func writeLive(t *testing.T, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(paths.Config(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o2770); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(p, data, 0o660); err != nil {
		t.Fatalf("写 %s 失败: %v", rel, err)
	}
}

func readLive(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(paths.Config(), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("读 %s 失败: %v", rel, err)
	}
	return b
}

func existsLive(rel string) bool {
	_, err := os.Stat(filepath.Join(paths.Config(), filepath.FromSlash(rel)))
	return err == nil
}

func discardLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// ---------- 假宿主 ----------

// fakeHost 是一个最小的宿主端点。
//
// 它只做"把一份快照封成信封"这一件事——真正的宿主侧逻辑（从配置目录生成快照、
// 维护 generation、非 host 返回 404）在 commit 2 的 host.go 里，本测试不该
// 依赖它，否则副本侧的测试会因为宿主侧改动而红。
type fakeHost struct {
	srv    *httptest.Server
	encKey []byte
	token  []byte

	mu       sync.Mutex
	host     string
	cfgGen   int
	secGen   int
	files    map[string][]byte
	keys     map[string]string
	cfgMiss  bool // /__newgate/config 回 404（模拟"对端不是宿主"）
	authFail bool // 回 401（模拟根密钥不同）
	// 直接回这些字节，不走 Seal。用来造"被篡改的信封"和"根本解不开的垃圾"——
	// 这两种都要能验，而假宿主自己封印的话造不出来。
	rawConfig  []byte
	rawSecrets []byte
	// 慢响应：给"Stop 打断在飞的请求"用
	block chan struct{}
}

func newFakeHost(t *testing.T, root []byte) *fakeHost {
	t.Helper()
	encKey, token, err := DeriveKeys(root)
	if err != nil {
		t.Fatalf("派生密钥失败: %v", err)
	}
	h := &fakeHost{
		encKey: encKey,
		token:  token,
		host:   "0123456789abcdef0123456789abcdef",
		files:  map[string][]byte{},
		keys:   map[string]string{},
	}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHost) setConfig(gen int, files map[string][]byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfgGen, h.files = gen, files
}

func (h *fakeHost) setSecrets(gen int, keys map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.secGen, h.keys = gen, keys
}

// 下面几个 setter 都带锁：httptest 的 handler 跑在**另一个** goroutine 上，
// 测试里裸读 h.host 会被 -race 抓出来（CI 那条就是带 -race 的）。
func (h *fakeHost) setRawConfig(raw []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rawConfig = raw
}

func (h *fakeHost) setRawSecrets(raw []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rawSecrets = raw
}

func (h *fakeHost) hostID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.host
}

func (h *fakeHost) setHostID(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.host = id
}

func (h *fakeHost) setNotFound(v bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfgMiss = v
}

func (h *fakeHost) serve(w http.ResponseWriter, r *http.Request) {
	if h.block != nil {
		<-h.block
	}
	h.mu.Lock()
	authFail, cfgMiss := h.authFail, h.cfgMiss
	wantAuth := "Bearer " + hex.EncodeToString(h.token)
	host, cfgGen, secGen := h.host, h.cfgGen, h.secGen
	files, keys := h.files, h.keys
	rawConfig, rawSecrets := h.rawConfig, h.rawSecrets
	h.mu.Unlock()

	if authFail || r.Header.Get("Authorization") != wantAuth {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case PathConfig:
		if cfgMiss {
			http.Error(w, "not a host", http.StatusNotFound)
			return
		}
		if rawConfig != nil {
			_, _ = w.Write(rawConfig)
			return
		}
		plain, _ := json.Marshal(Snapshot{HostID: host, Generation: cfgGen, Files: files})
		env, err := Seal(h.encKey, KindConfig, host, cfgGen, plain)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeEnvelope(w, env)
	case PathSecrets:
		if cfgMiss {
			http.Error(w, "not a host", http.StatusNotFound)
			return
		}
		if rawSecrets != nil {
			_, _ = w.Write(rawSecrets)
			return
		}
		plain, _ := json.Marshal(Secrets{HostID: host, Generation: secGen, Keys: keys})
		env, err := Seal(h.encKey, KindSecrets, host, secGen, plain)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeEnvelope(w, env)
	default:
		http.Error(w, "no such path", http.StatusNotFound)
	}
}

func writeEnvelope(w http.ResponseWriter, env *Envelope) {
	raw, _ := json.Marshal(env)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// newTestClient 造一个指向假宿主的客户端。
//
// **必须用 srv.Client()**：它的 Transport 的 Proxy 是 nil，而 http.DefaultClient
// 认 HTTP_PROXY/HTTPS_PROXY——跑这个仓库测试的会话本身往往就穿行在 newgate 的
// 代理里，环境里挂着代理时发给假宿主的请求会先被真网关转一手，断言就全乱了
// （testing/upstream 的注释记的是同一个坑）。
func newTestClient(t *testing.T, h *fakeHost, root []byte) *Client {
	t.Helper()
	c, err := New(Config{
		Endpoint: h.srv.URL,
		RootKey:  root,
		Version:  "build-17-test",
		Log:      discardLogger(),
		HTTP:     h.srv.Client(),
	})
	if err != nil {
		t.Fatalf("建客户端失败: %v", err)
	}
	return c
}

// validProviders 是一份能通过校验的 providers.json（两个上游，密钥都走密钥通道）。
func validProviders() []byte {
	return []byte(`{"providers":{
	  "smt-deepseek":{"base_url":"https://api.deepseek.com","api_key_env":"DS_KEY"},
	  "glm":{"base_url":"https://open.bigmodel.cn/api/paas/v4"}
	}}`)
}

func validProfile() []byte {
	return []byte(`{"name":"prod","roles":{"normal":[{"provider":"smt-deepseek","model":"deepseek-chat"}]}}`)
}

// metaFile 造一份 newgate-config.json。
func metaFile(schema int, minVersion string) []byte {
	raw, _ := json.Marshal(Meta{SchemaVersion: schema, MinNewgateVersion: minVersion})
	return raw
}

// snapshotFiles 是一份常见的快照内容（providers + 一个档位 + meta）。
func snapshotFiles() map[string][]byte {
	return map[string][]byte{
		ProvidersName:        validProviders(),
		MetaFileName:         metaFile(SupportedSchema, ""),
		"mappings/prod.json": validProfile(),
	}
}
