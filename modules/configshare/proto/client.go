package proto

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/rzbdz/newgate/lib/i18n"
	paths "github.com/rzbdz/newgate/modules/config/paths"
)

// maxEnvelopeBytes 单次拉取的大小上限。
//
// 配置快照就是几十 KB 的 JSON，密钥更小。给上限是因为**对端的行为不由我们
// 控制**（中间还可能有跳板/代理宿主，endpoint 也可能配错打到别的东西上）：
// 没有上限的话一次拉取就能把 daemon 的内存吃光。8 MiB 是"离现实很远、
// 离危险也很远"的一个数。
const maxEnvelopeBytes = 8 << 20

// 两种要说人话的拉取失败。它们都是**第一分钟就会遇到**的错（没设 role、
// blob 拷错），所以文案直接写排查方向，而不是抛一个状态码。
//
// 这里用 i18n.E 而不是 i18n.T：E 只把源语言原文存下来（消息身份），渲染发生在
// Error() 被调用的那一刻，也就是语言装好之后。包级变量里写 i18n.T 会把译文
// 永久冻在源语言上（见 lib/i18n 的包注释）。
var (
	ErrNotHost = i18n.E("the peer is not a host (the endpoint returned 404) — is its role not set to host?", nil)
	ErrAuth    = i18n.E("authentication refused: this machine's root key differs from the host's (the config trust blob was copied wrong or not at all)", nil)
)

// Config 是构造一个副本客户端需要的全部东西。
//
// 路径不在这里：一律走 paths 包（本仓库配置目录路径的唯一来源），测试靠
// NEWGATE_HOME 隔离（testkit.Sandbox 的同一套做法）。
type Config struct {
	Endpoint string // 形如 https://gateway-host.tailnet.ts.net:8899
	RootKey  []byte // 预共享根密钥原文
	Version  string // 本二进制构建串，喂给 Gate
	Log      *log.Logger
	HTTP     *http.Client // nil = 默认（20s 超时）
	Now      func() time.Time
}

// Client 是副本侧的拉取器与应用器。
type Client struct {
	cfg       Config
	encKey    []byte
	authToken []byte
	http      *http.Client
	now       func() time.Time
}

// New 派生密钥并构造客户端。
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, i18n.E("the endpoint is empty (run newgate config join <endpoint> first)", nil)
	}
	encKey, token, err := DeriveKeys(cfg.RootKey)
	if err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, encKey: encKey, authToken: token, http: cfg.HTTP}
	if c.http == nil {
		c.http = &http.Client{Timeout: 20 * time.Second}
	}
	c.now = cfg.Now
	if c.now == nil {
		c.now = time.Now
	}
	return c, nil
}

// Channel 是一个通道这一轮的结果。
type Channel struct {
	Kind string
	Gen  int // 这一轮之后生效的代数

	// Changed 内容变了并且已经落盘。**只有它为真才该打"已更新"那行日志**：
	// 代数涨了但字节没变（宿主重新 publish 了一份一样的内容）不该装作更新过。
	Changed   bool
	Refused   bool // 拉到了、但拒绝应用（保留上一份继续服务）
	Reason    string
	Err       error // 这一通道自身的错误（网络、解密、校验）
	Drift     *DriftReport
	Written   []string
	Removed   []string
	Unchanged int
	Archive   string
	Warnings  []string
}

// Outcome 是一轮拉取的结果。
//
// 两个通道各自独立：配置端点抖一下不该让密钥轮换也停摆，反过来也一样。
// 所以错误是按通道记的，Pull 的返回值只报"连记账都写不下去"这种全局失败。
type Outcome struct {
	Config  Channel
	Secrets Channel
	Notes   []string // 给调用方展示的旁注（记账损坏、权威变更…）

	// 下面几个是 finalize 汇总出来的"这一轮整体怎么样"，调用方只负责**渲染**
	// ——记账与判定都发生在 Pull 的锁里面，见 finalize 的注释。
	Failed    bool
	ErrText   string // 稳定的错误串（同一问题重复出现时字符串不变，日志抑制靠它）
	FailCount int    // 这一轮之后连续失败的次数
	Recovered bool   // 这一轮恢复（此前有失败）
	PrevGen   int    // 这一轮之前生效的配置代数（日志里写 "41 → 42" 用）
}

// Pull 拉一轮并应用。
//
// 顺序是**先配置后密钥**：配置通道会更新记账里的权威身份（HostID），密钥
// 通道紧跟着用同一个身份判断"权威换了吗"，顺序反了就会多一次多余的代数
// 基线重置。
func (c *Client) Pull(ctx context.Context, force bool) (Outcome, error) {
	var out Outcome
	run := func() error {
		st, err := LoadState()
		if err != nil {
			// 记账坏了不该让同步彻底停摆：按初始状态重来，但要说出来。
			out.Notes = append(out.Notes, err.Error())
			c.logf("[configshare] %v", err)
		}
		out.PrevGen = st.Generation
		out.Config = c.pullConfig(ctx, &st, force)
		out.Secrets = c.pullSecrets(ctx, &st, force)
		out.finalize(&st, c.now())
		return SaveState(st)
	}
	// 拿锁跑完整轮：CLI 的 config pull 与后台 poller 可能同时跑，两边都要
	// 读改写同一份记账。等 3 秒（手动命令的耐心），后台那条走 wait=0。
	ran, err := WithLock(ConfigShareLockFile(), 3*time.Second, run)
	if err != nil {
		return out, err
	}
	if !ran {
		out.Notes = append(out.Notes, i18n.T("another sync round is already running; this round was skipped", nil))
	}
	return out, nil
}

// finalize 汇总这一轮，并把失败计数写进记账。
//
// 为什么在 Pull 里面而不是让每个调用方各写一遍：记账**只有一处写入者**才不会
// 互相覆盖，而 Pull 已经在锁里了。调用方（后台 poller / config pull）只负责
// 按自己的语气渲染这个结果。
//
// ErrText 的稳定性是有意的：同一类问题重复出现时字符串必须一模一样，否则
// poller 的日志抑制（"同一错误只按 1/2/5/10/25/50… 报"）会失效，变成每 30 秒
// 刷一行。所以拒绝原因的文案里**不放文件名列表**，只放个数。
func (o *Outcome) finalize(st *State, now time.Time) {
	var msgs []string
	for _, ch := range []Channel{o.Config, o.Secrets} {
		switch {
		case ch.Err != nil:
			msgs = append(msgs, ch.Err.Error())
		case ch.Refused:
			msgs = append(msgs, ch.Reason)
		}
	}
	if len(msgs) == 0 {
		if st.FailCount > 0 {
			o.Recovered = true
		}
		st.FailCount = 0
		st.LastError = ""
		st.LastSuccessAt = now
		return
	}
	o.Failed = true
	// 连接符过 i18n（中文是「；」）。ErrText 的稳定性不受影响：一次进程只装一门
	// 语言，同一类问题在同一进程里拼出来的串还是一模一样的。
	o.ErrText = strings.Join(msgs, i18n.T("; ", nil))
	st.FailCount++
	st.LastError = o.ErrText
	st.LastErrorAt = now
	o.FailCount = st.FailCount
}

// pullConfig 是配置通道的一轮。
func (c *Client) pullConfig(ctx context.Context, st *State, force bool) Channel {
	ch := Channel{Kind: KindConfig}
	env, err := c.fetch(ctx, KindConfig, PathConfig)
	if err != nil {
		ch.Err = err
		return ch
	}
	ch.Gen = env.Gen

	// ---- 快路径：权威没换、代数没变 ----
	//
	// 连解密都不做、不写盘、不打日志。这是"空转完全静默"的落地点：一天 2880
	// 轮里绝大多数走到这里就回去了。
	//
	// 这里 <= 而不是 == 是**单调性守卫**：迟到的、乱序的、或者被中间人重放的
	// 旧快照不能把车队回滚。代价是宿主侧的**故意回滚**（恢复一份旧配置）不会
	// 传播——要回滚就得让宿主往上 bump 一个代数（宿主侧 newgate config publish
	// 就是这么做的，见 host.go）。这个代价是有意接受的：一个能悄悄把整个车队
	// 降级的通道，比一个"回滚要重新发布一次"的通道危险得多。
	if st.HostID == env.Host && env.Gen <= st.Generation {
		ch.Gen = st.Generation
		return ch
	}

	plain, err := Open(c.encKey, KindConfig, env)
	if err != nil {
		ch.Err = err
		return ch
	}
	var snap Snapshot
	if err := json.Unmarshal(plain, &snap); err != nil {
		ch.Err = i18n.Ef(err, "cannot parse the snapshot: {err}", nil)
		return ch
	}
	if snap.HostID == "" {
		snap.HostID = env.Host // 宿主没填就用信封上那个（两者本应一致）
	}
	if snap.Generation != env.Gen {
		// 信封说 42、密文里说 41：宿主端拼装出了 bug。记账只认一个数，猜不得。
		ch.Err = i18n.E("the envelope generation {envelope} does not match the generation inside the snapshot {snapshot} (the host assembled it wrong)",
			i18n.A{"envelope": env.Gen, "snapshot": snap.Generation})
		return ch
	}
	if st.HostID != "" && st.HostID != snap.HostID {
		out := i18n.T("authority changed ({from} → {to}); the generation baseline was reset",
			i18n.A{"from": short(st.HostID), "to": short(snap.HostID)})
		ch.Warnings = append(ch.Warnings, out)
		c.logf("[configshare] %s", out)
		// 重置基线：新宿主的计数从 0 开始，不重置就永远追不上（见 types.go）。
		st.Generation = 0
	}

	// ---- 校验：schema 门 → 内容形状 ----
	meta, err := ParseMeta(snap.Files)
	if err != nil {
		ch.Refused, ch.Reason, ch.Err = true, err.Error(), err
		return ch
	}
	ok, warn, refuse := Gate(meta, GateInput{CurrentVersion: c.cfg.Version})
	if warn != "" {
		ch.Warnings = append(ch.Warnings, warn)
	}
	if !ok {
		// 拒绝应用但**不报错**：这不是故障，是一个明确的"这份我解释不了"。
		// 保留上一份继续服务，状态里显示成需要处理的事。
		ch.Refused, ch.Reason = true, refuse
		return ch
	}
	warns, err := ValidateSnapshot(paths.Config(), snap.Files)
	ch.Warnings = append(ch.Warnings, warns...)
	if err != nil {
		ch.Refused, ch.Reason, ch.Err = true, err.Error(), err
		return ch
	}

	// ---- 漂移守卫 ----
	drift := DetectDrift(paths.Config(), st.Applied, snap.Files)
	if drift.Blocking() {
		ch.Drift = &drift
		if !force {
			ch.Refused = true
			ch.Reason = i18n.N(
				"{n} managed file on this machine was modified locally; applying the remote configuration stopped (your changes will not be overwritten)",
				"{n} managed files on this machine were modified locally; applying the remote configuration stopped (your changes will not be overwritten)",
				len(drift.Paths()), i18n.A{"n": len(drift.Paths())})
			// 记账**不动**：代数不推进，下一轮还会重试这个决定。这样用户改回去
			// 或明确 --force 之后，同一个代数会被重新应用一次。
			st.Drift = &drift
			return ch
		}
		arch, err := ArchiveFiles(paths.Config(), stamp(c.now()), drift.Paths())
		if err != nil {
			ch.Refused, ch.Reason, ch.Err = true, err.Error(), err
			return ch
		}
		ch.Archive = arch
	}

	// ---- 落盘 ----
	res, err := Materialize(paths.Config(), snap.Files, st.Applied)
	if err != nil {
		ch.Refused, ch.Reason, ch.Err = true, err.Error(), err
		return ch
	}
	ch.Written, ch.Removed, ch.Unchanged = res.Written, res.Removed, len(res.Unchanged)
	ch.Changed = len(res.Written)+len(res.Removed) > 0

	// ---- 记账 ----
	st.HostID = snap.HostID
	st.Generation = env.Gen
	st.Version = snap.Version
	st.AppliedAt = c.now()
	st.Applied = res.Manifest
	st.Warnings = ch.Warnings
	// 记账里的漂移要描述**现在**的状况，而不是我们刚修好之前的样子：
	// 落盘之后再算一次。--force 之后阻断项已经归档并覆盖，重算就干净了；而
	// 本地层（Extra）会照旧出现在里面——它本来就该一直显示着（不显示就是
	// silent-override trap）。
	st.Drift = driftOrNil(DetectDrift(paths.Config(), res.Manifest, snap.Files))
	return ch
}

// pullSecrets 是密钥通道的一轮。
func (c *Client) pullSecrets(ctx context.Context, st *State, force bool) Channel {
	ch := Channel{Kind: KindSecrets}
	env, err := c.fetch(ctx, KindSecrets, PathSecrets)
	if err != nil {
		ch.Err = err
		return ch
	}
	ch.Gen = env.Gen
	if st.HostID == env.Host && env.Gen <= st.SecretsGen {
		ch.Gen = st.SecretsGen
		return ch
	}
	plain, err := Open(c.encKey, KindSecrets, env)
	if err != nil {
		ch.Err = err
		return ch
	}
	var sec Secrets
	if err := json.Unmarshal(plain, &sec); err != nil {
		ch.Err = i18n.Ef(err, "cannot parse the secret set: {err}", nil)
		return ch
	}
	if sec.Generation != 0 && sec.Generation != env.Gen {
		ch.Err = i18n.E("the envelope generation {envelope} does not match the generation inside the secret set {secrets}",
			i18n.A{"envelope": env.Gen, "secrets": sec.Generation})
		return ch
	}
	want, err := secretsBytes(sec.Keys)
	if err != nil {
		ch.Err = err
		return ch
	}
	sum := hashBytes(want)

	// 漂移守卫，与配置通道同一个道理：本机的 secrets.json 被人手改过，而远端
	// 又要写一份不同的，那就是要抹掉别人的改动——必须先说、先归档。
	// 密钥"只由宿主分发"是它的契约，但契约不构成"可以静默覆盖"的理由。
	path := secretsPath()
	if cur, err := readFileIfExists(path); err == nil && cur != nil {
		curSum := hashBytes(cur)
		if curSum != st.SecretsApplied && curSum != sum {
			dr := &DriftReport{Modified: []string{SecretsFileName}, At: c.now()}
			ch.Drift = dr
			if !force {
				ch.Refused = true
				ch.Reason = i18n.T("this machine's {name} was modified locally (your changes will not be overwritten)",
					i18n.A{"name": SecretsFileName})
				st.SecretsDrift = dr
				return ch
			}
			arch, err := ArchiveFiles(paths.Config(), stamp(c.now()), []string{SecretsFileName})
			if err != nil {
				ch.Refused, ch.Reason, ch.Err = true, err.Error(), err
				return ch
			}
			ch.Archive = arch
		}
	}

	written, err := MaterializeSecrets(path, sec.Keys)
	if err != nil {
		ch.Refused, ch.Reason, ch.Err = true, err.Error(), err
		return ch
	}
	ch.Changed = written != st.SecretsApplied

	st.SecretsGen = env.Gen
	st.SecretsApplied = written
	st.SecretsAt = c.now()
	// 落盘成功了，密钥通道的漂移就解决了（--force 时已经归档过）。
	st.SecretsDrift = nil
	if sec.HostID != "" {
		st.HostID = sec.HostID
	}
	return ch
}

// fetch 取一个端点上的信封。
func (c *Client) fetch(ctx context.Context, kind, path string) (*Envelope, error) {
	url := strings.TrimRight(c.cfg.Endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// token 是 HKDF 派生的**另一个**值，不是加密密钥：它进 header、进日志、
	// 进任何中间件，而这些地方泄露不了加密密钥（见 seal.go 的 DeriveKeys）。
	// 用 hex 而不是原始字节：header 里只放可见 ASCII。
	req.Header.Set("Authorization", "Bearer "+hex.EncodeToString(c.authToken))
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, i18n.Ef(err, "cannot reach {url}: {err}", i18n.A{"url": url})
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotHost
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrAuth
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, i18n.E("{url} returned {status}: {body}",
			i18n.A{"url": url, "status": resp.Status, "body": strings.TrimSpace(string(body))})
	}

	var env Envelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxEnvelopeBytes)).Decode(&env); err != nil {
		return nil, i18n.Ef(err, "the response from {url} is not a valid envelope: {err}", i18n.A{"url": url})
	}
	return &env, nil
}

func (c *Client) logf(format string, args ...any) {
	if c.cfg.Log != nil {
		c.cfg.Log.Printf(format, args...)
	}
}

// short 把 host_id 截短用于日志（它是 32 位 hex，整串在日志里没用）。
func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	if id == "" {
		return i18n.T("(none)", nil)
	}
	return id
}
