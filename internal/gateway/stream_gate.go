package gateway

// A streamed reply is read ahead of the agent until its first body content.
// Until then nothing is written to the agent: an error event the provider
// sends first (overloaded, rate limited, a model it does not have) makes the
// reply a failed one, which the caller may hand on to another candidate, and
// the events of the dropped reply, its message_start and pings included, are
// never seen. Once the body has begun, or the hold has waited holdFor, or it
// has read holdBytes, the reply goes through as it comes, and an error after
// that is the agent's to see.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	holdFor   = 15 * time.Second // how long a reply may be held back, waiting for its body
	holdBytes = 1 << 20          // how much of it may be read ahead
)

// pumped reads rd on its own goroutine and hands over what it reads, in
// order. The channel closes when rd ends or fails, or when ctx is done.
func pumped(ctx context.Context, rd io.Reader) <-chan []byte {
	ch := make(chan []byte)
	go func() {
		defer close(ch)
		buf := make([]byte, 32<<10)
		for {
			n, err := rd.Read(buf)
			if n > 0 {
				select {
				case ch <- append([]byte(nil), buf[:n]...):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

// hold reads from ch into g until the gate opens or fails, ch closes, or
// holdFor has passed, whichever comes first.
func hold(ch <-chan []byte, g *streamGate) {
	timer := time.NewTimer(holdFor)
	defer timer.Stop()
	for !g.open && g.fail == 0 {
		select {
		case b, ok := <-ch:
			if !ok {
				g.finish()
				return
			}
			g.feed(b)
		case <-timer.C:
			g.release()
			return
		}
	}
}

// streamGate decodes a streamed reply as it is read ahead. Before it opens,
// the events it decodes are held, and the bytes read are kept in raw; an
// error event before any body content sets fail. Once open, events go to sink.
type streamGate struct {
	dec      func(data string, emit func(Event)) error // the protocol's decoder
	sink     func(Event)                               // takes events once open
	open     bool
	raw      []byte  // bytes read before the gate opened, in order
	tail     []byte  // bytes of a line not yet complete
	data     []string // data lines of the event being read
	held     []Event
	fail     int    // status of an error that came before any body content; 0 if none
	failMsg  string // its message
	failData string // its data, as the upstream sent it
}

func newStreamGate(dec func(data string, emit func(Event)) error, sink func(Event)) *streamGate {
	if sink == nil {
		sink = func(Event) {}
	}
	return &streamGate{dec: dec, sink: sink}
}

// feed takes the next bytes the upstream sent.
func (g *streamGate) feed(b []byte) {
	if !g.open {
		g.raw = append(g.raw, b...)
	}
	g.tail = append(g.tail, b...)
	g.drain()
	if !g.open && g.fail == 0 && len(g.raw) > holdBytes {
		g.release()
	}
}

// drain reads the complete lines of tail, as readSSE does.
func (g *streamGate) drain() {
	for g.fail == 0 {
		i := bytes.IndexByte(g.tail, '\n')
		if i < 0 {
			return
		}
		line := strings.TrimSuffix(string(g.tail[:i]), "\r")
		g.tail = g.tail[i+1:]
		g.line(line)
	}
}

func (g *streamGate) line(line string) {
	switch {
	case line == "":
		if len(g.data) > 0 {
			data := strings.Join(g.data, "\n")
			g.data = nil
			_ = g.dec(data, func(ev Event) { g.emit(ev, data) })
		}
	case strings.HasPrefix(line, ":"), strings.HasPrefix(line, "event:"):
	case strings.HasPrefix(line, "data:"):
		g.data = append(g.data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
	}
}

// emit takes one decoded event: held until the gate opens, unless it is the
// error that fails the reply, or it opens the gate itself.
func (g *streamGate) emit(ev Event, data string) {
	if g.open {
		g.sink(ev)
		return
	}
	if ev.Kind == KError {
		if g.fail == 0 {
			g.fail, g.failMsg, g.failData = streamErrorStatus(data), ev.Text, data
		}
		return
	}
	g.held = append(g.held, ev)
	if bodyKind(ev.Kind) {
		g.release()
	}
}

// release lets the reply through: what was held goes to the sink now.
func (g *streamGate) release() {
	if g.open {
		return
	}
	g.open = true
	held := g.held
	g.held = nil
	for _, ev := range held {
		g.sink(ev)
	}
}

// finish is the upstream's end: an event or line it never closed is taken as
// it stood, and a reply that failed is left failed.
func (g *streamGate) finish() {
	if g.fail != 0 {
		return
	}
	if len(g.tail) > 0 {
		g.tail = append(g.tail, '\n')
		g.drain()
	}
	if g.fail == 0 {
		g.line("")
	}
	if g.fail == 0 {
		g.release()
	}
}

// bodyKind is whether an event is body content, which once sent cannot be
// taken back: text, thinking, or a tool call the reply has begun.
func bodyKind(k EventKind) bool {
	return k == KText || k == KThink || k == KToolStart || k == KToolArgs
}

// streamErrorTypes maps the names vendors give their stream errors to the
// status each stands for, when the error gives no numeric code.
var streamErrorTypes = map[string]int{
	"overloaded_error":      529,
	"overloaded":            529,
	"rate_limit_error":      429,
	"rate_limit_exceeded":   429,
	"insufficient_quota":    429,
	"authentication_error":  401,
	"permission_error":      403,
	"not_found_error":       404,
	"invalid_request_error": 400,
	"api_error":             500,
	"server_error":          500,
}

// streamErrorStatus is the status an error event stands for: the numeric
// code it gives, else what its type or code names, else 502.
func streamErrorStatus(data string) int {
	type errFields struct {
		Type string          `json:"type"`
		Code json.RawMessage `json:"code"`
	}
	var v struct {
		errFields
		Error    *errFields `json:"error"`
		Response struct {
			Error *errFields `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(data), &v); err != nil {
		return 502
	}
	e := v.Error
	if e == nil {
		e = v.Response.Error
	}
	if e == nil {
		e = &v.errFields
	}
	var n int
	if json.Unmarshal(e.Code, &n) == nil && n >= 400 && n <= 599 {
		return n
	}
	code := ""
	var s string
	if json.Unmarshal(e.Code, &s) == nil {
		code = s
		if n, err := strconv.Atoi(s); err == nil && n >= 400 && n <= 599 {
			return n
		}
	}
	for _, k := range []string{e.Type, code} {
		if st, ok := streamErrorTypes[k]; ok {
			return st
		}
	}
	return 502
}
