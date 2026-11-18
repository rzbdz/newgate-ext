package metrics

import (
	"strings"

	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Group 返回计数器归属的组：**身份**（ASCII 稳定标识）与**说法**（给人看的锚点）。
//
// 为什么要分成两样：`newgate metrics` 那张表按组排序，而那个顺序讲的是**一次请求
// 的生命周期**（请求 → 链 → 超时 → 兜底 → 插件 → 熔断 → 客户端）——顺序本身是
// 内容，不是版式。排序就得拿一个**与语言无关**的东西去对；拿说法去对，换个语言
// 顺序就可能跟着变（两门语言里那几个词没有共同的次序），于是中文用户和英文用户
// 看到的不是同一张表。身份管排序与去重，说法只管印出来。
//
// **为什么这张表住在这里而不是 CLI**：这些名字是转发热路径产生的（全部由
// gateway/forward 里的 metrics.Default.Inc 写下），含义只有数据面知道。CLI 的
// `newgate metrics` 是那一屏表格的**排版者**，它不该知道 "chain.failover"
// 是什么——2026-09-18 之前那张表硬编码在 modules/cli/diag.go 里，于是改一个
// 计数器名要同时改两处，而 CLI 那处根本不知道自己在改什么。
//
// **这张表只覆盖数据面自己的计数器。** 别人的计数器由别人自报：调用方先问
// 策略账本（policy.Registry.MetricGroup），没人认领才落到这里。`breaker.*`
// 那三行 2026-09-18 就是这么搬回 modules/breaker 的——熔断器打开意味着什么，
// 不该由这个包解释。
//
// 身份也**不翻译**：它是计数器与表格之间那份契约的键，只在本进程内用。
func Group(k string) (id, label string) {
	switch {
	case strings.HasPrefix(k, "requests."):
		return "requests", i18n.T("requests", nil)
	case strings.HasPrefix(k, "chain."):
		return "chain", i18n.T("chain", nil)
	case strings.HasPrefix(k, "timeout."):
		return "timeout", i18n.T("timeout", nil)
	case strings.HasPrefix(k, "client."):
		return "client", i18n.T("client", nil)
	case strings.HasPrefix(k, "special."):
		return "plugin", i18n.T("plugin", nil)
	case strings.HasPrefix(k, "count_tokens."):
		return "fallback", i18n.T("fallback", nil)
	}
	return "other", i18n.T("other", nil)
}

// Hint 返回计数器名字的人话注释。没列出的不硬凑——空说明比编一句好。
//
// 同 Group：只有数据面自己的计数器在这里，别人的由贡献者经 MetricNamer 自报。
// `special.*` 一族也是自报的（special.MetricProvider）——那个名字里带插件名，
// 只有插件知道那一笔业务上意味着什么。
func Hint(k string) string {
	switch {
	case k == "requests.total":
		return i18n.T("requests entering the gateway", nil)
	case k == "count_tokens.forwarded":
		return i18n.T("forwarded upstream to get the true value", nil)
	case k == "count_tokens.local":
		return i18n.T("local rough estimate fallback (upstream has no such endpoint)", nil)
	case k == "count_tokens.probe_404":
		return i18n.T("lazy probe returned 404, recorded as upstream unsupported", nil)
	case k == "timeout.first_byte.non_stream":
		return i18n.T("first byte timed out (non-stream), moved down the chain", nil)
	case k == "timeout.first_byte.stream":
		return i18n.T("first byte timed out (stream), moved down the chain", nil)
	case strings.HasPrefix(k, "timeout.first_byte"):
		return i18n.T("first byte timed out, moved down the chain", nil)
	case k == "chain.failover":
		return i18n.T("an earlier candidate failed and a later one succeeded", nil)
	case k == "chain.step_failed":
		return i18n.T("a chain step failed (connection / transferable error)", nil)
	case k == "chain.budget_exhausted":
		return i18n.T("chain budget exhausted", nil)
	case k == "client.cancel":
		return i18n.T("cancelled by the client", nil)
	case strings.HasPrefix(k, "special."):
		if hint, ok := special.MetricHint(k); ok {
			return hint
		}
		return i18n.T("a plugin rewrote the request (each rewrite is logged)", nil)
	}
	return ""
}
