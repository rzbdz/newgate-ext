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

// This one uses **real SSH** (one x/crypto playing both client and server), over real
// TCP, with real keys, a real known_hosts, real direct-tcpip channels.
//
// # What gap it fills
//
// mock/e2e_tunnel.sh verifies the same thing with **real OpenSSH** (local sshd), closer
// to the scene - but it cannot run in CI (needs sshd and ports), so on a day nobody runs
// it by hand, no line of this chain is verified. This fills that gap in CI: protocol
// version, auth method, channel request shape, known_hosts format - four mismatches that
// happen while **my side looks perfectly right**, each only showing as "cannot connect".
//
// Two layers: this one runs on every commit; the real-sshd one is confirmed by hand.

// echoServer is a fake "remote webui": it **records** every path it receives and
// writes it back verbatim into the body.
//
// Both are needed, and **the body alone will not do**: the path in the body is moved
// once more by our own rewrite (remote returns `/ui/api/snapshot`, the browser gets
// `/ui/remote/<id>/api/snapshot` - exactly this route's job). So "what the remote
// actually received" can only be asked of the record; the body asserts a failed rewrite.
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

// sshd is a real SSH server (the other half of the same library); it does one thing:
// dials direct-tcpip channels through to some local address - sshd's AllowTcpForwarding.
type sshd struct {
	addr string
	// client is the private key for the public key this sshd **accepts** (the client
	// authenticates with it). Both stay: private key to a file, public key to known_hosts.
	client    ed25519.PrivateKey
	clientPub ssh.PublicKey
	ln        net.Listener
	done      chan struct{}
}

func newSSHD(t *testing.T, _ string) *sshd {
	t.Helper()

	// The host key and the server public key are both one-off ed25519 (one pair per test).
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
					// The payload is the direct-tcpip request struct. **Dial the address it names** -
					// this matters: ignore the address and the whole class of "SSH works but nothing
					// listens on that remote port" fails can never show up here (the most common one).
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

// newKey makes an ed25519 pair; the private key becomes an ssh PEM (the client path reads files).
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

// writeKeyFile puts the private key on disk (sshDialer takes the "read a file" path).
//
// Written as PKCS#8 PEM: x/crypto's ParsePrivateKey accepts it, and **this test is
// about the "read a real key file" part** - stuffing a Signer into memory skips it.
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

// ---------- three paths over real SSH ----------

// newRealDialer builds a real sshDialer at the sandbox HOME (keys and known_hosts live there).
func newRealDialer(t *testing.T, home string) *sshDialer {
	t.Helper()
	return &sshDialer{home: home, loggedKeys: map[string]bool{}}
}

// realTarget points one target at that test sshd.
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
	// accept-new: this path must also really run "record it on first sight"
	// (a wrong known_hosts format only shows up at this step).
	tt.HostKey = HostKeyAcceptNew

	d := newRealDialer(t, home)
	p := newPool(d)
	px := newProxy(p, Load)
	loaded := Loaded{Targets: []Target{tt}}
	px.loaded = func() Loaded { return loaded }
	defer func() {
		// Same order as production shutdown: drop idle conns (hold leases), then close the pool.
		px.closeIdle()
		closePool(t, p)
	}()
	h := http.StripPrefix(Prefix, px)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", Prefix+"/real/api/snapshot", nil)
	req.Host = "127.0.0.1:8899"
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	// **The remote receives /ui/...**: after stripping our route prefix, the remote's own
	// absolute prefix goes on - one wrong character in the mapping and the symptom is a 404.
	if seen := remote.seen(); len(seen) != 1 || seen[0] != "/ui/api/snapshot" {
		t.Errorf("the remote received path %v, want [/ui/api/snapshot]", seen)
	}
	// And **what the browser gets is rewritten back**: the `/ui/...` the remote wrote
	// into the body must become an address on this route, or the browser asks the local machine.
	if got := rec.Body.String(); !strings.Contains(got, ">/ui/remote/real/api/snapshot<") {
		t.Errorf("the absolute prefix in the body was not rewritten: %s", got)
	}
	// accept-new really wrote into known_hosts.
	kb, err := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts"))
	if err != nil {
		t.Fatalf("accept-new did not write known_hosts: %v", err)
	}
	if !strings.Contains(string(kb), ":"+strconv.Itoa(tt.Port)+" ") {
		t.Errorf("known_hosts does not contain this machine: %s", kb)
	}
	// The line written must **read back through knownhosts itself** - with a bad format
	// the next connection ends in "host key mismatch", which looks like a man in the middle.
	if _, err := knownhosts.New(filepath.Join(home, ".ssh", "known_hosts")); err != nil {
		t.Errorf("the known_hosts just written cannot be read back: %v", err)
	}
}

func TestARealSSHConnectionCarriesTheForwardingPort(t *testing.T) {
	// Route one (the local forward port) also goes over real SSH: its only difference
	// from the route path is **that the path is not touched by a single byte** - its reason to exist.
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
	// Check both, because "forwarding does not change paths" is **two-way**: what goes
	// out is verbatim, what comes back is verbatim (no rewrite here, so /ui/assets/app.js stays).
	if seen := remote.seen(); len(seen) != 1 || seen[0] != "/ui/assets/app.js" {
		t.Errorf("the remote received path %v, want [/ui/assets/app.js]", seen)
	}
	if !strings.Contains(string(body), ">/ui/assets/app.js<") {
		t.Errorf("the body that came back was changed: %s", body)
	}
}

func TestStrictRefusesAMachineItHasNeverSeen(t *testing.T) {
	// The default is strict, and the strict failure text must offer **a next step** - say
	// only "host key verification failed" and the user turns verification off, which is worse.
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
		t.Fatal("the first connection under strict should be refused")
	}
	msg := err.Error()
	for _, want := range []string{"not in", "ssh ", "accept-new"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal reason has no %q, so the user does not know what to do next: %s", want, msg)
		}
	}
	// And it did **not** quietly record it.
	if _, err := os.Stat(filepath.Join(home, ".ssh", "known_hosts")); err == nil {
		t.Error("strict refused but wrote known_hosts anyway")
	}
}

func TestAChangedHostKeyIsAlwaysRefused(t *testing.T) {
	// accept-new means "record what was never seen", **not** "accept a change". The
	// latter is the most dangerous silent downgrade: drop TOFU and any swapped key passes.
	remote := newEchoServer(t)
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	_ = os.MkdirAll(sshDir, 0o700)
	s := newSSHD(t, remote.addr())
	key := writeKeyFile(t, home, s.client)
	tt := realTarget(s, key, remote.addr())
	tt.HostKey = HostKeyAcceptNew

	// Pre-populate known_hosts with a **different** key (as if the machine was replaced).
	otherPub, _ := newKey(t)
	line := knownhosts.Line([]string{knownhosts.Normalize(net.JoinHostPort(tt.Host, strconv.Itoa(tt.Port)))}, otherPub)
	if err := os.WriteFile(filepath.Join(sshDir, "known_hosts"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	d := newRealDialer(t, home)
	if _, err := d.Dial(context.Background(), tt); err == nil {
		t.Fatal("it connected even though the host key changed - accept-new was treated as 'accept changes too'")
	}
	// And the new one was **not** written over it.
	kb, err := os.ReadFile(filepath.Join(sshDir, "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(kb), string(s.clientPub.Marshal())) {
		t.Error("it wrote the new key in when they did not match")
	}
}

func TestABrokenConfigReportsWhereItStuck(t *testing.T) {
	// A **successful** SSH and an "SSH works but nothing listens on that port" are two
	// different things, and the next action differs. Probe's whole value is this.
	remote := newEchoServer(t)
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	s := newSSHD(t, remote.addr())
	key := writeKeyFile(t, home, s.client)

	tt := realTarget(s, key, remote.addr())
	tt.HostKey = HostKeyAcceptNew
	// Points at a port nobody listens on (the forward target that was shut down).
	tt.RemotePort = freePort(t)

	d := newRealDialer(t, home)
	steps := Probe(context.Background(), tt, d)
	if len(steps) == 0 {
		t.Fatal("no steps at all")
	}
	last := steps[len(steps)-1]
	if last.State != "bad" {
		t.Fatalf("the last hop should be the broken one, got %+v", steps)
	}
	if last.Name != "remote" {
		t.Errorf("the stuck step is %q, want remote (SSH itself works)", last.Name)
	}
	// Every earlier step must be fine - else that assertion above is testing some other failure.
	for _, st := range steps[:len(steps)-1] {
		if st.State == "bad" {
			t.Errorf("this step must not be broken: %+v", st)
		}
	}
}

func TestABlackHoleHostDoesNotHangForever(t *testing.T) {
	// A machine whose TCP connects but never sends a banner (a firewall black hole, or a
	// stuck sshd). Without a deadline this hangs forever - **and on the browser side it just
	// looks like "spinning"**, with no clue where it is stuck.
	//
	// This test was written on exactly this bug: ClientConfig.Timeout **does nothing** on
	// the ssh.NewClientConn path (only ssh.Dial uses it), so the deadline must be on conn.
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
			// Accept, then stay silent (and do not close - closing would return our read at once).
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
	// Squeeze the deadline to 300ms: what is verified here is "**a** deadline exists and
	// it speaks first", not the number. With the real value this test takes 20 seconds,
	// and 20 seconds in a test that runs on every commit is a disaster.
	budget := 300 * time.Millisecond
	d.handshake = budget
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = d.Dial(ctx, tt)
	if err == nil {
		t.Fatal("it actually connected to the black-hole machine")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("it took %v to fail - the handshake deadline did not take effect (the outer context gives 30 seconds, so that is not it)", elapsed)
	}
}
