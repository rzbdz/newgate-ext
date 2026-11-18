package httpx

import (
	"net"
	"net/http"
	"net/url"
	"time"
)

// IsLoopback 判断 host 是不是本机回环。
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

// LoopbackAwareTransport 对回环地址绕过出站代理，其它照旧走环境变量里的代理。
//
// 为什么必须这么做：集群/公司机器上常配 HTTP_PROXY，而 Go 的
// http.DefaultTransport 用 ProxyFromEnvironment——于是 newgate 自己发往
// 127.0.0.1 的请求也会被送去出站代理，代理连不上本机 loopback 就回 502。
// 后果极其难查：start 的探活失败 → 判定"代理没起来" → 反过来把刚起好的
// 代理杀掉，最后 ss 看不到端口，日志里只有一行"监听 …"。
//
// 反过来也不能一刀切禁用代理：访问真实上游（api.xxx.com）在这种机器上
// 往往必须经过出站代理。所以只能按目标区分。
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

// LocalClient 专门用于打本机的客户端。
func LocalClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: LoopbackAwareTransport()}
}

// TCPAlive 直接 TCP 拨号探活。不走 HTTP、不受任何代理设置影响，
// 是判断「端口到底有没有在听」最可靠的手段。
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
