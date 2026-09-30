package sshtunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Session 是一条已经建立、可以往里要通道的 SSH 连接。
//
// 它是一个**接口**，理由只有一个但足够：测试里不需要一台真的 sshd。假实现用
// net.Pipe 就能把「请求来了才拨号」「连上之后复用」「空闲了断开」这些**这个模块
// 自己的**行为验干净；而「真的能连上一台 OpenSSH」是另一件事，它由那条真 sshd 的
// 端到端锁住（见 mock/e2e_tunnel.sh）。前者跑在每次提交里，后者跑在本地。
type Session interface {
	// Dial 在连接上开一条到 addr 的通道（等价于 ssh.Client.Dial）。
	Dial(network, addr string) (net.Conn, error)
	// SendRequest 是 ssh.Client.SendRequest 的转发：keepalive 要用它。
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
	Close() error
}

// Dialer 拨一条到目标机器的 SSH 连接。
type Dialer interface {
	Dial(ctx context.Context, t Target) (Session, error)
}

// ---------- 真实现 ----------

// sshDialer 是拨号的全部知识：怎么认证、信不信那台机器、怎么把 home 目录里的
// 那些约定接起来。
type sshDialer struct {
	home string
	// agentFn 是测试用的接缝（真实路径走 SSH_AUTH_SOCK）。留 nil = 按环境找。
	agentFn func() ([]ssh.Signer, error)

	mu sync.Mutex
	// loggedKeys 记住已经打过日志的已知主机写入，避免每条连接都刷屏。
	loggedKeys map[string]bool

	cfgOnce sync.Once
	cfg     map[string]sshAlias

	// handshake 是「TCP 连上之后等握手做完」的上限。留 0 = 用 handshakeBudget。
	// 做成字段只有一个理由：**测试要能在几百毫秒里验完这件事**——那个 20 秒的
	// 值在真实使用里是对的，在一个每次提交都跑的测试里是灾难（实测：一条 20 秒，
	// 而它验的是一条时间上限，不是那个具体的数）。
	handshake time.Duration
}

// handshakeOr 是这次要用的握手期限。
func (d *sshDialer) handshakeOr() time.Duration {
	if d.handshake > 0 {
		return d.handshake
	}
	return handshakeBudget
}

func newSSHDialer() *sshDialer {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return &sshDialer{home: home, loggedKeys: map[string]bool{}}
}

// Dial 建立一条连接。
//
// 每一步的失败都带着**是哪一步**回出去：DNS 解析不了、TCP 连不上、握手被拒、
// 密钥不对、known_hosts 不认识这台机器——这五种在用户眼里天差地别，而它们对应的
// 补救动作完全不同（改地址 / 开防火墙 / 看 sshd 日志 / 加公钥 / 先 ssh 一次）。
// 合成一句「连接失败」等于把排查成本整个推给用户。
func (d *sshDialer) Dial(ctx context.Context, t Target) (Session, error) {
	client, err := d.dialOnce(ctx, t.normalize())
	if err != nil {
		return nil, err
	}
	return &sshSession{client: client}, nil
}

// sshSession 把 *ssh.Client 收成 Session。
type sshSession struct{ client *ssh.Client }

func (s *sshSession) Dial(network, addr string) (net.Conn, error) {
	return s.client.Dial(network, addr)
}

func (s *sshSession) SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error) {
	return s.client.SendRequest(name, wantReply, payload)
}

func (s *sshSession) Close() error { return s.client.Close() }

// resolved 是「把 ~/.ssh/config 合完之后」的那组值。
type resolved struct {
	Host     string
	Port     int
	User     string
	Identity string
	// Notices 是 ssh_config 里我们**认不出但会影响连通性**的键，原样报给用户。
	Notices []string
}

// resolve 把配置里那几个字段与 ~/.ssh/config 合起来，得到这次真正要用的值。
//
// 顺序是 ssh 自己的顺序：**显式给的压过 ssh_config**。这里没有「命令行」这一层
// （配置就在 state.json 里），所以判据是「state.json 里写了没有」。
func (d *sshDialer) resolve(t Target) resolved {
	r := resolved{
		Host: t.Host, Port: t.Port, User: t.User,
		Identity: expandHome(t.Identity, d.home),
	}
	alias, ok := d.sshConfig()[t.Host]
	if !ok {
		return r
	}
	// 用户写的是 `snode1` 这种别名时，HostName 才是真地址。写的是真地址时别名表里
	// 通常没有它，走不到这里。
	if alias.HostName != "" {
		r.Host = alias.HostName
	}
	// 只在用户**没显式写端口**时采用。22 就是默认值，写成 22 与没写等价。
	if alias.Port != 0 && t.Port == DefaultSSHPort {
		r.Port = alias.Port
	}
	if alias.User != "" && t.User == "" {
		r.User = alias.User
	}
	if len(alias.IdentityFiles) > 0 && t.Identity == "" {
		r.Identity = expandHome(alias.IdentityFiles[0], d.home)
	}
	r.Notices = alias.Notices
	return r
}

// auth 是这次要试的认证方式。
//
// 顺序照 ssh 的老规矩：先身份文件，最后 agent。密钥文件在前是因为它更「具体」——
// 用户显式指了一把钥匙，就该先用它。
func (d *sshDialer) auth(r resolved) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	for _, path := range d.keyPaths(r) {
		signer, err := readSigner(path)
		if err != nil {
			// 文件在、但用不了（权限不对 / 有口令 / 不是密钥）：这是用户该知道的事，
			// 不能咽下去——咽下去的症状是「明明配了密钥，它却说没有可用密钥」。
			return nil, i18n.E("cannot use the private key {path}: {err}",
				i18n.A{"path": path, "err": err})
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if signers, err := d.agentSigners(); err == nil && len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	if len(methods) == 0 {
		// 空列表交给 ssh 的后果是一句「no auth methods」，用户不知道下一步做什么。
		return nil, i18n.E("no usable key for {host}: look in {dir} or start ssh-agent "+
			"(passwords are not supported — this module only does keys and the agent)",
			i18n.A{"host": r.Host, "dir": d.sshDir()})
	}
	return methods, nil
}

// keyPaths 是这次要按顺序试的身份文件。
//
// 显式给了就**只**试那一个：用户指了钥匙，别自作主张再试别的——试出来的结果不是
// 他想要的那把。没给就试 ~/.ssh 下那几个，按 ssh 自己的顺序。
func (d *sshDialer) keyPaths(r resolved) []string {
	if r.Identity != "" {
		return []string{r.Identity}
	}
	var out []string
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa", "id_dsa"} {
		p := filepath.Join(d.sshDir(), name)
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func (d *sshDialer) sshDir() string {
	if d.home == "" {
		return "~/.ssh"
	}
	return filepath.Join(d.home, ".ssh")
}

// knownHostsPath 是这次要读写的 known_hosts。
func (d *sshDialer) knownHostsPath() string { return filepath.Join(d.sshDir(), "known_hosts") }

// agentSigners 找 agent 里的钥匙。
//
// 走 SSH_AUTH_SOCK（ssh 自己的约定）。找不到 agent 不是错误——没有 agent 的人
// 本该用密钥文件，那条路在调用方已经先试过了。
func (d *sshDialer) agentSigners() ([]ssh.Signer, error) {
	if d.agentFn != nil {
		return d.agentFn()
	}
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("SSH_AUTH_SOCK is not set")
	}
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return nil, err
	}
	// **不关**这条连接：agent 的签名者是按需去连它签的，关了之后签名会失败。
	// 量是每个 target 一条（不是每个请求一条），跟着进程走。
	return agent.NewClient(conn).Signers()
}

// hostKeyCallback 决定信不信这台机器。
func (d *sshDialer) hostKeyCallback(t Target) (ssh.HostKeyCallback, error) {
	known := d.knownHostsPath()
	base, err := knownhosts.New(known)
	if err != nil && !os.IsNotExist(err) {
		return nil, i18n.E("cannot read {path}: {err}", i18n.A{"path": known, "err": err})
	}
	if err != nil {
		// 文件还不存在是**全新机器**的常态，不是错误。造一个「谁都没见过」的判据，
		// 让下面那两层能照常工作（strict 拒、accept-new 记）。
		base = unknownHost
	}

	switch t.HostKey {
	case HostKeyAcceptNew:
		return d.acceptNew(known, base), nil
	default:
		// strict：不认识就拒。错误文案要给**下一步**——只说「host key verification
		// failed」的话，用户的第一反应是关掉校验，那是更坏的选择。
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			err := base(hostname, remote, key)
			if err == nil {
				return nil
			}
			var ke *knownhosts.KeyError
			if errors.As(err, &ke) && len(ke.Want) == 0 {
				return i18n.E("the host key of {host} is not in {path} — connect once from a "+
					"terminal (`ssh {host}`) and answer yes, or set host_key to \"accept-new\" "+
					"on this target", i18n.A{"host": hostname, "path": known})
			}
			return i18n.E("the host key of {host} does not match the one recorded in {path} — "+
				"that is either a rebuilt machine or somebody in the middle",
				i18n.A{"host": hostname, "path": known})
		}, nil
	}
}

// unknownHost 是「known_hosts 里谁都没见过」的判据。
func unknownHost(hostname string, remote net.Addr, key ssh.PublicKey) error {
	return &knownhosts.KeyError{Want: nil}
}

// acceptNew 是 accept-new：第一次见到就记下来，之后仍然要一致。
//
// 每次都打一条日志：**信任一台机器是一个事件**，不是一次内部状态变化。用户第一次
// 连上去之后该在日志里看见「我记下了这台机器的钥匙」，否则他没有任何机会发现
// 「我以为连的是 A，其实连的是 B」。
func (d *sshDialer) acceptNew(known string, base ssh.HostKeyCallback) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := base(hostname, remote, key)
		if err == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		if !errors.As(err, &ke) || len(ke.Want) != 0 {
			// 记录里有、但对不上：**不覆盖**。accept-new 的语义是「没见过就记」，
			// 不是「变了也认」——后者是最危险的那种静默降级（TOFU 被撤掉）。
			return err
		}
		if werr := appendKnownHost(known, hostname, key); werr != nil {
			return i18n.E("the host key of {host} is new and {path} is not writable: {err}",
				i18n.A{"host": hostname, "path": known, "err": werr})
		}
		d.mu.Lock()
		first := !d.loggedKeys[hostname]
		d.loggedKeys[hostname] = true
		d.mu.Unlock()
		if first {
			log.Printf("[tunnel] %s", i18n.T("recorded the host key of {host} in {path} ({fp})",
				i18n.A{"host": hostname, "path": known, "fp": ssh.FingerprintSHA256(key)}))
		}
		return nil
	}
}

// appendKnownHost 往 known_hosts 追加一行（照 knownhosts.Line 的格式）。
func appendKnownHost(path, hostname string, key ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key))
	return err
}

// readSigner 读一把私钥。
func readSigner(path string) (ssh.Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := ssh.ParsePrivateKey(b)
	if err == nil {
		return s, nil
	}
	var pe *ssh.PassphraseMissingError
	if errors.As(err, &pe) {
		// 有口令的密钥 ssh 自己会弹提示，但我们是一个 daemon，没有终端可弹。
		// 说清楚该走哪条路（agent），比让它在后台永远卡着好。
		return nil, i18n.E("it is protected by a passphrase; add it to ssh-agent "+
			"(`ssh-add {path}`) and leave identity empty", i18n.A{"path": path})
	}
	return nil, err
}

// dialOnce 真的拨一次，把每一步的失败都标出来。
func (d *sshDialer) dialOnce(ctx context.Context, t Target) (*ssh.Client, error) {
	r := d.resolve(t)
	methods, err := d.auth(r)
	if err != nil {
		return nil, err
	}
	cb, err := d.hostKeyCallback(t)
	if err != nil {
		return nil, err
	}
	user := r.User
	if user == "" {
		user = currentUserName()
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            methods,
		HostKeyCallback: cb,
		// 握手与认证的总预算。**这个字段只有在走 ssh.Dial 时才生效**——我们用的是
		// ssh.NewClientConn（为了能把 context 与自己的拨号器接上），那条路上它被
		// 完全忽略。真正管住「黑洞机器」的是下面那个 conn 上的 deadline；这里留着
		// 是为了「读的人以为它管用」这件事不成立——见 handshakeBudget 的注释。
		Timeout: d.handshakeOr(),
	}
	for _, n := range r.Notices {
		log.Printf("[tunnel] %s", n)
	}

	addr := net.JoinHostPort(r.Host, strconv.Itoa(r.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, i18n.E("cannot reach {addr}: {err}", i18n.A{"addr": addr, "err": err})
	}
	// 握手要有期限。一台 TCP 连得上、但永远不回 banner 的机器（防火墙的
	// 「黑洞」模式、或者一个卡死的 sshd）在这里会让整个请求挂到天荒地老——而
	// 浏览器那边看起来只是「一直在转」。ClientConfig.Timeout 在这条路上不管用
	// （见上），所以期限只能设在 conn 上，握手完再清掉：**它之后是一条长连接，
	// 带着 deadline 的话每几分钟就会自己断一次**。
	if err := conn.SetDeadline(time.Now().Add(d.handshakeOr())); err != nil {
		_ = conn.Close()
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, i18n.E("SSH handshake with {addr} failed: {err}", i18n.A{"addr": addr, "err": err})
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// handshakeBudget 是「TCP 连上之后，等它把 SSH 握手做完」的上限。
//
// 20 秒：公钥运算在内网是几十毫秒，跨公网也是几百毫秒；给到 20 秒是为了容忍
// 一次慢的 DNS/路由，同时短到让用户在浏览器自己超时之前先看到一个说得清的错误
// （而「先失败的那一方能给出理由」是这里的全部意义）。
const handshakeBudget = 20 * time.Second

func currentUserName() string {
	for _, k := range []string{"USER", "LOGNAME"} {
		if u := os.Getenv(k); u != "" {
			return u
		}
	}
	return "root"
}

// expandHome 把 ~ 展开。**只认裸 ~ 与 ~/ 开头**，不认 ~other——那要查 passwd，
// 而配置里出现它的概率远低于「写错一个字符却看着像对的」的风险。
func expandHome(p, home string) string {
	if p == "" || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// ---------- ~/.ssh/config ----------

// sshAlias 是 ssh_config 里一个 Host 块里我们认的那几个键。
type sshAlias struct {
	HostName      string
	Port          int
	User          string
	IdentityFiles []string
	// Notices 是认不出、但会影响连通性的键（ProxyJump / ProxyCommand / Match…）。
	Notices []string
}

// loudUnknown 是**必须报出来**的那几个键：它们的语义都是「这次连接不是直连」，
// 而我们只做直连。静默忽略的后果是用户以为自己在走跳板机，实际直连了一个不可达的
// 地址——而那种失败看起来像网络问题。
var loudUnknown = []string{"ProxyJump", "ProxyCommand", "Match", "LocalCommand", "RemoteCommand"}

// sshConfig 读 ~/.ssh/config，按 Host 建索引。
//
// 自己写解析器而不是引第三方：我们只认四个键，而这个文件的格式（Key Value、
// 大小写不敏感的键名、# 注释、Host 可以写好几个模式）几句话就说完。为了「读一个
// 文本文件」多一个依赖不划算。
//
// **缓存**（sync.Once）：这个函数在每条连接、每次快照里都会被调。代价是改了
// ssh_config 要重启 daemon 才生效——这条写在 README 里，不然它就是一条静默的
// 「我改了它没反应」。
func (d *sshDialer) sshConfig() map[string]sshAlias {
	d.cfgOnce.Do(func() { d.cfg = parseSSHConfig(filepath.Join(d.sshDir(), "config")) })
	return d.cfg
}

// parseSSHConfig 是那个解析器，独立出来是为了能单测（给它一个文件路径就行）。
func parseSSHConfig(path string) map[string]sshAlias {
	out := map[string]sshAlias{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	var patterns []string
	cur := sshAlias{}
	flush := func() {
		for _, p := range patterns {
			if p == "*" || strings.ContainsAny(p, "*?!") {
				// 通配块（含 `Host *`）是拿来给默认值的，而默认值的继承规则
				// （先出现的赢、Host * 垫底）比我们这个解析器愿意承担的复杂。
				// **不实现它**，而不是实现一半——实现一半的那种错是「某些机器
				// 悄悄用了 `Host *` 里的 User」，看起来完全正常。
				continue
			}
			if existing, ok := out[p]; ok {
				// 后出现的块**补**前面缺的键（ssh 的语义是先出现的赢，
				// 「已经有的不覆盖」是同一个效果）。
				if existing.HostName == "" {
					existing.HostName = cur.HostName
				}
				if existing.Port == 0 {
					existing.Port = cur.Port
				}
				if existing.User == "" {
					existing.User = cur.User
				}
				if len(existing.IdentityFiles) == 0 {
					existing.IdentityFiles = cur.IdentityFiles
				}
				existing.Notices = append(existing.Notices, cur.Notices...)
				out[p] = existing
				continue
			}
			out[p] = cur
		}
		patterns = nil
	}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := splitDirective(line)
		if !ok {
			continue
		}
		switch strings.ToLower(key) {
		case "host":
			flush()
			patterns = strings.Fields(val)
			cur = sshAlias{}
		case "hostname":
			cur.HostName = val
		case "port":
			if n, err := strconv.Atoi(val); err == nil {
				cur.Port = n
			}
		case "user":
			cur.User = val
		case "identityfile":
			cur.IdentityFiles = append(cur.IdentityFiles, val)
		default:
			for _, loud := range loudUnknown {
				if strings.EqualFold(key, loud) {
					cur.Notices = append(cur.Notices, i18n.T(
						"ssh_config sets {key} for this host, but this module only makes direct "+
							"connections — that setting is being ignored", i18n.A{"key": key}))
					break
				}
			}
		}
	}
	flush()
	return out
}

// splitDirective 拆一行 `Key Value`（值里可能有引号、可能有行尾注释）。
func splitDirective(line string) (key, val string, ok bool) {
	i := strings.IndexAny(line, " \t=")
	if i <= 0 {
		return "", "", false
	}
	key = line[:i]
	val = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[i+1:]), "="))
	// 引号内的内容整体算值（路径带空格时用得上）。
	if strings.HasPrefix(val, `"`) {
		if j := strings.Index(val[1:], `"`); j >= 0 {
			val = val[1 : 1+j]
		} else {
			val = val[1:]
		}
	} else if i := strings.Index(val, " #"); i >= 0 {
		val = strings.TrimSpace(val[:i])
	}
	return key, val, val != ""
}

// ---------- 诊断 ----------

// Step 是一次探测里的一步。
type Step struct {
	Name  string
	State string // ok / warn / bad（与 cli 那套语气同一份词汇）
	Line  string
}

// Probe 真拨一次，并把每一步的结论报出来。
//
// 与 Dial 的区别：它**不只回答成败**，它回答卡在哪一步。`newgate tunnel test` 是
// 用户在「连不上」时唯一能自己跑的诊断，而它最有用的输出是「DNS 解析到 X、TCP 通、
// 认证被拒」——不是一个红叉。
//
// 名字不叫 Test：这个包里所有大写 Test 开头的东西都会被读成测试函数。
func Probe(ctx context.Context, t Target, d Dialer) []Step {
	t = t.normalize()
	var steps []Step
	add := func(name, state, line string) {
		steps = append(steps, Step{Name: name, State: state, Line: line})
	}

	real, isReal := d.(*sshDialer)
	user := t.User
	host := t.Host
	port := t.Port
	if isReal {
		r := real.resolve(t)
		host, port, user = r.Host, r.Port, r.User
		add("resolve", "ok", i18n.T("{host}:{port} as {user}", i18n.A{
			"host": host, "port": port, "user": user}))
		if r.Identity != "" {
			add("identity", "ok", i18n.T("key file {path}", i18n.A{"path": r.Identity}))
		} else if keys := real.keyPaths(r); len(keys) == 0 {
			add("identity", "warn", i18n.T("no key file found in {dir} — relying on ssh-agent",
				i18n.A{"dir": real.sshDir()}))
		} else {
			add("identity", "ok", i18n.T("keys {list}", i18n.A{"list": strings.Join(keys, ", ")}))
		}
		// DNS 单独一步：解析不了与连不上是两件事，而它们的下一步动作完全不同。
		if _, err := net.DefaultResolver.LookupHost(ctx, host); err != nil {
			add("dns", "bad", i18n.T("cannot resolve {host}: {err}", i18n.A{"host": host, "err": err}))
			return steps
		}
		add("dns", "ok", i18n.T("{host} resolves", i18n.A{"host": host}))
	}

	sess, err := d.Dial(ctx, t)
	if err != nil {
		add("ssh", "bad", err.Error())
		return steps
	}
	defer sess.Close()
	add("ssh", "ok", i18n.T("handshake and authentication succeeded", nil))

	// 最后一跳：从远端那台机器去看 webui 的端口。这一步能区分「SSH 通了」与
	// 「SSH 通了但远端那个 webui 不在」——后者是最常见的一种，而它在上一步
	// 完全看不出来。
	conn, err := sess.Dial("tcp", t.RemoteAddr())
	if err != nil {
		add("remote", "bad", i18n.T("SSH is fine, but {remote} refuses connections from this "+
			"side: {err}", i18n.A{"remote": t.RemoteAddr(), "err": err}))
		return steps
	}
	_ = conn.Close()
	add("remote", "ok", i18n.T("{remote} accepts connections", i18n.A{"remote": t.RemoteAddr()}))
	return steps
}
