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

// Session is an established SSH connection one can ask for channels on.
//
// It is an **interface**, for one reason that suffices: tests need no real sshd. A fake
// with net.Pipe verifies **this module's own** behavior (dial on demand, reuse, idle
// close) cleanly; "really connects to OpenSSH" is another thing, locked by the real-sshd
// e2e (see mock/e2e_tunnel.sh). The former runs on every commit, the latter locally.
type Session interface {
	// Dial opens a channel to addr on the connection (like ssh.Client.Dial).
	Dial(network, addr string) (net.Conn, error)
	// SendRequest forwards to ssh.Client.SendRequest: keepalive needs it.
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
	Close() error
}

// Dialer dials one SSH connection to the target machine.
type Dialer interface {
	Dial(ctx context.Context, t Target) (Session, error)
}

// ---------- the real implementation ----------

// sshDialer is all the dialing knowledge: how to authenticate, whether to trust the
// machine, how to wire up the conventions found in the home directory.
type sshDialer struct {
	home string
	// agentFn is a seam for tests (the real path uses SSH_AUTH_SOCK). nil = look in the env.
	agentFn func() ([]ssh.Signer, error)

	mu sync.Mutex
	// loggedKeys remembers known-host writes already logged, so each connection does not spam.
	loggedKeys map[string]bool

	cfgOnce sync.Once
	cfg     map[string]sshAlias

	// handshake is the cap on "wait for the handshake after TCP connects". 0 = handshakeBudget.
	// A field for one reason: **tests must verify this in hundreds of ms** - 20s is right
	// in real use, a disaster in a per-commit test (measured: one 20s test, and it checks
	// a time cap, not that particular number).
	handshake time.Duration
}

// handshakeOr is the handshake deadline for this dial.
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

// Dial establishes one connection.
//
// Every step's failure comes back with **which step**: DNS unresolved, TCP refused,
// handshake rejected, bad key, known_hosts unknown - five very different things to a
// user, each with its own remedy (address / firewall / sshd logs / key / ssh once).
// One "connection failed" pushes the whole debugging cost onto the user.
func (d *sshDialer) Dial(ctx context.Context, t Target) (Session, error) {
	client, err := d.dialOnce(ctx, t.normalize())
	if err != nil {
		return nil, err
	}
	return &sshSession{client: client}, nil
}

// sshSession wraps *ssh.Client as a Session.
type sshSession struct{ client *ssh.Client }

func (s *sshSession) Dial(network, addr string) (net.Conn, error) {
	return s.client.Dial(network, addr)
}

func (s *sshSession) SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error) {
	return s.client.SendRequest(name, wantReply, payload)
}

func (s *sshSession) Close() error { return s.client.Close() }

// resolved is the set of values after merging in ~/.ssh/config.
type resolved struct {
	Host     string
	Port     int
	User     string
	Identity string
	// Notices are ssh_config keys we **do not know but that affect connectivity**, reported as is.
	Notices []string
}

// resolve merges the config's fields with ~/.ssh/config into the values really used.
//
// Order is ssh's own: **explicit values beat ssh_config**. There is no "command line"
// layer here (config lives in state.json), so the criterion is "was it written in state.json".
func (d *sshDialer) resolve(t Target) resolved {
	r := resolved{
		Host: t.Host, Port: t.Port, User: t.User,
		Identity: expandHome(t.Identity, d.home),
	}
	alias, ok := d.sshConfig()[t.Host]
	if !ok {
		return r
	}
	// When the user wrote an alias like `snode1`, HostName is the real address. Writing a
	// real address usually finds no alias entry, so this is not reached.
	if alias.HostName != "" {
		r.Host = alias.HostName
	}
	// Adopted only when the user **did not write a port explicitly**. 22 is the default anyway.
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

// auth is the authentication to try this time.
//
// ssh's old rule: key files first, agent last. Key files first because they are more
// specific - the user named a key explicitly, so try it first.
func (d *sshDialer) auth(r resolved) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	for _, path := range d.keyPaths(r) {
		signer, err := readSigner(path)
		if err != nil {
			// The file exists but is unusable (bad perms / passphrase / not a key): the user
			// should know, never swallowed - else "a key is configured, it says none available".
			return nil, i18n.E("cannot use the private key {path}: {err}",
				i18n.A{"path": path, "err": err})
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if signers, err := d.agentSigners(); err == nil && len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	if len(methods) == 0 {
		// An empty list to ssh means "no auth methods"; the user does not know what to do next.
		return nil, i18n.E("no usable key for {host}: look in {dir} or start ssh-agent "+
			"(passwords are not supported — this module only does keys and the agent)",
			i18n.A{"host": r.Host, "dir": d.sshDir()})
	}
	return methods, nil
}

// keyPaths is the identity files to try in order this time.
//
// Given explicitly, try **only** that one: the user named a key, do not try others - a
// different one succeeding is not what they wanted. Not given: try those in ~/.ssh, ssh's order.
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

// knownHostsPath is the known_hosts to read and write this time.
func (d *sshDialer) knownHostsPath() string { return filepath.Join(d.sshDir(), "known_hosts") }

// agentSigners finds keys in the agent.
//
// Via SSH_AUTH_SOCK (ssh's own convention). No agent is not an error - those without
// an agent should use key files, which the caller already tried first.
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
	// **Do not close** this connection: the agent's signers connect on demand to sign, and
	// closing breaks signing. One per target (not per request), living with the process.
	return agent.NewClient(conn).Signers()
}

// hostKeyCallback decides whether to trust this machine.
func (d *sshDialer) hostKeyCallback(t Target) (ssh.HostKeyCallback, error) {
	known := d.knownHostsPath()
	base, err := knownhosts.New(known)
	if err != nil && !os.IsNotExist(err) {
		return nil, i18n.E("cannot read {path}: {err}", i18n.A{"path": known, "err": err})
	}
	if err != nil {
		// A missing file is **a fresh machine**'s normal state, not an error. Build an
		// "nobody has seen it" criterion so the layers below work (strict refuses, accept-new records).
		base = unknownHost
	}

	switch t.HostKey {
	case HostKeyAcceptNew:
		return d.acceptNew(known, base), nil
	default:
		// strict: refuse if unknown. The message must give **the next step** - "host key
		// verification failed" alone makes users disable verification, which is worse.
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

// unknownHost is the criterion for "nobody in known_hosts has seen it".
func unknownHost(hostname string, remote net.Addr, key ssh.PublicKey) error {
	return &knownhosts.KeyError{Want: nil}
}

// acceptNew is accept-new: record on first sight, must match afterwards.
//
// Log every time: **trusting a machine is an event**, not an internal state change. On
// first connect users should see "I saved this machine's key", or they never get a
// chance to notice "I thought I was connecting to A, but it was B".
func (d *sshDialer) acceptNew(known string, base ssh.HostKeyCallback) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := base(hostname, remote, key)
		if err == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		if !errors.As(err, &ke) || len(ke.Want) != 0 {
			// Present but mismatched: **do not overwrite**. accept-new means "record if unseen",
			// not "accept changes" - the latter is the most dangerous silent downgrade (TOFU revoked).
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

// appendKnownHost appends one line to known_hosts (in knownhosts.Line's format).
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

// readSigner reads one private key.
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
		// A passphrase-protected key would prompt, but we are a daemon with no terminal.
		// Say which way to go (the agent) rather than hang in the background forever.
		return nil, i18n.E("it is protected by a passphrase; add it to ssh-agent "+
			"(`ssh-add {path}`) and leave identity empty", i18n.A{"path": path})
	}
	return nil, err
}

// dialOnce really dials once, tagging each step's failure.
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
		// Total budget for handshake and auth. **This field only works via ssh.Dial** - we use
		// ssh.NewClientConn (to attach the context and our dialer), where it is ignored. What
		// really guards a "black hole" is the conn deadline below; it stays so that "the
		// reader thinks it works" does not hold - see the handshakeBudget note.
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
	// The handshake needs a deadline. A machine that accepts TCP but never sends a banner
	// (a firewall's black-hole mode, or a stuck sshd) hangs the request forever - the
	// browser just shows a spinner. ClientConfig.Timeout does not work here (see above),
	// so the deadline goes on conn and is cleared after: **it is a long-lived connection
	// then; with a deadline it reconnects every few minutes**.
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

// handshakeBudget caps "after TCP connects, wait for the SSH handshake to finish".
//
// 20s: key ops are tens of ms on a LAN, hundreds across the internet; 20s tolerates one
// slow DNS/route while short enough for the user to see a clear error before the browser
// times out ("whoever fails first can give the reason" is the whole point here).
const handshakeBudget = 20 * time.Second

func currentUserName() string {
	for _, k := range []string{"USER", "LOGNAME"} {
		if u := os.Getenv(k); u != "" {
			return u
		}
	}
	return "root"
}

// expandHome expands ~. **Only bare ~ and ~/ prefixes**, never ~other - that needs
// passwd, and its odds in config are far below "one wrong char that looks right".
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

// sshAlias is the keys we recognize inside one ssh_config Host block.
type sshAlias struct {
	HostName      string
	Port          int
	User          string
	IdentityFiles []string
	// Notices are unrecognized keys that affect connectivity (ProxyJump / ProxyCommand / Match...).
	Notices []string
}

// loudUnknown is the keys that **must be reported**: they all mean "not a direct connection",
// and we only do direct. Ignoring them silently makes the user think they go through a
// jump host while connecting directly to an unreachable address - which looks like a network fault.
var loudUnknown = []string{"ProxyJump", "ProxyCommand", "Match", "LocalCommand", "RemoteCommand"}

// sshConfig reads ~/.ssh/config, indexing by Host.
//
// A hand-written parser instead of a third party: we know four keys, and this file's
// format (Key Value, case-insensitive keys, # comments, several patterns per Host) fits
// in a few sentences. One more dependency to "read a text file" is not worth it.
//
// **Cached** (sync.Once): called per connection and per snapshot. The cost is that
// ssh_config edits need a daemon restart - stated in the README, or it is a silent
// "I changed it and nothing happened".
func (d *sshDialer) sshConfig() map[string]sshAlias {
	d.cfgOnce.Do(func() { d.cfg = parseSSHConfig(filepath.Join(d.sshDir(), "config")) })
	return d.cfg
}

// parseSSHConfig is that parser, split out so it can be unit-tested (give it a file path).
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
				// Wildcard blocks (including `Host *`) are meant for defaults, and their
				// inheritance rules (first wins, Host * last) are more complex than this parser
				// will take on. **Not implemented** rather than half-implemented - the half-done
				// error is "some machines silently use `Host *`'s User", and it looks normal.
				continue
			}
			if existing, ok := out[p]; ok {
				// A later block **fills** keys the earlier one lacks (ssh semantics: first
				// occurrence wins, "do not overwrite what exists" is the same effect).
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

// splitDirective splits one `Key Value` line (quotes maybe, trailing comments maybe).
func splitDirective(line string) (key, val string, ok bool) {
	i := strings.IndexAny(line, " \t=")
	if i <= 0 {
		return "", "", false
	}
	key = line[:i]
	val = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[i+1:]), "="))
	// Content inside quotes counts whole (useful for paths with spaces).
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

// ---------- diagnostics ----------

// Step is one step of a probe.
type Step struct {
	Name  string
	State string // ok / warn / bad（与 cli 那套语气同一份词汇）
	Line  string
}

// Probe really dials once and reports every step's conclusion.
//
// Difference from Dial: it **does not just answer success/failure**, it says where it
// stopped. `newgate tunnel test` is the one diagnostic users can run on "cannot connect",
// and its most useful output is "DNS resolved to X, TCP open, auth rejected" - not a cross.
//
// Not named Test: any capitalized Test prefix in this package reads as a test function.
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
		// DNS is its own step: unresolved and refused are two things with different next actions.
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

	// Last hop: from the remote machine, check the webui port. This separates "SSH works"
	// from "SSH works but the remote webui is not there" - the most common one, invisible
	// in the previous step.
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
