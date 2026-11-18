package forward

import (
	"bytes"
	"net/http"

	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

// sseSplitter 按 SSE 事件边界切开上游流，把每条事件交给这一发认领的改写着。
//
// 为什么需要它：响应侧补丁（比如 codex × DeepSeek 的 custom_tool_call 还原）
// 改的是**事件里的 JSON**，而一个事件可能被 TCP 切成任意几段。要改就得先攒齐
// 一个事件。
//
// 它与 thinkcache.Observer 的分工：那个是**只读旁路**（拿到什么算什么，看错
// 了也不影响转发）；这个是**在通路上**的，写出去的就是它给的字节，所以它必须
// 保证「改写者说不动就逐字节原样」。
//
// 三条硬约束：
//   - **只按 `\n\n` 切。** SSE 的事件边界就是这个（换行可以是 \r\n，切点仍然
//     落在 \n\n 上，所以切出来的字节里保留 \r 也没关系）。
//   - **攒不齐就原样吐出去。** 一个事件大到超过上限时不能无限攒——那会把内存
//     吃爆，而且流就卡住了。超限时把已经拿到的字节原样写出去，退化成「不拆帧」：
//     这一条改不了，但**转发本身不受影响**（fail-open 的方向）。
//   - **收尾要把尾巴吐掉。** 流结束时缓冲区里通常还剩最后一段（很多上游的
//     最后一个事件不带结尾空行），不 flush 就丢字节。
type sseSplitter struct {
	w      http.ResponseWriter
	egress special.Egress
	onIn   func(orig, out []byte) // 每次真的改了就把两条字节交出来（调用方记日志）

	pending bytes.Buffer
}

// maxSSEEvent 一条事件攒到这个大小还不完整就放弃拆帧、原样转发。
//
// 4 MiB 与 thinkcache.Observer 的上限同源（那里是「一个 chunk 里塞了大段推理」），
// 实际事件远小于它——正常事件是几十到几千字节。
const maxSSEEvent = 4 << 20

func newSSESplitter(w http.ResponseWriter, egress special.Egress,
	onIn func(orig, out []byte)) *sseSplitter {
	return &sseSplitter{w: w, egress: egress, onIn: onIn}
}

// write 是这一侧的入口：喂进来的字节是**上游给的原字节**。
func (s *sseSplitter) write(p []byte) error {
	s.pending.Write(p)
	for {
		raw := s.pending.Bytes()
		i := bytes.Index(raw, []byte("\n\n"))
		if i < 0 {
			if s.pending.Len() > maxSSEEvent {
				// 攒不出边界又太长：原样吐出去，别再攒。
				out := append([]byte(nil), raw...)
				s.pending.Reset()
				_, err := s.w.Write(out)
				return err
			}
			return nil
		}
		event := append([]byte(nil), raw[:i+2]...) // 含结尾空行：这是**完整替代**
		s.pending.Next(i + 2)
		if err := s.emit(event); err != nil {
			return err
		}
	}
}

// emit 把一条完整事件交给改写着，然后写出去。
func (s *sseSplitter) emit(event []byte) error {
	out := special.RewriteEvent(s.egress, event)
	if len(out) == 0 {
		_, err := s.w.Write(event)
		return err
	}
	if s.onIn != nil {
		s.onIn(event, out)
	}
	_, err := s.w.Write(out)
	return err
}

// flush 把缓冲区里最后那一段（没有结尾空行的事件）吐掉。
func (s *sseSplitter) flush() error {
	if s.pending.Len() == 0 {
		return nil
	}
	event := append([]byte(nil), s.pending.Bytes()...)
	s.pending.Reset()
	return s.emit(event)
}
