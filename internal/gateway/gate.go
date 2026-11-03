package gateway

// A streamed reply is held back until it says something the agent should
// see. The events before its first content (a message's start, pings) wait
// in the gate; an error event that comes before any content makes the reply
// a failed one, which the next candidate may answer, and nothing of it goes
// to the agent. Once content has gone out, errors pass as they come. A reply
// held past streamHold or streamHoldBytes is passed on as it stands.

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	streamHold      = 15 * time.Second
	streamHoldBytes = 1 << 20
)

// errStreamFailed stops reading a reply that failed before any content.
var errStreamFailed = errors.New("stream failed before any content")

// streamFail is an error event a reply said before any content.
type streamFail struct {
	status int
	msg    string
}

// streamFailStatus is the status a stream error before any content stands
// for, from the event's own words: overloaded and rate limited are what they
// say; anything else is the provider failing.
func streamFailStatus(data string) int {
	s := strings.ToLower(data)
	switch {
	case strings.Contains(s, "overloaded"):
		return 529
	case strings.Contains(s, "rate_limit") || strings.Contains(s, "rate limit"):
		return 429
	}
	return 502
}

// contentKind is whether an event carries what the reply says, as opposed to
// what it is about: a message's start, its usage, how it stopped.
func contentKind(k EventKind) bool {
	return k == KText || k == KThink || k == KToolStart || k == KToolArgs
}

// gate holds what a streamed reply writes to it until the reply has content,
// and then writes through. It stands in for the agent's writer while the
// reply's first events are read.
type gate struct {
	w       http.ResponseWriter
	f       http.Flusher
	started time.Time
	open    bool
	status  int
	held    bytes.Buffer
}

func newGate(w http.ResponseWriter) *gate {
	f, _ := w.(http.Flusher)
	return &gate{w: w, f: f, started: time.Now()}
}

func (g *gate) Header() http.Header { return g.w.Header() }

func (g *gate) WriteHeader(code int) {
	if g.open {
		g.w.WriteHeader(code)
		return
	}
	if g.status == 0 {
		g.status = code
	}
}

func (g *gate) Write(b []byte) (int, error) {
	if g.open {
		return g.w.Write(b)
	}
	return g.held.Write(b)
}

func (g *gate) Flush() {
	if g.open && g.f != nil {
		g.f.Flush()
	}
}

// settle lets the held reply go to the agent: it has said something, or it
// is too long to keep back, or it has ended.
func (g *gate) settle() {
	if g.open {
		return
	}
	g.open = true
	if g.status == 0 {
		g.status = http.StatusOK
	}
	g.w.WriteHeader(g.status)
	g.w.Write(g.held.Bytes())
	g.held.Reset()
	g.Flush()
}

// full is whether the gate has held the reply as long or as far as it will.
func (g *gate) full() bool {
	return !g.open && (time.Since(g.started) > streamHold || g.held.Len() > streamHoldBytes)
}

// writeSSE writes one event as the wire has it: its name, and its data one
// line at a time.
func writeSSE(w io.Writer, event, data string) {
	var b bytes.Buffer
	if event != "" {
		b.WriteString("event: " + event + "\n")
	}
	for _, line := range strings.Split(data, "\n") {
		b.WriteString("data: " + line + "\n")
	}
	b.WriteString("\n")
	w.Write(b.Bytes())
}

// sniffWriter passes what it writes on, to the sniffer first.
type sniffWriter struct {
	http.ResponseWriter
	sniff *usageSniffer
}

func (s sniffWriter) Write(b []byte) (int, error) {
	s.sniff.write(b)
	return s.ResponseWriter.Write(b)
}

func (s sniffWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
