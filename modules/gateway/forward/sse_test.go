package forward

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

// rewriter 是测试用的改写者：按 map 替换整条事件，认不出返回 nil。
type rewriter struct {
	repl  map[string]string
	calls int
}

func (r *rewriter) Egress(event []byte) []byte {
	r.calls++
	if to, ok := r.repl[string(event)]; ok {
		return []byte(to)
	}
	return nil
}

// discardWriter 只收字节，不当 http.ResponseWriter 用（splitter 只写它）。
type discardWriter struct {
	http.ResponseWriter
	buf bytes.Buffer
}

func (d *discardWriter) Write(p []byte) (int, error) { return d.buf.Write(p) }

// 一条事件被 TCP 切成任意几段，也要按 `\n\n` 边界拼回完整事件再交出去。
func TestSSESplitterReassemblesAcrossChunks(t *testing.T) {
	w := &discardWriter{}
	first := "event: a\ndata: {\"one\":1}\n\n"
	second := "event: b\ndata: {\"two\":2}\n\n"
	rw := &rewriter{repl: map[string]string{
		second: "event: b\ndata: {\"two\":\"changed\"}\n\n",
	}}
	sp := newSSESplitter(w, rw, nil)

	// 把两条事件切在**任意**位置喂进去：一次一个字节。
	all := first + second
	for i := 0; i < len(all); i++ {
		if err := sp.write([]byte{all[i]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sp.flush(); err != nil {
		t.Fatal(err)
	}
	want := first + "event: b\ndata: {\"two\":\"changed\"}\n\n"
	if w.buf.String() != want {
		t.Fatalf("got %q\nwant %q", w.buf.String(), want)
	}
}

// 改写者说不动的那条事件必须**逐字节**原样——这是「没有补丁时转发就是转发」
// 在拆帧这条路上的落点。
func TestSSESplitterPassesUntouchedEventsThrough(t *testing.T) {
	w := &discardWriter{}
	event := "event: response.created\ndata: {\"a\":1}\n\n"
	rw := &rewriter{}
	sp := newSSESplitter(w, rw, nil)
	if err := sp.write([]byte(event)); err != nil {
		t.Fatal(err)
	}
	if w.buf.String() != event {
		t.Fatalf("an unclaimed event must survive byte for byte: %q", w.buf.String())
	}
	if rw.calls != 1 {
		t.Fatalf("the rewriter must be asked once, got %d", rw.calls)
	}
}

// 收尾：很多上游的最后一个事件不带结尾空行，不吐就丢字节。
func TestSSESplitterFlushesTheTrailingEvent(t *testing.T) {
	w := &discardWriter{}
	rw := &rewriter{}
	sp := newSSESplitter(w, rw, nil)
	trailing := "event: response.completed\ndata: {\"done\":true}"
	if err := sp.write([]byte(trailing)); err != nil {
		t.Fatal(err)
	}
	if w.buf.Len() != 0 {
		t.Fatalf("an event without its blank line must not be written early: %q", w.buf.String())
	}
	if err := sp.flush(); err != nil {
		t.Fatal(err)
	}
	if w.buf.String() != trailing {
		t.Fatalf("flush must emit the trailing tail: %q", w.buf.String())
	}
}

// 一条事件大到超过上限还不完整时，原样吐出去、别再攒——否则内存会爆，流会卡住。
// 这是 fail-open 在这条路上的落点：这一条改不了，但转发本身不受影响。
func TestSSESplitterGivesUpOnOversizedEvents(t *testing.T) {
	w := &discardWriter{}
	rw := &rewriter{}
	sp := newSSESplitter(w, rw, nil)
	huge := bytes.Repeat([]byte("x"), maxSSEEvent+1)
	if err := sp.write(huge); err != nil {
		t.Fatal(err)
	}
	if w.buf.Len() != len(huge) {
		t.Fatalf("an oversized event must be forwarded as-is: wrote %d of %d",
			w.buf.Len(), len(huge))
	}
	if rw.calls != 0 {
		t.Fatalf("an oversized event is not an event: asked the rewriter %d times", rw.calls)
	}
	// 之后来的字节照常按边界切，不受影响。
	next := "data: {\"ok\":1}\n\n"
	if err := sp.write([]byte(next)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(w.buf.String(), next) {
		t.Fatalf("the stream must keep working after giving up: %q", w.buf.String())
	}
}

// 拆帧只改**写出时机**，不改字节：没有改写者认领的事件，交付的字节与上游给的
// 完全一致（含 \r\n 结尾、注释行、[DONE]）。
func TestSSESplitterPreservesCRLFAndOddities(t *testing.T) {
	w := &discardWriter{}
	rw := &rewriter{}
	sp := newSSESplitter(w, rw, nil)
	stream := ": keepalive\r\n\r\nevent: ping\r\ndata: [DONE]\r\n\r\n"
	// 边界是 `\n\n`，`\r\n\r\n` 里就有一个，所以切点落在注释行那条事件的结尾；
	// 逐字节保真的判据是**拼起来还是原来那条流**。
	if err := sp.write([]byte(stream)); err != nil {
		t.Fatal(err)
	}
	if err := sp.flush(); err != nil {
		t.Fatal(err)
	}
	if w.buf.String() != stream {
		t.Fatalf("the stream must be byte-identical:\n got %q\nwant %q", w.buf.String(), stream)
	}
}

var _ special.Egress = (*rewriter)(nil)
