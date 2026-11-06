package gateway

// A streamed reply that fails before any of its content has reached the
// agent is a failed try like any other: the next candidate may answer, or,
// with none left, the error goes out with its status. So the opening events
// are held back until one of them carries content, and an agent never sees
// a dropped candidate's events, message_start and pings included. Once
// content is held, or the wait or the size passes its limit, the reply is
// relayed as it comes, and an error after that is the agent's to see.

import (
	"bufio"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	firstWait = 15 * time.Second // how long opening events are held back
	firstCap  = 1 << 20          // and how many bytes of them
)

// rawEvent is one server-sent event, as it came.
type rawEvent struct {
	raw  []byte // every byte, the blank line that ends it included
	name string // its event: field
	data string // its data: fields, joined by newlines
	seen bool   // whether it had any field
}

// readRawEvent reads the next event from br. At the end of the stream it
// returns io.EOF, with the event that was cut short if there was one.
func readRawEvent(br *bufio.Reader) (rawEvent, error) {
	var ev rawEvent
	var data []string
	for {
		line, err := br.ReadString('\n')
		ev.raw = append(ev.raw, line...)
		l := strings.TrimRight(line, "\r\n")
		if l == "" && err == nil {
			if ev.seen {
				break
			}
			continue
		}
		switch {
		case strings.HasPrefix(l, "event:"):
			ev.name, ev.seen = strings.TrimSpace(l[len("event:"):]), true
		case strings.HasPrefix(l, "data:"):
			data = append(data, strings.TrimPrefix(l[len("data:"):], " "))
			ev.seen = true
		}
		if err != nil {
			ev.data = strings.Join(data, "\n")
			return ev, err
		}
	}
	ev.data = strings.Join(data, "\n")
	return ev, nil
}

// verdict is what an event means to a reply that has sent nothing yet.
type verdict int

const (
	waiting verdict = iota // nothing of the reply's content yet
	content                // the reply's content has begun
	failed                 // the upstream failed before any content
)

// judge reads one event. A failure carries the status it stands for and the
// upstream's words.
func judge(ev rawEvent) (verdict, int, string) {
	if !ev.seen || ev.data == "[DONE]" {
		return waiting, 0, ""
	}
	var v map[string]any
	if json.Unmarshal([]byte(ev.data), &v) != nil {
		if ev.name == "error" {
			return failed, 502, ev.data
		}
		return waiting, 0, ""
	}
	typ, _ := v["type"].(string)
	if ev.name == "error" || typ == "error" || typ == "response.failed" || v["error"] != nil {
		status, msg := errorOf(v, ev.data)
		return failed, status, msg
	}
	if hasContent(typ, v) {
		return content, 0, ""
	}
	return waiting, 0, ""
}

// hasContent reports whether an event carries the reply's text, or a call
// it is making: Anthropic's deltas, Responses' output deltas, Chat's deltas.
func hasContent(typ string, v map[string]any) bool {
	switch {
	case typ == "content_block_delta": // Anthropic
		return true
	case strings.HasPrefix(typ, "response.") && strings.HasSuffix(typ, ".delta"): // Responses
		d, _ := v["delta"].(string)
		return d != ""
	}
	choices, _ := v["choices"].([]any) // Chat Completions
	if len(choices) == 0 {
		return false
	}
	c, _ := choices[0].(map[string]any)
	d, _ := c["delta"].(map[string]any)
	for _, k := range []string{"content", "tool_calls", "reasoning_content"} {
		if x := d[k]; x != nil && x != "" {
			return true
		}
	}
	return false
}

// errorOf finds an error event's message, and the status it stands for.
func errorOf(v map[string]any, raw string) (int, string) {
	e, _ := v["error"].(map[string]any)
	if r, ok := v["response"].(map[string]any); e == nil && ok { // Responses' response.failed
		e, _ = r["error"].(map[string]any)
	}
	if e == nil {
		e = v
	}
	msg, _ := e["message"].(string)
	if msg == "" {
		msg = raw
	}
	return errorStatus(e), msg
}

// streamErrors maps the names vendors give their stream errors to statuses.
var streamErrors = map[string]int{
	"overloaded_error":      529,
	"overloaded":            529,
	"rate_limit_error":      429,
	"rate_limit_exceeded":   429,
	"rate_limit":            429,
	"insufficient_quota":    402,
	"authentication_error":  401,
	"invalid_api_key":       401,
	"permission_error":      403,
	"not_found_error":       404,
	"model_not_found":       404,
	"invalid_request_error": 400,
	"invalid_request":       400,
	"api_error":             500,
	"server_error":          500,
	"internal_error":        500,
	"internal_server_error": 500,
}

// errorStatus is the status an error event stands for: its own code when that
// is an HTTP status, else the name its type or code gives; 502 when neither says.
func errorStatus(e map[string]any) int {
	code, typ := textOf(e["code"]), textOf(e["type"])
	if n, err := strconv.Atoi(code); err == nil && n >= 400 && n <= 599 {
		return n
	}
	for _, name := range []string{code, typ} {
		if s, ok := streamErrors[name]; ok {
			return s
		}
	}
	return 502
}

func textOf(x any) string {
	switch t := x.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

// streamFail is a reply that failed before its content: the status it stands
// for, and the upstream's words.
type streamFail struct {
	status int
	msg    string
}

type rawItem struct {
	ev  rawEvent
	err error
}

// streamed is a reply from its first event on: the events held back before
// the decision first, then the rest as the body yields them.
type streamed struct {
	head []byte        // held back, read first
	end  error         // the error the body ended with, once its events are read
	cur  []byte        // the bytes of an event being read
	ch   chan rawItem  // events as the pump reads them
	done chan struct{} // closed when the reader is finished with the body
}

// pump reads the body's events in its own goroutine, so the wait for the
// first content can be timed.
func (s *streamed) pump(br *bufio.Reader) {
	for {
		ev, err := readRawEvent(br)
		select {
		case s.ch <- rawItem{ev, err}:
		case <-s.done:
			return
		}
		if err != nil {
			return
		}
	}
}

// Read gives the reply's bytes, as they came.
func (s *streamed) Read(p []byte) (int, error) {
	if len(s.head) > 0 {
		n := copy(p, s.head)
		s.head = s.head[n:]
		return n, nil
	}
	for len(s.cur) == 0 {
		if s.end != nil {
			return 0, s.end
		}
		it := <-s.ch
		s.cur, s.end = it.ev.raw, it.err
	}
	n := copy(p, s.cur)
	s.cur = s.cur[n:]
	return n, nil
}

// Close stops reading the body early, when the reply is dropped or done with.
func (s *streamed) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}

// gateStream holds a streamed reply's opening events back until one of them
// carries content. It returns the reply to relay, or the failure that came
// before any content, in which case nothing of the reply may be written.
func gateStream(body io.Reader) (*streamed, *streamFail) {
	s := &streamed{ch: make(chan rawItem), done: make(chan struct{})}
	go s.pump(bufio.NewReaderSize(body, 4<<10))
	wait := time.NewTimer(firstWait)
	defer wait.Stop()
	var held []byte
	for {
		select {
		case it := <-s.ch:
			held = append(held, it.ev.raw...)
			if it.err != nil && it.err != io.EOF {
				s.Close()
				return nil, &streamFail{502, it.err.Error()}
			}
			v, status, msg := judge(it.ev)
			if v == failed {
				s.Close()
				return nil, &streamFail{status, msg}
			}
			if v == content || it.err != nil || len(held) > firstCap {
				s.head, s.end = held, it.err
				return s, nil
			}
		case <-wait.C:
			s.head = held
			return s, nil
		}
	}
}
