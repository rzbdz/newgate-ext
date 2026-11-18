package httpx

import (
	"net"
	"net/http"
	"net/url"
	"time"
)

// IsLoopback reports whether host is a local loopback address.
func IsLoopback(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0", "":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// LoopbackAwareTransport bypasses the outbound proxy for loopback addresses
// and lets everything else use the proxy from the environment as usual.
//
// Why it is necessary: cluster/corporate machines often set HTTP_PROXY,
// and Go's http.DefaultTransport uses ProxyFromEnvironment -- so newgate's own requests
// to 127.0.0.1 also get sent to the outbound proxy, which cannot reach local loopback
// and returns 502. The consequence is extremely hard to trace: start's health probe
// fails -> judged "the proxy did not come up" -> the freshly started proxy gets killed in
// response, and in the end ss shows no port and the log has only one line, "listening …".
//
// Nor can the proxy be disabled wholesale: reaching the real
// upstream (api.xxx.com) on such machines often must go through the
// outbound proxy. So the only option is to distinguish by target.
func LoopbackAwareTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = func(req *http.Request) (*url.URL, error) {
		if IsLoopback(req.URL.Hostname()) {
			return nil, nil // 直连，不经代理
		}
		return http.ProxyFromEnvironment(req)
	}
	return t
}

// LocalClient is the client dedicated to talking to the local machine.
func LocalClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: LoopbackAwareTransport()}
}

// TCPAlive probes liveness with a direct TCP dial. It does not use HTTP and is unaffected by any
// proxy settings, making it the most reliable way to tell whether a port is actually listening.
func TCPAlive(host string, port int, timeout time.Duration) bool {
	c, err := net.DialTimeout("tcp",
		net.JoinHostPort(host, itoa(port)), timeout)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
