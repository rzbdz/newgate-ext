// Package upstream 是「假上游」：一个进程内冒充 OpenAI/Anthropic 兼容网关的
// httptest 服务，供 Go 集成测试使用。
//
// 为什么有它
//
// 在这之前只有一个 python 写的假上游 mock/fake_upstream.py：要单独起进程、
// 占一个固定端口（mock/e2e_claude.sh 用 18081）、依赖机器上有 python3、只能
// 从 bash 脚本里用，取记录还得走 /__mock/requests 的 HTTP 接口再解析一遍。
// Go 测试想要「一个像真上游那样回话的对象」时只能各自手写 httptest 桩——
// 每个包一份，方言细节（SSE 分块序列、DeepSeek 思考模式的最严口径）谁都不全，
// 于是最该被集成测试锁住的那几条行为反而没人锁。
//
// 本包是那个 python 版的逐条移植：路由、响应体、SSE 块序列、严格口径、控制面
// 全部照搬，只把「起进程 + 固定端口 + HTTP 取记录」换成 httptest + 进程内
// 记录（Requests/Reset）。python 版**继续存在**——bash e2e 还要用它
// （mock/e2e_claude.sh、mock/e2e_reasoning_affinity.sh），本包不动 mock/ 下
// 任何文件；两边行为的唯一真相是 python 版，改动这里之前先读它的同一段。
//
// 用法
//
//	s := upstream.New(t)                       // 测试结束自动关闭
//	resp, _ := s.Client().Post(s.URL()+"/v1/messages", "application/json", body)
//	// …断言响应…
//	rec := s.Requests()[0]                     // 代理到底发出去的是什么
//
// 三处有意的分歧（都写在对应代码旁边，别当成 bug 改回来）
//
//   - 记录用 Go 的 http.Header（规范大小写）和原始字节，python 存的是小写化
//     的头 + 解析后的 dict。控制面 GET /__mock/requests 仍按 python 的形状
//     输出（小写头 + 解析后的 body + epoch 秒），老脚本照旧能读。
//   - 流式响应没有 Content-Length 也不分块（Transfer-Encoding: identity +
//     Connection: close），与 python 的裸字节流逐字节同形；Go 默认会给
//     HTTP/1.1 的无长度响应加 chunked，那会让网关看到 bash e2e 里不存在的
//     一种分帧。见 (*Server).stream 里的注释。
//   - python 遇到解析不了的请求体（非 dict 的 JSON）会直接抛异常断连，这里
//     退化成 {"__unparsed__": 原文}，测试不会因为一个畸形请求挂死在读响应上。
package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// 常量：与 mock/fake_upstream.py 里的字面量逐字一致。集成测试断言时引这里，
// 别再抄一遍——抄一遍就多一处会漂的真相。
const (
	// StrictReasoningMessage 是 python 版 STRICT_REASONING_ERR 里那句 message
	// 原文：2026-09 在 new-api 直连 DeepSeek 官方的部署上实抓的行为。
	// 断言「严格口径真的拦下了」时拿它做子串匹配——状态码也可能是别的 400，
	// 这句话才是身份。
	StrictReasoningMessage = "The `reasoning_content` in the thinking mode must be passed back to the API. (request id: mock-e2e-strict)"

	// ThinkingText / ThinkingSignature 是非流式 anthropic 响应里那个固定的
	// thinking 块。thinkcache 的回填断言认的就是这两个值。
	ThinkingText      = "MOCK-THINKING-ORIGINAL"
	ThinkingSignature = "mock-sig"

	// ToolUseID / ToolName 是请求带 tools 时追加的那个 tool_use 块。
	// id 固定，是为了让 e2e 能断言「thinkcache 把这段推理原文回填到了哪一发」。
	ToolUseID = "toolu_mock_1"
	ToolName  = "Read"

	// MessageID / ChatCompletionID 是两个方言各自的响应 id。
	MessageID        = "msg_mock"
	ChatCompletionID = "chatcmpl-mock"

	// DefaultChunkDelay 是流式块间隔的默认值（python 的 0.05s）。
	DefaultChunkDelay = 50 * time.Millisecond
	// SlowChunkDelay 是请求体带 mock_slow 时的块间隔（python 的 0.5s）。
	// mock_slow 是给「一条流要跨过 restart 窗口」那种用例准备的：默认的
	// 4 块 × 0.5s ≈ 2s，够优雅交接在流中途发生。
	SlowChunkDelay = 500 * time.Millisecond
)

// Server 是一个进程内的假上游。
//
// 一个 Server 一张记录表：New 给每个测试起一个（随机端口），测试之间不共享
// 状态——控制面 /__mock/reset 也就只在同一个测试里有意义。
type Server struct {
	http *httptest.Server

	mu        sync.Mutex
	records   []Record      // 收到过的请求，按时间顺序
	failCode  int           // 非 0 = 武装了「下一发失败」（python 的 NEXT_FAIL）
	delay     time.Duration // 流式块间隔
	slowDelay time.Duration // mock_slow 时的块间隔
}

// New 起一个假上游，并在测试结束时关闭它。
//
// 地址由 httptest 给（127.0.0.1 上的随机端口），所以并行测试互不干扰，也不
// 需要 python 版那种「curl 探活等端口起来」的轮询（见 mock/e2e_claude.sh
// 第 1 章）——New 返回时服务已经在听了。
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{delay: DefaultChunkDelay, slowDelay: SlowChunkDelay}
	s.http = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// URL 是它的根地址（httptest 给的，形如 http://127.0.0.1:PORT）。
// 没有结尾斜杠，路径自己拼：s.URL() + "/v1/messages"。
func (s *Server) URL() string { return s.http.URL }

// Client 是一个**不走代理**的 http.Client。
//
// 为什么不直接用 http.DefaultClient：它认 HTTP_PROXY/HTTPS_PROXY 环境变量，
// 而跑这个仓库测试的会话本身往往就穿行在 newgate 的代理里（root 的 claude
// 被 PATH shim 接管）——环境里挂着代理时，你发给假上游的请求会先被真网关
// 转一手，断言就全乱了。httptest 自带的这个 client 的 Transport 的 Proxy 是
// nil，天然免疫。（mock/e2e_claude.sh 里的假 claude 也干同样的事：
// build_opener(ProxyHandler({}))。）
func (s *Server) Client() *http.Client { return s.http.Client() }

// Close 立刻关掉假上游。Cleanup 里也会调，重复调用无害。
//
// 显式调它的场景：模拟「上游挂了」——Close 之后这个地址上的连接直接失败，
// 用来验证网关的 fallback 会不会换到下一站，比 FailNext（那是一发干净的错误
// 响应）更狠一层。
func (s *Server) Close() { s.http.Close() }

// Record 是一条收到过的请求。
//
// 与 python 版记录的对应关系（python 存的是一个 dict）：
//
//	Path    python 的 rec["path"]，即请求行里的原始 URI，**含 query**
//	        （Claude Code 会发 /v1/messages?beta=true 这种）。断言请用
//	        strings.HasSuffix(r.Path, "/v1/messages")，别拿它跟纯路径比全等。
//	Header  Go 的 http.Header，规范大小写、Get 不区分大小写；python 存的是
//	        小写化的头。读用 rec.Header.Get("Authorization")。
//	Body    原始字节。python 存的是解析后的 dict；这里保留字节，是因为这个
//	        仓库在意「代理到底把什么发出去了」的字节级证据（字段顺序、未知
//	        键、大整数都在字节里）。要字段就 rec.JSON()。
//	At      python 存的是 epoch 秒（float）；Go 侧给 time.Time。
type Record struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
	At     time.Time
}

// JSON 把 Body 解成字段表，给「断言代理改写了哪个字段」用。
//
// 解析走 json.Number：默认的 float64 会把大整数改写成科学计数法（CLAUDE.md
// 里那条「大整数会变 …92」的坑，在记录里同样成立）。body 不是对象、或压根
// 不是 JSON 时返回 error。
func (r Record) JSON() (map[string]any, error) {
	var fields map[string]any
	dec := json.NewDecoder(bytes.NewReader(r.Body))
	dec.UseNumber()
	if err := dec.Decode(&fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// Requests 返回至今收到的全部请求（按时间顺序）。
//
// 返回的是切片副本，但 Record 里的 Header / Body 是共享引用——读没事，别改。
func (s *Server) Requests() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, len(s.records))
	copy(out, s.records)
	return out
}

// Count 是收到过的请求数。断言「上游一共被打了几发」时比 len(Requests()) 顺手。
func (s *Server) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// Last 是最后一条记录；一个请求都还没收到时 ok=false。
func (s *Server) Last() (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) == 0 {
		return Record{}, false
	}
	return s.records[len(s.records)-1], true
}

// Reset 清空请求记录，并解除 FailNext 的武装。
//
// 一并清失败注入是 python 的行为（/__mock/reset 同时清 RECORDED 和
// NEXT_FAIL），照搬——所以「Reset 之后下一发仍然失败」不是本包的 bug。
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = nil
	s.failCode = 0
}

// FailNext 让紧接着的一发请求返回状态码 code，用完即消。
//
// 「用完即消」是刻意的：只有失败注入只影响一发，测试才能精确地说
// 「第一发挂、第二发成了」——链上的换人/fallback 断言全靠这个。
// code 为 0 等于解除武装（python 的 `if NEXT_FAIL["code"]` 里 0 也是 falsy）。
//
// 两个容易踩的点（都是 python 的老行为，照搬）：
//   - 失败的那一发**照样进记录表**（先记后判），所以可以断言「网关确实重试了」；
//   - count_tokens 也吃这一发失败——判失败的顺序在它之前。
func (s *Server) FailNext(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCode = code
}

// SetChunkDelay 调整流式每块之间的延迟（0 = 尽快）。
//
// 默认 DefaultChunkDelay（50ms，python 的值）。0 是给「只验协议、不想白等」的
// 用例准备的；反过来，要验证「代理没有把整个响应缓冲下来」的用例得把延迟
// **调大**——缓冲的实现会把首块也压到整条流吐完才给。
func (s *Server) SetChunkDelay(d time.Duration) {
	if d < 0 {
		d = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delay = d
}

// SetSlowChunkDelay 调整请求体带 mock_slow 时的块间隔（默认 SlowChunkDelay）。
//
// 单独一档而不是让 SetChunkDelay 一起改，是因为「优雅交接不掐在途流」那类用例
// 需要：普通流要快（其它用例别浪费时间），而跨 restart 的那一条要足够慢。
func (s *Server) SetSlowChunkDelay(d time.Duration) {
	if d < 0 {
		d = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slowDelay = d
}

// ---- 路由 ----

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.serveGet(w, r)
	case http.MethodPost:
		s.servePost(w, r)
	default:
		// python 的 BaseHTTPRequestHandler 对没实现的方法回 501（HTML 错误页）。
		// 状态码照搬，体换成 JSON——本包的调用方是 Go/curl，没人解 HTML。
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "unsupported method"})
	}
}

func (s *Server) serveGet(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/__mock/requests":
		writeJSON(w, http.StatusOK, s.wireRecords())
	case path == "/__mock/fail":
		// python: int(q.get("code", ["500"])[0])，缺省 500。给的不是整数时
		// python 直接抛异常断连，这里回 400 把话说清楚（有意的分歧）。
		code := 500
		if raw := r.URL.Query().Get("code"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "code 要是整数"})
				return
			}
			code = n
		}
		s.FailNext(code)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "next_fail": code})
	case strings.HasSuffix(path, "/v1/models"):
		// python 这里是全等（== "/v1/models"）；用后缀是为了带前缀的变体
		// （/a/claude/p/ds/v1/models）也能直接打到假上游上，是超集。
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []any{}})
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such path"})
	}
}

func (s *Server) servePost(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/__mock/reset" {
		// python 也在读 body 之前处理 reset：这一发不进记录表。
		s.Reset()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		// 客户端半路断了。python 会按 Content-Length 死等，这里直接回 400，
		// 测试不会挂死在读响应上。
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "读请求体失败: " + err.Error()})
		return
	}
	body := decodeBody(raw)

	// 先记录、后判失败（python 同序）：失败的那一发也要留证据。
	s.appendRecord(Record{
		Method: r.Method,
		Path:   r.URL.RequestURI(),
		Header: r.Header.Clone(),
		Body:   raw,
		At:     time.Now(),
	})

	if code := s.takeFail(); code != 0 {
		writeJSON(w, code, map[string]any{"error": map[string]any{
			"message": fmt.Sprintf("mock forced %d", code),
		}})
		return
	}

	// anthropic 的私有端点：原生 anthropic 上游有、聚合器往往没有
	// （api.rvcompute.com 实测 404）。newgate 按上游能力决定是转发拿真值还是
	// 本地粗估——假上游实现它，转发路径才有得测。判在严格校验**之前**，
	// 因为 count_tokens 请求不带 model、也不该被思考模式的规则拦下。
	if strings.HasSuffix(path, "/count_tokens") {
		writeJSON(w, http.StatusOK, map[string]any{"input_tokens": 42})
		return
	}

	if strictReasoningViolation(body) {
		writeJSON(w, http.StatusBadRequest, strictReasoningError())
		return
	}

	model := modelOf(body)
	anthropic := strings.HasSuffix(path, "/messages")
	if truthy(body["stream"]) {
		s.stream(w, r, model, anthropic, truthy(body["mock_slow"]), truthy(body["tools"]))
		return
	}
	if anthropic {
		writeJSON(w, http.StatusOK, anthropicMessage(model, truthy(body["tools"])))
		return
	}
	writeJSON(w, http.StatusOK, chatCompletion(model))
}

// ---- 记录表 ----

func (s *Server) appendRecord(rec Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, rec)
}

// takeFail 读走武装中的失败码（用完即消）。
func (s *Server) takeFail() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	code := s.failCode
	s.failCode = 0
	return code
}

// delays 快照当前的两档块间隔。python 是在 _stream 入口读模块级常量的，
// 中途改不影响已经开吐的这条流——照抄这个时机，并发调 SetChunkDelay 时
// 不会出现半条流中途变速。
func (s *Server) delays(slow bool) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slow {
		return s.slowDelay
	}
	return s.delay
}

// wireRecord 是 /__mock/requests 的线上形状——**照 python 版的 dict 抄的**
// （path/method/headers/body/at），好让 bash 脚本和 curl 看到的东西一字不变。
// 与 Go 侧 Record 的差别：头是小写化的单值 map，body 是解析后的 JSON
// （解析不了就是 {"__unparsed__": 原文}，python 的兜底），时间是 epoch 秒。
type wireRecord struct {
	Path    string            `json:"path"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    any               `json:"body"`
	At      float64           `json:"at"`
}

func (s *Server) wireRecords() []wireRecord {
	s.mu.Lock()
	records := make([]Record, len(s.records))
	copy(records, s.records)
	s.mu.Unlock()

	// 空记录要 marshal 成 []，不是 null：python 给的是 []，而 e2e_claude.sh 里
	// 那些 one-liner 是直接 for x in r 的，null 会让它们当场 TypeError。
	out := make([]wireRecord, 0, len(records))
	for _, rec := range records {
		headers := make(map[string]string, len(rec.Header))
		for key, values := range rec.Header {
			if len(values) == 0 {
				continue
			}
			// python 的 dict 推导式遇到重复的键是「后写的赢」，保持一致。
			headers[strings.ToLower(key)] = values[len(values)-1]
		}
		out = append(out, wireRecord{
			Path:    rec.Path,
			Method:  rec.Method,
			Headers: headers,
			Body:    decodeBody(rec.Body),
			At:      float64(rec.At.UnixNano()) / float64(time.Second),
		})
	}
	return out
}

// ---- 小工具 ----

func writeJSON(w http.ResponseWriter, code int, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// 显式给长度（python 的 _json 也给了），非流式响应就不必靠连接关闭分帧。
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(code)
	_, _ = w.Write(raw)
}

// decodeBody 解析请求体，解析不了返回 {"__unparsed__": 原文}（python 的兜底）。
//
// 用 json.Decoder + UseNumber，别退回 json.Unmarshal 的 float64：那会把大整数
// 改写成科学计数法（这个仓库被这个坑咬过，见 CLAUDE.md）。python 版把解析后的
// int 原样再 dump 出去，这里也得原样。
func decodeBody(raw []byte) map[string]any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{} // python: json.loads(raw or b"{}")
	}
	var parsed map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err != nil {
		return map[string]any{"__unparsed__": string(raw)}
	}
	return parsed
}

// truthy 是 python 的真值语义：None/False/0/""/[]/{} 为假。
// 请求体是 JSON，落到 any 上的类型就那么几种。
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case json.Number:
		n, err := typed.Float64()
		return err != nil || n != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// modelOf 读请求里的 model（python: body.get("model", "unknown")）。
// 只有「键不在」或值是 null 才回落 "unknown"——空串也原样回显，与 python 一致
// （响应里的 model 会原样回显，断言「代理把档位反解成了哪个真实模型」就看它）。
func modelOf(body map[string]any) string {
	if model, ok := body["model"].(string); ok {
		return model
	}
	return "unknown"
}
