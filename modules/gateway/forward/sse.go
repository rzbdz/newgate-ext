package forward

import (
	"bytes"
	"net/http"

	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

// sseSplitter splits the upstream stream at SSE event boundaries, handing each event to the
// rewriter this shot claimed.
//
// Why it is needed: response-side patches (e.g. codex x DeepSeek's custom_tool_call restore)
// change **JSON inside an event**, and one event may be split by TCP at any point. To change
// it, one whole event must first be accumulated.
//
// Its split against thinkcache.Observer: that one is a **read-only side path** (it takes what
// it gets, a misread does not affect forwarding); this one is **on the path** -- what goes out
// is the bytes it gives, so it must guarantee "if the rewriter says no change, byte-identical".
//
// Three hard constraints:
//   - **Split only at `\n\n`.** That is the SSE event boundary (newlines may be \r\n, the cut
//     still lands on \n\n, so keeping a \r in the cut bytes is fine).
//   - **When it cannot accumulate, emit as-is.** An event larger than the cap cannot be
//     accumulated forever -- that blows up memory and stalls the stream. Past the cap, write
//     the bytes already in hand as-is, degrading to "no splitting": this event cannot be
//     changed, but **forwarding itself is unaffected** (fail-open's direction).
//   - **Flush the tail at the end.** When the stream ends the buffer usually still holds the
//     last piece (many upstreams' last event has no trailing blank line); without a flush,
//     bytes are lost.
type sseSplitter struct {
	w      http.ResponseWriter
	egress special.Egress
	onIn   func(orig, out []byte) // both byte slices at every real change (the caller logs)

	pending bytes.Buffer
}

// maxSSEEvent once an event reaches this size without completing, give up splitting and
// forward as-is.
//
// 4 MiB shares its origin with thinkcache.Observer's cap (there the case is "a chunk stuffed
// with a huge reasoning block"); real events are far smaller -- tens to thousands of bytes.
const maxSSEEvent = 4 << 20

func newSSESplitter(w http.ResponseWriter, egress special.Egress,
	onIn func(orig, out []byte)) *sseSplitter {
	return &sseSplitter{w: w, egress: egress, onIn: onIn}
}

// write is the entry point on this side: the bytes fed in are **the upstream's raw bytes**.
func (s *sseSplitter) write(p []byte) error {
	s.pending.Write(p)
	for {
		raw := s.pending.Bytes()
		i := bytes.Index(raw, []byte("\n\n"))
		if i < 0 {
			if s.pending.Len() > maxSSEEvent {
				// No boundary and too large: emit as-is, stop accumulating.
				out := append([]byte(nil), raw...)
				s.pending.Reset()
				_, err := s.w.Write(out)
				return err
			}
			return nil
		}
		event := append([]byte(nil), raw[:i+2]...) // with trailing blank line: a **complete** replacement
		s.pending.Next(i + 2)
		if err := s.emit(event); err != nil {
			return err
		}
	}
}

// emit hands one complete event to the rewriter, then writes it out.
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

// flush emits the buffer's last piece (an event without a trailing blank line).
func (s *sseSplitter) flush() error {
	if s.pending.Len() == 0 {
		return nil
	}
	event := append([]byte(nil), s.pending.Bytes()...)
	s.pending.Reset()
	return s.emit(event)
}
