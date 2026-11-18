package probe

import (
	"context"
	"crypto/tls"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// NetCheck 逐层探测到某个 provider 的连通性。
// 目的是把「Bad Gateway」这种糊成一团的失败拆开：
// 到底是 DNS 挂了、TCP 连不上、TLS 握手失败，还是鉴权被拒。
type NetCheck struct {
	Provider string
	Host     string
	Steps    []Step
}

type Step struct {
	Name    string
	OK      bool
	Detail  string
	Latency time.Duration
}

func CheckNet(name string, p domain.Provider, timeout time.Duration) NetCheck {
	nc := NetCheck{Provider: name}

	u, err := url.Parse(p.BaseURL)
	if err != nil || u.Host == "" {
		nc.Steps = append(nc.Steps, Step{i18n.T("parse base_url", nil), false,
			i18n.T("{url} is not a valid URL", i18n.A{"url": p.BaseURL}), 0})
		return nc
	}
	nc.Host = u.Host

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	// 1. DNS —— 静态编译（CGO_ENABLED=0）用纯 Go 解析器，
	//    和 glibc 的行为不同，某些机器上会在这一步挂
	t0 := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	cancel()
	d := time.Since(t0)
	if err != nil {
		nc.Steps = append(nc.Steps, Step{i18n.T("DNS resolution", nil), false,
			i18n.T("{err}\n      ↳ the static build uses the pure Go resolver; if the dynamic build resolves but this one does not, the host DNS config is non-standard (/etc/nsswitch.conf lists sss/mdns or similar)", i18n.A{"err": err}), d})
		return nc
	}
	if ip := net.ParseIP(host); ip != nil {
		nc.Steps = append(nc.Steps, Step{i18n.T("DNS resolution", nil), true,
			i18n.T("(already an IP, no lookup needed)", nil), 0})
	} else {
		nc.Steps = append(nc.Steps, Step{i18n.T("DNS resolution", nil), true, strings.Join(ips, ", "), d})
	}

	// 2. TCP
	addr := net.JoinHostPort(host, port)
	t0 = time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	d = time.Since(t0)
	if err != nil {
		nc.Steps = append(nc.Steps, Step{i18n.T("TCP connection", nil), false,
			i18n.T("{err}\n      ↳ the port may be blocked; an outbound proxy may be required (newgate does not read HTTPS_PROXY today)", i18n.A{"err": err}), d})
		return nc
	}
	nc.Steps = append(nc.Steps, Step{i18n.T("TCP connection", nil), true, addr, d})

	// 3. TLS
	if u.Scheme == "https" {
		t0 = time.Now()
		tc := tls.Client(conn, &tls.Config{ServerName: host})
		_ = tc.SetDeadline(time.Now().Add(timeout))
		err = tc.Handshake()
		d = time.Since(t0)
		if err != nil {
			nc.Steps = append(nc.Steps, Step{i18n.T("TLS handshake", nil), false,
				i18n.T("{err}\n      ↳ certificate problem / MITM proxy in the middle / wrong clock",
					i18n.A{"err": err}), d})
			_ = conn.Close()
			return nc
		}
		nc.Steps = append(nc.Steps, Step{i18n.T("TLS handshake", nil), true,
			tc.ConnectionState().PeerCertificates[0].Subject.CommonName, d})
		_ = tc.Close()
	} else {
		_ = conn.Close()
	}

	// 4. 鉴权 + 模型可用性
	if p.Key() == "" {
		nc.Steps = append(nc.Steps, Step{i18n.T("auth", nil), false,
			i18n.T("no api_key; the actual request is skipped", nil), 0})
		return nc
	}
	return nc
}

// CheckNetAll 对所有 provider 各跑一遍分层探测。
func CheckNetAll(timeout time.Duration) ([]NetCheck, error) {
	provs, err := store.LoadProviders()
	if err != nil {
		return nil, err
	}
	var names []string
	for n := range provs.Providers {
		names = append(names, n)
	}
	sortStrings(names)

	// 同一个 host 只探一次，多个 provider 共用一个网关很常见
	byHost := map[string]NetCheck{}
	var out []NetCheck
	for _, n := range names {
		p := provs.Providers[n]
		if u, err := url.Parse(p.BaseURL); err == nil && u.Host != "" {
			if prev, ok := byHost[u.Host]; ok {
				c := prev
				c.Provider = n
				out = append(out, c)
				continue
			}
		}
		c := CheckNet(n, p, timeout)
		if c.Host != "" {
			byHost[c.Host] = c
		}
		out = append(out, c)
	}
	return out, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
