package sshtunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// 这一条用**真的 SSH**（同一份 x/crypto 同时扮客户端与服务端），跑在真的 TCP 上，
// 用真的密钥、真的 known_hosts、真的 direct-tcpip 通道。
//
// # 它补的是哪一块
//
// mock/e2e_tunnel.sh 用**真的 OpenSSH**（本机 sshd）验同一件事，比这里更接近现场
// ——但它进不了 CI（要 sshd、要端口），所以在一个没人手动跑它的日子里，这条链路上
// 的每一行都没人验过。这一条是那个缺口在 CI 里的填充：协议版本对不上、认证方式
// 对不上、通道请求的形状对不上、known_hosts 的格式对不上——这四类错都在**我这边
// 看起来完全正确**的前提下发生，而它们各自的症状都只是「连不上」。
//
// 两层都在，各管一段：这里每次提交都跑；真 sshd 那条在开发机上手工确认。

// echoServer 是一台假的「远端 webui」：它**记下**收到的每个路径，并把它原样
// 写回正文里。
//
// 两件事都要，而且**不能只看正文**：正文里那个路径会被我们自己的改写再动一次
// （远端返回 `/ui/api/snapshot`，浏览器拿到的是 `/ui/remote/<id>/api/snapshot`
// ——那正是这条 route 该干的事）。所以「远端到底收到了什么」只能问记录，
// 问正文等于在断言改写没生效。
type echoServer struct {
	srv   *httptest.Server
	mu    chan struct{}
	paths []string
}

func newEchoServer(t *testing.T) *echoServer {
	t.Helper()
	e := &echoServer{mu: make(chan struct{}, 1)}
	e.mu <- struct{}{}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-e.mu
		e.paths = append(e.paths, r.URL.Path)
		e.mu <- struct{}{}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<p id=hit>"+r.URL.Path+"</p>")
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *echoServer) addr() string { return strings.TrimPrefix(e.srv.URL, "http://") }

func (e *echoServer) seen() []string {
	<-e.mu
	defer func() { e.mu <- struct{}{} }()
	return append([]string(nil), e.paths...)
}

// sshd 是一台真的 SSH 服务端（同一份库的另一半），它只做一件事：
// 把 direct-tcpip 通道接到本机的某个地址上——也就是 sshd 的 AllowTcpForwarding。
type sshd struct {
	addr string
	// client 是这台 sshd **接受**的那把公钥对应的私钥（客户端要用它认证）。
	// 两个都留着：私钥写进文件给客户端读，公钥用来写 known_hosts 的期望值。
	client    ed25519.PrivateKey
	clientPub ssh.PublicKey
	ln        net.Listener
	done      chan struct{}
}

func newSSHD(t *testing.T, _ string) *sshd {
	t.Helper()

	// 主机密钥与服务端公钥都用一次性的 ed25519（每个测试各一对）。
	_, hostPriv := newKey(t)
	clientPub, clientPriv := newKey(t)

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(clientPub.Marshal()) {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	signer, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &sshd{addr: ln.Addr().String(), client: clientPriv, clientPub: clientPub, ln: ln, done: make(chan struct{})}
	go s.serve(cfg)
	t.Cleanup(func() {
		_ = ln.Close()
		<-s.done
	})
	return s
}

func (s *sshd) serve(cfg *ssh.ServerConfig) {
	defer close(s.done)
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
			if err != nil {
				_ = conn.Close()
				return
			}
			defer sc.Close()
			go ssh.DiscardRequests(reqs)
			for ch := range chans {
				if ch.ChannelType() != "direct-tcpip" {
					_ = ch.Reject(ssh.UnknownChannelType, "only direct-tcpip here")
					continue
				}
				go func(ch ssh.NewChannel) {
					// 载荷是 direct-tcpip 的请求结构。**照它说的地址拨**——这一点
					// 很重要：无视地址的话，「SSH 通了但远端那个端口没人听」这一
					// 整类失败在这条测试里就永远测不出来（而那是最常见的一种）。
					var req struct {
						Host       string
						Port       uint32
						OriginHost string
						OriginPort uint32
					}
					if err := ssh.Unmarshal(ch.ExtraData(), &req); err != nil {
						_ = ch.Reject(ssh.ConnectionFailed, "bad payload")
						return
					}
					upstream, err := net.Dial("tcp", net.JoinHostPort(req.Host, strconv.Itoa(int(req.Port))))
					if err != nil {
						_ = ch.Reject(ssh.ConnectionFailed, err.Error())
						return
					}
					channel, reqs, err := ch.Accept()
					if err != nil {
						_ = upstream.Close()
						return
					}
					go ssh.DiscardRequests(reqs)
					go func() { _, _ = io.Copy(channel, upstream); _ = channel.Close() }()
					_, _ = io.Copy(upstream, channel)
					_ = upstream.Close()
				}(ch)
			}
		}()
	}
}

// newKey 造一对 ed25519 密钥，并把私钥写成 ssh 的 PEM（客户端那条路读的是文件）。
func newKey(t *testing.T) (ssh.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return sshPub, priv
}

// writeKeyFile 把私钥落到盘上（sshDialer 走的是「读文件」那条路）。
//
// 写成 PKCS#8 PEM：x/crypto 的 ParsePrivateKey 认它，而**这条测试要验的正是
// 「读一个真的密钥文件」那一段**——直接在内存里塞一个 Signer 就绕开了它。
func writeKeyFile(t *testing.T, dir string, priv ed25519.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	path := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(path, block, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---------- 真 SSH 上的三条路 ----------

// newRealDialer 造一个真的 sshDialer，指向沙箱 HOME（密钥与 known_hosts 都在那里）。
func newRealDialer(t *testing.T, home string) *sshDialer {
	t.Helper()
	return &sshDialer{home: home, loggedKeys: map[string]bool{}}
}

// realTarget 把一条 target 指向那台测试 sshd。
func realTarget(s *sshd, keyPath string, forwardTo string) Target {
	host, port, _ := net.SplitHostPort(s.addr)
	p, _ := strconv.Atoi(port)
	remoteHost, remotePort, _ := net.SplitHostPort(forwardTo)
	rp, _ := strconv.Atoi(remotePort)
	return Target{
		ID: "real", Host: host, Port: p, Identity: keyPath,
		RemoteHost: remoteHost, RemotePort: rp,
	}.normalize()
}

func TestARealSSHConnectionCarriesTheRoute(t *testing.T) {
	remote := newEchoServer(t)
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	s := newSSHD(t, remote.addr())
	key := writeKeyFile(t, home, s.client)

	tt := realTarget(s, key, remote.addr())
	// accept-new：这条链路上要把「第一次见到就记下来」也真的走一遍
	// （known_hosts 的格式错了只会在这一步露出来）。
	tt.HostKey = HostKeyAcceptNew

	d := newRealDialer(t, home)
	p := newPool(d)
	px := newProxy(p, Load)
	loaded := Loaded{Targets: []Target{tt}}
	px.loaded = func() Loaded { return loaded }
	defer func() {
		// 顺序与生产停机一致：先放掉空闲连接（它们占着借约），再关池。
		px.closeIdle()
		closePool(t, p)
	}()
	h := http.StripPrefix(Prefix, px)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", Prefix+"/real/api/snapshot", nil)
	req.Host = "127.0.0.1:8899"
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态 %d，正文 %s", rec.Code, rec.Body.String())
	}
	// **远端收到的是 /ui/…**：剥掉我们这条 route 的前缀之后，接上远端自己的
	// 绝对前缀——两边的对应关系错一个字符，症状都是远端 404。
	if seen := remote.seen(); len(seen) != 1 || seen[0] != "/ui/api/snapshot" {
		t.Errorf("远端收到的路径是 %v，想要 [/ui/api/snapshot]", seen)
	}
	// 而**浏览器拿到的那份被改了回来**：远端写在正文里的 `/ui/...` 必须变成
	// 这条 route 上的地址，否则浏览器会去本机要那个资源。
	if got := rec.Body.String(); !strings.Contains(got, ">/ui/remote/real/api/snapshot<") {
		t.Errorf("正文里的绝对前缀没有被改写：%s", got)
	}
	// accept-new 真的写进了 known_hosts。
	kb, err := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts"))
	if err != nil {
		t.Fatalf("accept-new 没有写 known_hosts：%v", err)
	}
	if !strings.Contains(string(kb), ":"+strconv.Itoa(tt.Port)+" ") {
		t.Errorf("known_hosts 里没有这台机器：%s", kb)
	}
	// 写进去的那一行必须**能被 knownhosts 自己读回来**——格式错了的话，下一次
	// 连接会以「主机密钥对不上」告终，而那看起来像有人中间人。
	if _, err := knownhosts.New(filepath.Join(home, ".ssh", "known_hosts")); err != nil {
		t.Errorf("写下去的 known_hosts 读不回来：%v", err)
	}
}

func TestARealSSHConnectionCarriesTheForwardingPort(t *testing.T) {
	// 路线一（本地转发端口）也走一次真的 SSH：它与 route 那条唯一的区别是
	// **路径一个字节都不动**，而那正是它存在的理由。
	remote := newEchoServer(t)
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	s := newSSHD(t, remote.addr())
	key := writeKeyFile(t, home, s.client)

	tt := realTarget(s, key, remote.addr())
	tt.HostKey = HostKeyAcceptNew
	tt.LocalPort = freePort(t)

	d := newRealDialer(t, home)
	p := newPool(d)
	defer closePool(t, p)

	f, err := startForward(tt, p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		_ = f.close(ctx)
	}()

	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(tt.LocalPort) + "/ui/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// 两条都验，因为「转发不改路径」是**双向**的：发出去的那份原样，回来的那份
	// 也原样（这条路上没有改写，所以正文里的 /ui/assets/app.js 就该原样回来）。
	if seen := remote.seen(); len(seen) != 1 || seen[0] != "/ui/assets/app.js" {
		t.Errorf("远端收到的路径是 %v，想要 [/ui/assets/app.js]", seen)
	}
	if !strings.Contains(string(body), ">/ui/assets/app.js<") {
		t.Errorf("回来的正文被改动了：%s", body)
	}
}

func TestStrictRefusesAMachineItHasNeverSeen(t *testing.T) {
	// 默认是 strict，而 strict 的失败文案必须给出**下一步**——只说「主机密钥校验
	// 失败」的话，用户的第一反应是关掉校验，那是更坏的选择。
	remote := newEchoServer(t)
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	s := newSSHD(t, remote.addr())
	key := writeKeyFile(t, home, s.client)

	tt := realTarget(s, key, remote.addr())
	tt.HostKey = HostKeyStrict

	d := newRealDialer(t, home)
	_, err := d.Dial(context.Background(), tt)
	if err == nil {
		t.Fatal("strict 下第一次连接该被拒")
	}
	msg := err.Error()
	for _, want := range []string{"not in", "ssh ", "accept-new"} {
		if !strings.Contains(msg, want) {
			t.Errorf("拒绝的理由里没有 %q，用户不知道下一步做什么：%s", want, msg)
		}
	}
	// 而且**没有**偷偷把它记下来。
	if _, err := os.Stat(filepath.Join(home, ".ssh", "known_hosts")); err == nil {
		t.Error("strict 拒绝了却还是写了 known_hosts")
	}
}

func TestAChangedHostKeyIsAlwaysRefused(t *testing.T) {
	// accept-new 的语义是「没见过就记」，**不是**「变了也认」。后者是最危险的那种
	// 静默降级：TOFU 被撤掉之后，中间人换一把钥匙也能过。
	remote := newEchoServer(t)
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	_ = os.MkdirAll(sshDir, 0o700)
	s := newSSHD(t, remote.addr())
	key := writeKeyFile(t, home, s.client)
	tt := realTarget(s, key, remote.addr())
	tt.HostKey = HostKeyAcceptNew

	// 事先在 known_hosts 里放一把**别的**钥匙（换过机器的样子）。
	otherPub, _ := newKey(t)
	line := knownhosts.Line([]string{knownhosts.Normalize(net.JoinHostPort(tt.Host, strconv.Itoa(tt.Port)))}, otherPub)
	if err := os.WriteFile(filepath.Join(sshDir, "known_hosts"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	d := newRealDialer(t, home)
	if _, err := d.Dial(context.Background(), tt); err == nil {
		t.Fatal("主机密钥变了还是连上了——accept-new 被当成了「变了也认」")
	}
	// 而且**没有**把新的那把覆盖进去。
	kb, err := os.ReadFile(filepath.Join(sshDir, "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(kb), string(s.clientPub.Marshal())) {
		t.Error("对不上的时候把新的钥匙写进去了")
	}
}

func TestABrokenConfigReportsWhereItStuck(t *testing.T) {
	// 一次**成功的** SSH 与一次「SSH 通了但远端那个端口没人听」是完全不同的两件事，
	// 而它们的下一步动作也不同。Probe 的价值全在这一点上。
	remote := newEchoServer(t)
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	s := newSSHD(t, remote.addr())
	key := writeKeyFile(t, home, s.client)

	tt := realTarget(s, key, remote.addr())
	tt.HostKey = HostKeyAcceptNew
	// 指一个没人听的端口（关掉的那条转发目标）。
	tt.RemotePort = freePort(t)

	d := newRealDialer(t, home)
	steps := Probe(context.Background(), tt, d)
	if len(steps) == 0 {
		t.Fatal("没有任何步骤")
	}
	last := steps[len(steps)-1]
	if last.State != "bad" {
		t.Fatalf("最后一跳该是坏的，实际 %+v", steps)
	}
	if last.Name != "remote" {
		t.Errorf("卡住的那一步是 %q，想要 remote（SSH 本身是通的）", last.Name)
	}
	// 前面几步都该是好的——不然上面那条断言等于在验别的失败。
	for _, st := range steps[:len(steps)-1] {
		if st.State == "bad" {
			t.Errorf("这一步不该坏：%+v", st)
		}
	}
}

func TestABlackHoleHostDoesNotHangForever(t *testing.T) {
	// 一台 TCP 连得上、但永远不发 banner 的机器（防火墙的「黑洞」模式，或者一个
	// 卡死的 sshd）。没有期限的话这里会一直挂着——**而浏览器那边看起来只是
	// 「一直在转」**，没有任何线索说明卡在哪。
	//
	// 这条测试正是在这个 bug 上写的：ClientConfig.Timeout 在 ssh.NewClientConn
	// 那条路上**完全不起作用**（它只被 ssh.Dial 用），所以期限必须设在 conn 上。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// 收下就不再说话（也不关——关掉反而会让我们这边的 read 立刻返回）。
			_ = c
		}
	}()

	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	_, priv := newKey(t)
	key := writeKeyFile(t, home, priv)

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	tt := Target{ID: "blackhole", Host: host, Port: p, Identity: key}.normalize()

	d := newRealDialer(t, home)
	// 把期限压到 300ms：这里验的是「**有一个**期限，而且是它先说话」，不是
	// 那个具体数字。用真实值的话这一条要跑 20 秒，而每次提交都跑的测试里
	// 20 秒是灾难。
	budget := 300 * time.Millisecond
	d.handshake = budget
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = d.Dial(ctx, tt)
	if err == nil {
		t.Fatal("黑洞机器居然连上了")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("挂了 %v 才失败——握手期限没生效（外层上下文给了 30 秒，所以这不是它）", elapsed)
	}
}
