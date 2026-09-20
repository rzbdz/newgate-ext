package proto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/lib/i18n"
	paths "github.com/rzbdz/newgate/modules/config/paths"
)

func rootKey(seed byte) []byte { return bytes.Repeat([]byte{seed}, RootKeyLen) }

func mustPull(t *testing.T, c *Client, force bool) Outcome {
	t.Helper()
	out, err := c.Pull(context.Background(), force)
	if err != nil {
		t.Fatalf("Pull 报错: %v", err)
	}
	return out
}

func anyContains(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// backupCount 数 backups/configshare-* 的个数（归档只该在 --force 时出现）。
func backupCount(t *testing.T) int {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(paths.BackupDir(), "configshare-*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(m)
}

func TestPullAppliesSnapshot(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	h.setConfig(1, snapshotFiles())
	h.setSecrets(1, map[string]string{"smt-deepseek": "sk-1"})
	c := newTestClient(t, h, root)

	out := mustPull(t, c, false)
	if out.Failed {
		t.Fatalf("首轮不该失败: %s", out.ErrText)
	}
	if !out.Config.Changed || len(out.Config.Written) != 3 {
		t.Fatalf("首轮该落盘 3 个文件，实际 changed=%v written=%v", out.Config.Changed, out.Config.Written)
	}
	for rel := range snapshotFiles() {
		if !existsLive(rel) {
			t.Fatalf("%s 没落盘", rel)
		}
	}
	if !out.Secrets.Changed {
		t.Fatal("密钥首轮该落盘")
	}
	if v, ok := LookupSecret(secretsPath(), "smt-deepseek"); !ok || v != "sk-1" {
		t.Fatalf("密钥没落盘: %q %v", v, ok)
	}

	st, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Generation != 1 || st.HostID != h.hostID() {
		t.Fatalf("记账不对: gen=%d host=%q", st.Generation, st.HostID)
	}
	if st.SecretsGen != 1 {
		t.Fatalf("密钥代数没记: %d", st.SecretsGen)
	}
	if len(st.Applied) != 3 {
		t.Fatalf("applied 清单该有 3 条（漂移守卫的基准），实际 %v", st.Applied)
	}
	if st.FailCount != 0 || st.LastError != "" {
		t.Fatalf("成功的一轮不该留失败记账: %+v", st)
	}
}

// TestPullFastPathDoesNotDecrypt 把"空转完全静默"变成可测的东西。
//
// 手法：第二轮把端点换成一封**根本解不开**的信封，但代数与权威保持不变。
// 快路径（代数没变 → 连解密都不做）应当因此完全无感。如果我们偷偷解密了，
// 这里就会报 ErrTampered——所以这个测试同时锁住了"静默"和"代数比较在解密之前"。
func TestPullFastPathDoesNotDecrypt(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	h.setConfig(2, snapshotFiles())
	c := newTestClient(t, h, root)
	mustPull(t, c, false)

	garbage, _ := json.Marshal(Envelope{
		V: ProtocolVersion, Kind: KindConfig, Host: h.hostID(), Gen: 2,
		Nonce: make([]byte, 12), CT: []byte("这不是密文"),
	})
	h.setRawConfig(garbage)

	out := mustPull(t, c, false)
	if out.Config.Err != nil || out.Failed {
		t.Fatalf("代数没变时不该解密，也就不该出错: %v / %s", out.Config.Err, out.ErrText)
	}
	if out.Config.Changed {
		t.Fatal("代数没变就不该写盘")
	}
}

// TestPullRejectsOlderGeneration 是单调性守卫：迟到的、乱序的、被中间人重放的
// 旧快照不能把车队回滚。
//
// 代价（有意接受）是宿主侧的**故意回滚**不会传播——要回滚得在宿主上重新
// publish 一个更高的代数。一个能悄悄让整个车队降级的通道，比一个"回滚要重新
// 发布一次"的通道危险得多。
func TestPullRejectsOlderGeneration(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	newer := snapshotFiles()
	newer[ProvidersName] = []byte(`{"providers":{"第五代":{"base_url":"https://five"}}}`)
	h.setConfig(5, newer)
	c := newTestClient(t, h, root)
	mustPull(t, c, false)

	older := snapshotFiles()
	older[ProvidersName] = []byte(`{"providers":{"第三代":{"base_url":"https://three"}}}`)
	h.setConfig(3, older)

	out := mustPull(t, c, false)
	if out.Config.Changed {
		t.Fatal("旧代不该被应用")
	}
	if strings.Contains(string(readLive(t, ProvidersName)), "第三代") {
		t.Fatal("旧内容被写下去了——车队被回滚了")
	}
	if !strings.Contains(string(readLive(t, ProvidersName)), "第五代") {
		t.Fatal("原内容该原样保留")
	}
	if st, _ := LoadState(); st.Generation != 5 {
		t.Fatalf("记账里的代数被回退了: %d", st.Generation)
	}
}

// TestPullHostChangeResetsBaseline 锁住 HostID 存在的理由。
//
// 宿主的记账文件丢了（重装、换机、误删 host.json）时，generation 会从 0 重来。
// 只看代数的话，副本会认为"所有新快照都比我已经应用的旧"而**永久拒绝更新**——
// 一个静默的、只有重装宿主才能修的故障。
func TestPullHostChangeResetsBaseline(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	first := snapshotFiles()
	first[ProvidersName] = []byte(`{"providers":{"老宿主":{"base_url":"https://old"}}}`)
	h.setConfig(5, first)
	c := newTestClient(t, h, root)
	mustPull(t, c, false)

	// 换了权威，新宿主的计数从 2 开始（比 5 小）。
	h.setHostID("ffffffffffffffffffffffffffffffff")
	second := snapshotFiles()
	second[ProvidersName] = []byte(`{"providers":{"新宿主":{"base_url":"https://new"}}}`)
	h.setConfig(2, second)

	out := mustPull(t, c, false)
	if !out.Config.Changed {
		t.Fatalf("换成新权威时该应用，哪怕代数更小。实际 %+v", out.Config)
	}
	if !strings.Contains(string(readLive(t, ProvidersName)), "新宿主") {
		t.Fatal("新权威的内容没落盘")
	}
	if !anyContains(out.Config.Warnings, "authority changed") {
		t.Fatalf("权威变更必须说出来（否则代数的突然重置没人看得懂）: %v", out.Config.Warnings)
	}
	st, _ := LoadState()
	if st.Generation != 2 || !strings.HasPrefix(st.HostID, "ffff") {
		t.Fatalf("记账没跟上新权威: gen=%d host=%q", st.Generation, st.HostID)
	}
}

// TestPullSchemaTooNewRefusedAndKeepsPrevious 是两级门里有牙的那一级。
//
// schema 更高 = 字段含义可能已经变了，而**误读不会响亮报错**（会路由到错的
// 上游、或报"provider 没有 api_key"然后 404，看起来像上游故障）。所以拒绝应用、
// 保留上一份——仍然 fail-open：机器继续服务。
func TestPullSchemaTooNewRefusedAndKeepsPrevious(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	h.setConfig(1, snapshotFiles())
	c := newTestClient(t, h, root)
	mustPull(t, c, false)

	next := snapshotFiles()
	next[ProvidersName] = []byte(`{"providers":{"未来格式":{"base_url":"https://future"}}}`)
	next[MetaFileName] = metaFile(SupportedSchema+1, "")
	h.setConfig(2, next)

	out := mustPull(t, c, false)
	if !out.Config.Refused {
		t.Fatalf("schema 更高必须拒绝，实际 %+v", out.Config)
	}
	if !strings.Contains(out.Config.Reason, "schema_version") {
		t.Fatalf("拒绝理由该点名 schema_version: %q", out.Config.Reason)
	}
	if strings.Contains(string(readLive(t, ProvidersName)), "未来格式") {
		t.Fatal("被拒绝了却写了盘")
	}
	if !strings.Contains(string(readLive(t, ProvidersName)), "smt-deepseek") {
		t.Fatal("上一份配置该原样保留（fail-open：机器继续服务）")
	}
	// 代数**不推进**：升完级之后同一份快照会被重新应用，而不是被当成"已经处理过"。
	if st, _ := LoadState(); st.Generation != 1 {
		t.Fatalf("被拒绝时代数不该推进，否则升级后就永远收不到这份配置了: %d", st.Generation)
	}
	// 这是一次失败（要退避、要显示），但不是"故障"：Err 该是空的。
	if out.Config.Err != nil {
		t.Fatalf("schema 拒绝是明确判定，不是故障: %v", out.Config.Err)
	}
	if !out.Failed {
		t.Fatal("拒绝应用该算作没收敛（要进退避、status 要显示）")
	}
}

// TestPullVersionMismatchWarnsButApplies 是另一级：构建串不匹配 ⇒ 放行 + 警告。
// 硬拒绝会让一台还能用的机器收不到 provider 更新，把装饰性不匹配升级成事故。
func TestPullVersionMismatchWarnsButApplies(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	next := snapshotFiles()
	next[MetaFileName] = metaFile(SupportedSchema, "build-99-别的构建")
	h.setConfig(1, next)
	c := newTestClient(t, h, root)

	out := mustPull(t, c, false)
	if out.Config.Refused {
		t.Fatalf("构建串不匹配不该拒绝: %s", out.Config.Reason)
	}
	if !out.Config.Changed {
		t.Fatal("该照常应用")
	}
	if !anyContains(out.Config.Warnings, "build-99-别的构建") {
		t.Fatalf("该提醒构建串不匹配: %v", out.Config.Warnings)
	}
}

// TestPullDriftRefusedThenForced 是提交 1 里最重要的一条：漂移守卫。
//
// 要堵的失效模式是"副本上有人手改了 providers.json，以为生效了，下一个轮询
// 周期把它**静默**覆盖"。这条测试同时锁住三个动作：拒绝（非零退出、不写盘、
// 不归档）、记账里留下漂移（status 要显示）、--force 时先归档再覆盖。
func TestPullDriftRefusedThenForced(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	h.setConfig(1, snapshotFiles())
	c := newTestClient(t, h, root)
	mustPull(t, c, false)

	local := []byte(`{"providers":{"本地私有的":{"base_url":"http://127.0.0.1:1234"}}}`)
	writeLive(t, ProvidersName, local)
	remote := snapshotFiles()
	remote[ProvidersName] = []byte(`{"providers":{"远端的":{"base_url":"https://remote"}}}`)
	h.setConfig(2, remote)

	// ---- 第一轮：拒绝 ----
	out := mustPull(t, c, false)
	if !out.Config.Refused {
		t.Fatalf("有本地改动时必须拒绝应用，实际 %+v", out.Config)
	}
	if out.Config.Drift == nil || !out.Config.Drift.Blocking() {
		t.Fatal("该带出阻断性的漂移报告")
	}
	if !bytes.Equal(readLive(t, ProvidersName), local) {
		t.Fatal("被拒绝了却动了盘")
	}
	if n := backupCount(t); n != 0 {
		t.Fatalf("拒绝时不该产生归档目录（还没动用户的文件），实际 %d 个", n)
	}
	st, _ := LoadState()
	if st.Generation != 1 {
		t.Fatalf("被拒绝时代数不该推进（否则用户改回去后这份就再也不会被应用）: %d", st.Generation)
	}
	if st.Drift == nil || !st.Drift.Blocking() {
		t.Fatal("漂移是持续状态，必须留在记账里供 status 显示")
	}

	// ---- 第二轮：--force ----
	out = mustPull(t, c, true)
	if !out.Config.Changed {
		t.Fatalf("--force 该应用，实际 %+v", out.Config)
	}
	if !bytes.Contains(readLive(t, ProvidersName), []byte("远端的")) {
		t.Fatal("没写成远端内容")
	}
	if out.Config.Archive == "" {
		t.Fatal("--force 必须先归档：否则这条命令就是不可恢复的")
	}
	got, err := os.ReadFile(filepath.Join(out.Config.Archive, ProvidersName))
	if err != nil || !bytes.Equal(got, local) {
		t.Fatalf("归档里该有用户那份原稿: %q / %v", got, err)
	}
	st, _ = LoadState()
	if st.Drift != nil {
		t.Fatal("应用成功之后漂移该清掉")
	}
	if st.Generation != 2 {
		t.Fatalf("应用成功了代数该推进到 2，实际 %d", st.Generation)
	}
}

func TestPullSecretsRotation(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	h.setConfig(1, snapshotFiles())
	h.setSecrets(1, map[string]string{"smt-deepseek": "sk-1"})
	c := newTestClient(t, h, root)
	mustPull(t, c, false)

	// 轮换：改一个、加一个。
	h.setSecrets(2, map[string]string{"smt-deepseek": "sk-2", "glm": "sk-3"})
	out := mustPull(t, c, false)
	if !out.Secrets.Changed {
		t.Fatalf("密钥变了该落盘，实际 %+v", out.Secrets)
	}
	// 配置没变 → 配置通道该完全静默（两个通道独立的核心价值：改个 provider
	// 不该重新传一遍全部密钥，反之亦然）。
	if out.Config.Changed {
		t.Fatal("配置没变却被写了一遍——两个通道的代数必须各走各的")
	}
	for prov, want := range map[string]string{"smt-deepseek": "sk-2", "glm": "sk-3"} {
		if v, ok := LookupSecret(secretsPath(), prov); !ok || v != want {
			t.Fatalf("%s 的密钥不对: %q %v", prov, v, ok)
		}
	}
	if st, _ := LoadState(); st.SecretsGen != 2 {
		t.Fatalf("密钥代数没推进: %d", st.SecretsGen)
	}
}

// TestPullSecretsDriftRefused：密钥"只由宿主分发"是它的契约，但契约不构成
// "可以静默覆盖本机改动"的理由。同一条守卫。
func TestPullSecretsDriftRefused(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	h.setConfig(1, snapshotFiles())
	h.setSecrets(1, map[string]string{"smt-deepseek": "sk-1"})
	c := newTestClient(t, h, root)
	mustPull(t, c, false)

	if _, err := MaterializeSecrets(secretsPath(), map[string]string{"smt-deepseek": "sk-本地手改的"}); err != nil {
		t.Fatal(err)
	}
	h.setSecrets(2, map[string]string{"smt-deepseek": "sk-新的"})

	out := mustPull(t, c, false)
	if !out.Secrets.Refused {
		t.Fatalf("本机 secrets.json 被改过时必须拒绝，实际 %+v", out.Secrets)
	}
	if v, _ := LookupSecret(secretsPath(), "smt-deepseek"); v != "sk-本地手改的" {
		t.Fatalf("被拒绝了却覆盖了本地改动: %q", v)
	}
	if out.Failed != true {
		t.Fatal("拒绝应用该算作没收敛")
	}
}

func TestPullErrors(t *testing.T) {
	sandbox(t)
	root := rootKey(1)

	t.Run("对端不是宿主", func(t *testing.T) {
		h := newFakeHost(t, root)
		h.setConfig(1, snapshotFiles())
		h.setNotFound(true)
		out := mustPull(t, newTestClient(t, h, root), false)
		if !errors.Is(out.Config.Err, ErrNotHost) {
			t.Fatalf("该给 ErrNotHost，实际 %v", out.Config.Err)
		}
	})

	t.Run("根密钥不同（blob 拷错）", func(t *testing.T) {
		h := newFakeHost(t, root)
		h.setConfig(1, snapshotFiles())
		// 客户端 trust 了另一个 blob：bearer 令牌对不上 → 401。
		// 文案必须指向根密钥，否则用户会去怀疑网络。
		out := mustPull(t, newTestClient(t, h, rootKey(2)), false)
		if !errors.Is(out.Config.Err, ErrAuth) {
			t.Fatalf("该给 ErrAuth，实际 %v", out.Config.Err)
		}
		if !strings.Contains(i18n.ID(ErrAuth), "root key") {
			t.Fatalf("ErrAuth 文案该提到根密钥: %v", ErrAuth)
		}
	})

	t.Run("信封被篡改", func(t *testing.T) {
		h := newFakeHost(t, root)
		encKey, _, _ := DeriveKeys(root)
		plain, _ := json.Marshal(Snapshot{HostID: h.hostID(), Generation: 1, Files: snapshotFiles()})
		env, err := Seal(encKey, KindConfig, h.hostID(), 1, plain)
		if err != nil {
			t.Fatal(err)
		}
		env.CT[0] ^= 0x01
		raw, _ := json.Marshal(env)
		h.setRawConfig(raw)

		out := mustPull(t, newTestClient(t, h, root), false)
		if !errors.Is(out.Config.Err, ErrTampered) {
			t.Fatalf("篡改必须被发现，实际 %v", out.Config.Err)
		}
		if existsLive(ProvidersName) {
			t.Fatal("验不过的内容绝不该落盘")
		}
	})

	t.Run("响应不是信封", func(t *testing.T) {
		h := newFakeHost(t, root)
		h.setRawConfig([]byte("<html>这不是 JSON</html>"))
		out := mustPull(t, newTestClient(t, h, root), false)
		if out.Config.Err == nil {
			t.Fatal("不是信封的响应必须报错")
		}
	})
}

// TestPullRefusesUnmanagedFileInSnapshot：payload 来自网络，一台被控的（或写坏了
// 的）宿主不该有能力往 state.json、dump/、甚至配置目录之外写东西。
func TestPullRefusesUnmanagedFileInSnapshot(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	files := snapshotFiles()
	files["state.json"] = []byte(`{"default_profile":"被劫持"}`)
	files["../逃逸.json"] = []byte("{}")
	h.setConfig(1, files)
	c := newTestClient(t, h, root)

	out := mustPull(t, c, false)
	if !out.Config.Refused {
		t.Fatalf("托管集合之外的文件必须被拒绝，实际 %+v", out.Config)
	}
	if existsLive("state.json") {
		t.Fatal("state.json 被写下来了（那是机器本地状态）")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(paths.Config()), "逃逸.json")); err == nil {
		t.Fatal("写到配置目录之外去了")
	}
}

func TestPullInlineKeyRefused(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	files := snapshotFiles()
	files[ProvidersName] = []byte(`{"providers":{"x":{"base_url":"https://x","api_key":"sk-明文"}}}`)
	h.setConfig(1, files)

	out := mustPull(t, newTestClient(t, h, root), false)
	if !out.Config.Refused {
		t.Fatalf("内联密钥必须被拒绝: %+v", out.Config)
	}
	if !strings.Contains(out.Config.Reason, "secrets channel") {
		t.Fatalf("拒绝理由该说清密钥该走哪条路: %q", out.Config.Reason)
	}
}

// TestPullRemovesRetiredFiles 是"只写不删"那个坑的反面：宿主下线一个档位之后，
// 副本上不能永远留着它（否则 @那个档位 之类的引用会一直解析得到东西）。
func TestPullRemovesRetiredFiles(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	h.setConfig(1, snapshotFiles())
	c := newTestClient(t, h, root)
	mustPull(t, c, false)
	if !existsLive("mappings/prod.json") {
		t.Fatal("前置条件不对")
	}

	next := snapshotFiles()
	delete(next, "mappings/prod.json")
	h.setConfig(2, next)
	out := mustPull(t, c, false)
	if len(out.Config.Removed) != 1 || out.Config.Removed[0] != "mappings/prod.json" {
		t.Fatalf("该删掉下线的档位，实际 %v", out.Config.Removed)
	}
	if existsLive("mappings/prod.json") {
		t.Fatal("下线的档位还在")
	}
}
