package forward

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

// rewriter is a test rewriter: replace a whole event by map, return nil when unrecognized.
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

// discardWriter only takes bytes; it is not usable as an http.ResponseWriter (the splitter
// writes only to it).
type discardWriter struct {
	http.ResponseWriter
	buf bytes.Buffer
}

func (d *discardWriter) Write(p []byte) (int, error) { return d.buf.Write(p) }

// One event split by TCP at any point must still be reassembled at `\n\n` boundaries and
// handed over whole.
func TestSSESplitterReassemblesAcrossChunks(t *testing.T) {
	w := &discardWriter{}
	first := "event: a\ndata: {\"one\":1}\n\n"
	second := "event: b\ndata: {\"two\":2}\n\n"
	rw := &rewriter{repl: map[string]string{
		second: "event: b\ndata: {\"two\":\"changed\"}\n\n",
	}}
	sp := newSSESplitter(w, rw, nil)

	// Feed two events split at **arbitrary** points: one byte at a time.
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

// The event the rewriter says nothing about must stay **byte-identical** -- this is where "with
// no patch, forwarding is forwarding" lands on the splitting path.
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

// The tail: many upstreams' last event has no trailing blank line; not emitting it loses bytes.
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

// When an event grows past the cap without completing, emit it as-is and stop accumulating --
// otherwise memory blows up and the stream stalls. This is where fail-open lands on this path:
// this event cannot be changed, but forwarding itself is unaffected.
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
	// Bytes arriving afterwards are split at boundaries as usual, unaffected.
	next := "data: {\"ok\":1}\n\n"
	if err := sp.write([]byte(next)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(w.buf.String(), next) {
		t.Fatalf("the stream must keep working after giving up: %q", w.buf.String())
	}
}

// Splitting changes only **when** bytes are written, not the bytes: for an event no rewriter
// claimed, what is delivered equals what the upstream gave (including \r\n endings, comment
// lines, [DONE]).
func TestSSESplitterPreservesCRLFAndOddities(t *testing.T) {
	w := &discardWriter{}
	rw := &rewriter{}
	sp := newSSESplitter(w, rw, nil)
	stream := ": keepalive\r\n\r\nevent: ping\r\ndata: [DONE]\r\n\r\n"
	// The boundary is `\n\n` and `\r\n\r\n` contains one, so the cut lands at the end of the
	// comment-line event; the byte-fidelity criterion is **concatenated, it is still the
	// original stream**.
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
