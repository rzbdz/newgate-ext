// Package assembly turns the component graph's assembly trace into a log record.
//
// Why it is here and not in the kernel: the kernel only offers the fact outlet
// (component.SetTrace). "Report it or not, where, in what format, and whether to
// dedupe repeated identical assemblies" is a product decision. This used to live
// in the kernel's app package together with the distribution's log path, which
// made the framework unusable without a distribution.
//
// 内核只提供事实出口（component.SetTrace），不认识日志、不认识文件、不认识
// 环境变量；「报不报、报去哪、什么时候报」是产品层的决定，都在这一处。
package assembly

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"sync"
	"time"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// TraceTo 把装配过程（扫描 → 声明 → 构图 → 启动 → 停止）写给这些 writer，
// 返回收尾函数（进程退出前调用）。不传 writer 等于关掉。
//
// 它是这一族里唯一的原语，别处（含子进程）都从它出发——所以它不依赖任何
// 具体目的地，也不需要是 *log.Logger：装配过程一律带时间戳输出，而
// log.Logger 本身就是个 io.Writer。
func TraceTo(writers ...io.Writer) func() {
	live := make([]io.Writer, 0, len(writers))
	for _, w := range writers {
		if w != nil {
			live = append(live, w)
		}
	}
	if len(live) == 0 {
		modules.SetTrace(nil)
		return func() {}
	}
	lg := make([]*log.Logger, 0, len(live))
	for _, w := range live {
		lg = append(lg, log.New(w, "", log.LstdFlags))
	}
	modules.SetTrace(func(format string, args ...any) {
		for _, l := range lg {
			l.Printf(format, args...)
		}
	})
	return func() { modules.SetTrace(nil) }
}

// TraceToLogFile 把装配过程追加进 newgate 的日志文件；`NEWGATE_TRACE=1` 时**同时**
// 打到 stderr，方便本地跑一发亲眼看清顺序（那一份不该是默认行为——CLI 的输出是
// 给人读的，`newgate tier normal` 的答案不该被几十行装配日志埋掉）。
//
// # 为什么每个进程都记，而不是只记守护进程
//
// 装配过程是「这一版到底起了哪几个模块、什么顺序、谁的弱依赖没命中」的唯一一手
// 材料——它只在装配期存在，事后从 Manager 里问不出来（那里只有结果）。
//
// 最需要它的场合是**优雅交接**：旧进程还在服务，新进程在后台装配，用户全程看不
// 见它。而 `newgate restart` 的优雅路径是**旧进程**把 socket 交给新进程的，旧二
// 进制不可能给新二进制带上「你是守护进程」这类的标记——判据必须是进程**自己**
// 知道的。用「argv 里有 __serve」当判据等于把 gateway 的动词名抄进 main，用环境
// 变量当判据则恰好在最需要它的那一次升级上失效。所以这里不做区分：**谁装配了
// 图，谁就留下这一段**。
//
// # 同一个装配只写一次（2026-09-20）
//
// 上面那条「不做区分」的代价，实测比注释里原来写的数字大得多：整图 24 个组件、
// 每一条 `newgate …` 命令（`status`、`--help`、agent 启动路径上的 `newgate claude`）
// 都往日志里写 **104 行**，而且这 104 行**逐字相同**——声明顺序是编译期定下的、
// 拓扑顺序是稳定排序、时延那几位是唯一会变的东西。一个开发下午的日志实测
// 38087 行，其中真正的代理流量 **23 行**：这本日志是这个产品最主要的排查面
// （`newgate logs`、alllogs 诊断包），它 99.9% 是装配回音。
//
// 所以改成**只在装配变了的时候写整段**，没变就**一个字都不写**：
//
//	2026/09/20 18:48:07 assembly: fingerprint 3f9a1c2b4d5e — 上面这段是新的
//
// （2026-09-21 又收了一次：原先「没变」时会留一行说明。那一行仍然是回音——
// 敲一条 `newgate status` 就往守护进程日志里加一行「什么都没变」，而这份日志
// 上每一行都该是一次跃迁。理由写在 flush 里。）
//
// 判据是那段文字**去掉时间戳与耗时之后**的内容哈希。它买到的东西正好是当初要它
// 的理由：升级那一发的整段记录照样在（新旧二进制装的东西不同 → 指纹不同 → 整段
// 写出），而同一份二进制敲一万次命令只留下「我装过一次，是这一份」。
// 这与 watcher、configshare 的日志政策是同一条——**只在状态跃迁时报**。
//
// 随之而来的两个取舍，都写明白：
//
//   - **记录改成攒完再写**，所以装配**中途**进程死掉时这一段不在日志里（原来
//     是边装配边写，死的时候能留下半截）。要看那半截就用 `NEWGATE_TRACE=1`——
//     那条路径照旧一路流出去，不缓冲、不判断，它同时是这条的逃生口。代价是
//     那一发**不写指纹**（它走的是旧路径，末尾没有标记行），于是**紧跟着的
//     下一发会把整段重写一次**，之后又回到一行。这是有意的：逃生口的意义就是
//     「旧行为，一个字节不改」，而它是个调试开关，不是常态。
//   - 指纹**只在当前这份日志里**找（读尾部 8KB，不另开状态文件）。所以日志
//     轮转之后的第一发会重写整段——那是对的：新的一份日志本来就该自己带上
//     这份记录，而不是继承一份它没有的。
//
// # 打不开就静默跳过
//
// 典型现场：root 的 CLI 撞上 claude 建的 `0640 root:developer`（CLAUDE.md §3.1）。
// 这里不报也不拦，是有意的取舍——留痕是诊断素材，不该让每条 `newgate status` 都
// 往终端吐一行权限警告，而那个权限问题本身已经在别的路径上响亮地报过
// （thinkcache 落盘关闭、控制令牌写不出去）。
func TraceToLogFile(logFile string) func() {
	// **O_RDWR 而不是 O_WRONLY**：这里要把上一次的记录读回来看（lastFingerprint）。
	// 写成只写时 ReadAt 一律 EBADF，回看永远返回空——于是每一发都被当成「第一次」，
	// 整段照写，这个文件要修的东西原样还在。2026-09-20 实测踩过：单元测试自己用
	// O_RDWR 开文件，所以它看不见（补了 TestRepeatedInvocationsOfTheRealThing 之后才看得见）。
	// O_APPEND 保证写入仍然只追加。
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o660)
	if err != nil {
		if os.Getenv("NEWGATE_TRACE") != "" {
			return TraceTo(os.Stderr)
		}
		return TraceTo()
	}
	// NEWGATE_TRACE 是「我现在就要看着它跑」：不缓冲，边装配边写。
	if os.Getenv("NEWGATE_TRACE") != "" {
		stop := TraceTo(f, os.Stderr)
		return func() {
			stop()
			_ = f.Close()
		}
	}
	rec := newAssemblyRecord(f, lastFingerprint(f))
	modules.SetTrace(rec.add)
	return func() {
		rec.flush()
		modules.SetTrace(nil)
		_ = f.Close()
	}
}

// fingerprintMarker 是写进日志的那行标记的**机器锚点**。它刻意不走 i18n：
// 下面是按它找上一次记录的（见 lastFingerprint），翻译了它，中文环境下就再也
// 找不回自己的记录——每一条命令都会重写整段，正是这个文件要修掉的东西。
// 一句话的解释跟在它后面，那句是翻译的。
var fingerprintMarker = regexp.MustCompile(`assembly: fingerprint ([0-9a-f]{12})`)

// tailWindow 是回看上次记录时读的日志尾部长度。
//
// 8KB 的依据：整段记录 24 个组件约 10KB，而标记写在**最末**，所以窗口只要盖得住
// 「最后一行附近」就够——不必盖住整段。
const tailWindow = 8 << 10

// lastFingerprint 在当前日志的尾部找最近一次记下的指纹；没有就返回空。
//
// 只读当前这一份日志文件，不读 `.1` 那些世代：轮转之后本文件里没有记录，于是
// 下一发重写整段——新日志自己带上这份记录，正是想要的。
func lastFingerprint(f *os.File) string {
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	size := st.Size()
	off := int64(0)
	if size > tailWindow {
		off = size - tailWindow
	}
	buf := make([]byte, size-off)
	// ReadAt 不动文件偏移，所以这次回看不会影响后面的追加。
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return ""
	}
	if off > 0 {
		// 窗口可能切在一行中间，第一段是半行，丢掉。
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			return ""
		}
		buf = buf[i+1:]
	}
	all := fingerprintMarker.FindAllSubmatch(buf, -1)
	if len(all) == 0 {
		return ""
	}
	return string(all[len(all)-1][1])
}

// 指纹要把「每次都会变」的两位去掉：时间戳前缀与耗时。剩下的部分（模块、顺序、
// 弱依赖命中与否）才是「这一版装配成什么样」。
//
// **耗时要按形状认，不能按词认**：装配记录是翻过译的（component 调的
// i18n.T），中文那行是「启动 4/23 locale 完成（用时 3.489ms）」——只认英文的
// 「(took …)」一条都剥不掉，于是每一行的耗时都进了指纹，**每次运行都是新指纹**，
// 整段每次都重写。2026-09-20 线上实测：同一份二进制连写四条不同的指纹
// （e5fe…/c683…），而这台开发机的沙箱是英文，一直没露头。
//
// 单位不翻译（`ms` 在中文行里还是 `ms`），所以「数字 + 单位」是语言无关的判据。
var (
	traceStamp = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)
	// 耗时按**形状**剥：数字紧跟单位。单位不翻译（中文那行是「…完成（用时
	// 3.489ms）」，ms 还是 ms），所以这条判据与语言无关——而只认英文的
	// 「(took …)」会漏掉全部译文，症状是换个语言就每次都重写整段
	// （2026-09-20 线上实测：同一份二进制连写四条不同指纹；沙箱是英文，没露头）。
	//
	// 括号与逗号**不用管**：它们每次都在同一处，稳定；这里要的只是稳定，不是好看。
	// 也正因为如此，这条正则里一个中文标点都不需要——少一样要维护的东西。
	traceDuration = regexp.MustCompile(`\d+(?:\.\d+)?(?:ns|µs|us|ms|s)\b`)
)

// normalizeTraceLine 去掉一行装配记录里与「装了什么」无关的部分。
func normalizeTraceLine(l string) string {
	l = traceStamp.ReplaceAllString(l, "")
	return traceDuration.ReplaceAllString(l, "(d)")
}

// assemblyFingerprint 是整段记录的哈希（去时间戳、去耗时之后）。
func assemblyFingerprint(lines []string) string {
	h := sha256.New()
	for _, l := range lines {
		_, _ = io.WriteString(h, l)
		_, _ = h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// assemblyRecord 攒下一段装配记录，收尾时决定整段写出去还是只留一行。
type assemblyRecord struct {
	mu    sync.Mutex
	out   io.Writer
	prev  string   // 上一次记下的指纹（空 = 这份日志里没有记录过）
	lines []string // 带时间戳的原文：整段写出去时用
	plain []string // 去时间戳去耗时：算指纹用
}

func newAssemblyRecord(out io.Writer, prev string) *assemblyRecord {
	return &assemblyRecord{out: out, prev: prev}
}

// add 是 component.SetTrace 的回调，装配期每一条都从这儿过。
//
// 时间戳在这里就打好（不是收尾时补）：那 24 行 `start n/24 … (took …)` 的**顺序与
// 时延**正是这段记录的价值所在，攒完再统一盖一个时间会把它们全压成同一刻。
func (r *assemblyRecord) add(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, stamp()+msg)
	r.plain = append(r.plain, normalizeTraceLine(msg))
}

// flush 写出去。整段只在指纹变了时写，否则只写一行。
func (r *assemblyRecord) flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) == 0 {
		return
	}
	// 比的是**写进日志的那 12 位**，不是完整的 64 位哈希：prev 是从日志里读回来的，
	// 拿全长去比它永远不相等，于是整段每次都写——这个文件要修的东西原样还在，
	// 而日志看起来完全正常（2026-09-20 被 TestFullRecordIsWrittenOnlyWhenItChanges 抓住）。
	fp := assemblyFingerprint(r.plain)[:12]
	if fp == r.prev {
		// **一个字都不写**（2026-09-21 用户：日志里不要再出现这东西）。
		//
		// 原来这里留一行「与已有的记录相同」。它想解决的是日志被装配回音淹掉，
		// 但留下的那一行**仍然是回音**：`status`、`doctor`、`tier` … 每敲一次就
		// 往守护进程的日志里塞一行「什么都没变」。而这份日志是这个产品最主要的
		// 排查面（`newgate logs`、alllogs 诊断包），它上面的每一行都该是**一次
		// 跃迁**——这条政策本文件开头就写着（「只在状态跃迁时报」），那一行恰恰
		// 不是跃迁，是自相矛盾。
		//
		// 什么都不写不会让人查不到东西：装配**变过**的那一段在日志里，它带着
		// 指纹；要看此刻这一发现场有 `NEWGATE_TRACE=1`。而 `lastFingerprint`
		// 靠的就是那条标记行——它还在，所以下一发照样认得出「没变」。
		return
	}
	for _, l := range r.lines {
		fmt.Fprintln(r.out, l)
	}
	fmt.Fprintf(r.out, "%sassembly: fingerprint %s — %s\n", stamp(), fp, i18n.T(
		"the full assembly record above is new; it is written again only when the graph changes", nil))
}

// stamp 是 log.LstdFlags 的格式，与 TraceTo 那条路径写出来的一模一样。
func stamp() string { return time.Now().Format("2006/01/02 15:04:05 ") }
