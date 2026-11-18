package breaker

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	modules "github.com/rzbdz/newgate/component"
)

// ShapeDetector 判断「这次 4xx 是不是**请求形状**问题」。
//
// 为什么把判据交出去（2026-09-17）：形状错误的定义完全属于**上游自己**。
// 「同一份 body 换哪个 provider 都一样错」这个性质，只有那个上游知道它的
// 校验规则才能判定——`must be passed back` 那两句文案是 DeepSeek 的方言，
// 硬编码在健康表里等于让 core 替上游背一条它不该知道的字符串。而且这条判据
// 是个**行权点**：认出来就不摘牌，认错了要么误摘（伤用户）要么放过（伤观测）。
// 它必须和知道真相的那个模块放在一起、由那个模块自己测试。
//
// core 只提供端口和策略（BucketShape：永不摘牌、只计数）。
//
// Match 必须是**纯函数**且不 panic：它在每次 4xx 上都会被调用，fail-open 的
// 兜底在 registry 那层做（见 shapeRegistry.match）。
type ShapeDetector interface {
	// Name 进日志与指标，用来回答「是哪条规则认出来的」。
	Name() string
	// Match 判断这个响应是不是该上游的请求形状错误。
	Match(status int, body []byte) bool
}

// shapeRegistry 持有注册进来的形状检测器。
//
// 为什么放在 table 里而不是包级变量：健康表在测试里可以起很多份，包级注册表
// 会让它们互相污染（谁先注册谁生效，测试顺序影响结果）。这里跟着实例走，
// 注册和注销都只影响自己那一份。
type shapeRegistry struct {
	mu        sync.RWMutex
	detectors []ShapeDetector
	tokens    map[string]uint64
	next      uint64
}

// errShapeDetector 是注册期的两类拒绝。做成变量是为了让测试能 errors.Is。
var (
	errShapeDetectorName   = errors.New("breaker: shape detector needs a Name()")
	errShapeDetectorDupFmt = "breaker: duplicate shape detector %s"
)

func errShapeDetectorDuplicate(name string) error {
	return fmt.Errorf(errShapeDetectorDupFmt, name)
}

// Register 加入一个检测器，返回只属于本次注册的撤销句柄。
//
// 同名重复注册直接报错：两个都叫 deepseek 的检测器同时生效，日志和指标就
// 分不清是谁认出来的了。
func (r *shapeRegistry) Register(d ShapeDetector) (modules.Release, error) {
	if d == nil || d.Name() == "" {
		return nil, errShapeDetectorName
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.detectors {
		if existing.Name() == d.Name() {
			return nil, errShapeDetectorDuplicate(d.Name())
		}
	}
	if r.tokens == nil {
		r.tokens = map[string]uint64{}
	}
	r.next++
	token := r.next
	// tokens 这张表**不是**为了容忍重复注册（那已经在上面被拒了），而是为了让
	// **陈旧 Release 无害**：注销之后再注册同一个键会拿到新 token，那个旧
	// Release 一旦晚到就会把新注册删掉。比对 token 拦的正是这一种。
	// （看代码时容易以为这条分支不可达——那是漏掉了「注销 → 再注册」。）
	r.tokens[d.Name()] = token
	r.detectors = append(append([]ShapeDetector(nil), r.detectors...), d)
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.tokens[d.Name()] != token {
			return nil // 已经被后来的注册顶掉了，不动
		}
		delete(r.tokens, d.Name())
		for i, existing := range r.detectors {
			if existing.Name() == d.Name() {
				r.detectors = append(r.detectors[:i], r.detectors[i+1:]...)
				break
			}
		}
		return nil
	}, nil
}

// match 问一遍所有检测器：任一个认领就是形状错误，返回认领者的名字。
//
// 返回名字是为了让日志说得清「是哪条规则认出来的」——形状检测器是多家上游
// 各自的判据，一条只说「命中了形状错误」的日志在有两家上游同时报 400 时毫
// 无用处。
//
// 每个检测器单独 recover：注册进来的是别人的代码，一个坏检测器不该让整条
// 记账路径跟着崩（fail-open 的同一原则——宁可少认一次形状错误，也不能因为
// 一个补丁把代理弄挂）。
func (r *shapeRegistry) match(status int, body []byte) (string, bool) {
	r.mu.RLock()
	detectors := r.detectors
	r.mu.RUnlock()
	for _, d := range detectors {
		if shapeMatch(d, status, body) {
			return d.Name(), true
		}
	}
	return "", false
}

// names 返回当前生效的检测器名（已排序，便于日志与测试断言）。
func (r *shapeRegistry) names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.detectors))
	for _, d := range r.detectors {
		out = append(out, d.Name())
	}
	sort.Strings(out)
	return out
}

// shapeMatch 是 recover 包一层。单独的顶层函数是为了让 defer 的恢复范围
// 精确到「这一次调用」，而不是把 match 的整个循环包进去。
func shapeMatch(d ShapeDetector, status int, body []byte) (matched bool) {
	defer func() {
		if recover() != nil {
			matched = false
		}
	}()
	return d.Match(status, body)
}

// shapeOf 问一句「这次失败是不是请求形状问题」，并说是哪条判据认的。
//
// **没有注册任何检测器时恒为 („", false)**：那意味着 400 一律不记在任何人
// 头上（见 Classify：非形状 400 的 Bucket 是 BucketNone）。这是安全的方向
// ——少认一次形状错误只是少一个计数，误认一次会让真正的可用性故障被放过。
func (b *table) shapeOf(status int, body []byte) (string, bool) {
	return b.shapes.match(status, body)
}

// ShapeDetectors 列出当前生效的形状检测器名，供诊断与日志。
func (b *table) ShapeDetectors() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.shapes.names()
}

// RegisterShapeDetector 是端口实现：把判据交给知道那家校验规则的模块。
func (b *table) RegisterShapeDetector(d ShapeDetector) (modules.Release, error) {
	return b.shapes.Register(d)
}
