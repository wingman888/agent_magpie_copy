package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// readSSE walks a server-sent event stream, calling fn with each event's
// name and data. Data lines of one event are joined with newlines.
func readSSE(r io.Reader, fn func(event, data string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 32<<20)
	var event string
	var data []string
	flush := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		err := fn(event, strings.Join(data, "\n"))
		event, data = "", nil
		return err
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return sc.Err()
}

// eventStreamLeads are the ways a server-sent event stream can open.
var eventStreamLeads = []string{"event:", "data:", ":"}

// eventStreamBody reports whether a provider reply is a server-sent event
// stream, and returns the reader to consume the body from. A Content-Type of
// text/event-stream settles it; otherwise the first bytes of the body decide,
// for upstreams that send no Content-Type. The returned reader replays any
// bytes that were peeked, so callers must read from it, not from res.Body.
func eventStreamBody(res *http.Response) (io.Reader, bool) {
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		return res.Body, true
	}
	br := bufio.NewReader(res.Body)
	return br, startsEventStream(br)
}

// startsEventStream peeks at the head of br, a byte at a time, until it can
// tell whether the body opens with an SSE field or comment.
func startsEventStream(br *bufio.Reader) bool {
	for n := 1; n <= 64; n++ {
		b, err := br.Peek(n)
		t := bytes.TrimLeft(b, " \t\r\n")
		undecided := len(t) == 0
		for _, lead := range eventStreamLeads {
			if bytes.HasPrefix(t, []byte(lead)) {
				return true
			}
			if len(t) < len(lead) && strings.HasPrefix(lead, string(t)) {
				undecided = true
			}
		}
		if !undecided || err != nil {
			return false
		}
	}
	return false
}

// sseWriter writes events to a client and flushes each one.
type sseWriter struct {
	w     http.ResponseWriter
	f     http.Flusher
	begun bool
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	f, _ := w.(http.Flusher)
	return &sseWriter{w: w, f: f}
}

func (s *sseWriter) begin() {
	if s.begun {
		return
	}
	s.begun = true
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	s.flush()
}

func (s *sseWriter) flush() {
	if s.f != nil {
		s.f.Flush()
	}
}

// event writes one event; a JSON-marshallable value or a raw string.
func (s *sseWriter) event(name string, v any) {
	s.begin()
	var b bytes.Buffer
	if name != "" {
		b.WriteString("event: ")
		b.WriteString(name)
		b.WriteByte('\n')
	}
	b.WriteString("data: ")
	switch x := v.(type) {
	case string:
		b.WriteString(x)
	case []byte:
		b.Write(x)
	case json.RawMessage:
		b.Write(x)
	default:
		j, _ := json.Marshal(x)
		b.Write(j)
	}
	b.WriteString("\n\n")
	s.w.Write(b.Bytes())
	s.flush()
}
