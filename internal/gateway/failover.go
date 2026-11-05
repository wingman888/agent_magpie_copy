package gateway

// Failover, past the choice of candidate: the last candidate is tried again
// when its failure may heal by itself, and a streamed reply is held back
// until its first content, so that an error before any of it moves the
// request on as an ordinary failure does, with nothing of the failed
// candidate's reply reaching the agent.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// maxRetries is how many times the last candidate is tried again.
const maxRetries = 2

// retryAfterMax is the longest a vendor's Retry-After is waited out before
// trying again; a longer one is given to the agent as the error it is.
const retryAfterMax = 8 * time.Second

// retryDelays are the waits before the first and the second retry, when the
// vendor names none. Tests shorten them.
var retryDelays = [maxRetries]time.Duration{time.Second, 2 * time.Second}

// streamHoldFor and streamHoldSize bound how long, and how many bytes, a
// streamed reply is held back waiting for its first content. Past either,
// it is passed on as it comes.
var (
	streamHoldFor  = 15 * time.Second
	streamHoldSize = 1 << 20
)

// retryAfterOf is the Retry-After a reply carried, in seconds or as a date.
func retryAfterOf(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		return max(time.Duration(n*float64(time.Second)), 0), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

// retryDelay says whether the last candidate, having failed with status and
// header h after attempt retries, is tried again, and how long it waits
// first. A failure that is not one of those that heal, a 429 without a
// Retry-After, or a Retry-After longer than retryAfterMax, is not retried.
func retryDelay(status int, h http.Header, attempt int) (time.Duration, bool) {
	if attempt >= maxRetries {
		return 0, false
	}
	switch status {
	case 408, 500, 502, 503, 504, 529:
	case 429:
		if _, ok := retryAfterOf(h, time.Now()); !ok {
			return 0, false
		}
	default:
		return 0, false
	}
	d, ok := retryAfterOf(h, time.Now())
	if !ok {
		return retryDelays[attempt], true
	}
	if d > retryAfterMax {
		return 0, false
	}
	return d, true
}

// sleepCtx waits d, or until ctx ends; false when ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// clearRest lifts a candidate's rest: it has answered.
func clearRest(id string) {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	delete(restingUntil.m, id)
	delete(restingUntil.note, id)
}

// streamFailure is an error an upstream event stream gave before any of its
// content: the status it stands for, and its message.
type streamFailure struct {
	status int
	msg    string
}

// streamStatus is the status an error in an event stream stands for, by its
// kind and its words. Without a kind it is a 400, which then counts by its
// words alone (see retryable).
func streamStatus(kind, msg string) int {
	k := strings.ToLower(kind)
	switch {
	case strings.Contains(k, "overload"), strings.Contains(strings.ToLower(msg), "overloaded"):
		return 529
	case strings.Contains(k, "rate_limit"), strings.Contains(k, "ratelimit"), strings.Contains(k, "rate limit"):
		return 429
	case strings.Contains(k, "insufficient_quota"), strings.Contains(k, "billing"):
		return 402
	case strings.Contains(k, "timeout"):
		return 408
	case strings.Contains(k, "server_error"), strings.Contains(k, "api_error"), strings.Contains(k, "internal"):
		return 500
	case strings.Contains(k, "authentication"):
		return 401
	case strings.Contains(k, "permission"):
		return 403
	case strings.Contains(k, "not_found"):
		return 404
	}
	return 400
}

// pumped is one read from an upstream body, or the error that ended it.
type pumped struct {
	b   []byte
	err error
}

// chanReader reads what a pump sent, in order; with err set it only returns
// that error.
type chanReader struct {
	ch  <-chan pumped
	buf []byte
	err error
}

func (c *chanReader) Read(p []byte) (int, error) {
	for len(c.buf) == 0 {
		if c.err != nil {
			return 0, c.err
		}
		m, ok := <-c.ch
		if !ok {
			c.err = io.EOF
			continue
		}
		c.buf, c.err = m.b, m.err
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

// gate looks at the records of an event stream as they arrive, and keeps
// every byte read so far.
type gate struct {
	proto   provider.Protocol
	raw     bytes.Buffer
	pending []byte // the bytes after the last complete record
}

const (
	gateHold = iota // no content yet, keep holding
	gatePass        // content, or a limit, or an error to hand on: pass on
	gateFail        // an error before any content, which another candidate may answer
)

// feed reads more of the stream and says what it means so far.
func (g *gate) feed(b []byte) (int, *streamFailure) {
	g.raw.Write(b)
	g.pending = append(g.pending, b...)
	for {
		end, sep := recordEnd(g.pending)
		if end < 0 {
			break
		}
		rec := g.pending[:end]
		g.pending = g.pending[end+sep:]
		kind, status, msg := classifyRecord(g.proto, rec)
		switch kind {
		case recContent:
			return gatePass, nil
		case recError:
			if !retryable(status, []byte(msg)) {
				return gatePass, nil // the request's own fault: the agent gets it
			}
			return gateFail, &streamFailure{status: status, msg: msg}
		}
	}
	if g.raw.Len() >= streamHoldSize {
		return gatePass, nil
	}
	return gateHold, nil
}

// holdStream reads the start of an upstream event stream until its first
// content, or an error that another candidate may answer, or the limits of
// streamHoldFor and streamHoldSize. It returns the reader to pass on, with
// everything read so far first; stop must be called when done with it. On
// a failure it returns no reader, and nothing has been read past it.
func holdStream(rd io.Reader, proto provider.Protocol) (io.Reader, func(), *streamFailure) {
	quit := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(quit) }) }
	chunks := make(chan pumped)
	go func() {
		defer close(chunks)
		for {
			buf := make([]byte, 32<<10)
			n, err := rd.Read(buf)
			if n > 0 {
				select {
				case chunks <- pumped{b: buf[:n]}:
				case <-quit:
					return
				}
			}
			if err != nil {
				select {
				case chunks <- pumped{err: err}:
				case <-quit:
				}
				return
			}
		}
	}()
	g := &gate{proto: proto}
	timer := time.NewTimer(streamHoldFor)
	defer timer.Stop()
	pass := func(rest io.Reader) (io.Reader, func(), *streamFailure) {
		return io.MultiReader(bytes.NewReader(g.raw.Bytes()), rest), stop, nil
	}
	for {
		select {
		case c, ok := <-chunks:
			if !ok {
				return pass(&chanReader{err: io.EOF})
			}
			if c.err != nil {
				return pass(&chanReader{err: c.err})
			}
			switch v, fail := g.feed(c.b); v {
			case gateFail:
				stop()
				return nil, stop, fail
			case gatePass:
				return pass(&chanReader{ch: chunks})
			}
		case <-timer.C:
			return pass(&chanReader{ch: chunks})
		}
	}
}

// Record kinds in an event stream, as far as failover cares.
const (
	recNeutral = iota
	recContent
	recError
)

// recordEnd finds the end of the first complete record in b, and the length
// of its separator; -1 when there is none yet.
func recordEnd(b []byte) (int, int) {
	for i := 0; i+1 < len(b); i++ {
		if b[i] != '\n' {
			continue
		}
		if b[i+1] == '\n' {
			return i, 2
		}
		if b[i+1] == '\r' && i+2 < len(b) && b[i+2] == '\n' {
			return i, 3
		}
	}
	return -1, 0
}

// sseParts splits a record into its event name and its data.
func sseParts(rec []byte) (string, string) {
	var event string
	var data []string
	for _, line := range strings.Split(string(rec), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[len("data:"):], " "))
		}
	}
	return event, strings.Join(data, "\n")
}

// classifyRecord says what one record of a stream is: content, an error and
// the status and message it stands for, or neither.
func classifyRecord(proto provider.Protocol, rec []byte) (int, int, string) {
	event, data := sseParts(rec)
	if data == "" || data == "[DONE]" {
		return recNeutral, 0, ""
	}
	switch proto {
	case provider.Chat:
		return classifyChatRecord(data)
	case provider.Responses:
		return classifyResponsesRecord(data)
	}
	return classifyAnthropicRecord(event, data)
}

func classifyAnthropicRecord(event, data string) (int, int, string) {
	var v struct {
		Type  string `json:"type"`
		Delta struct {
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
			Thinking    string `json:"thinking"`
		} `json:"delta"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(data), &v) != nil {
		return recNeutral, 0, ""
	}
	if event == "error" || v.Type == "error" {
		kind, msg := "", data
		if v.Error != nil {
			kind, msg = v.Error.Type, v.Error.Message
		}
		return recError, streamStatus(kind, msg), msg
	}
	if v.Type == "content_block_delta" && (v.Delta.Text != "" || v.Delta.PartialJSON != "" || v.Delta.Thinking != "") {
		return recContent, 0, ""
	}
	return recNeutral, 0, ""
}

func classifyChatRecord(data string) (int, int, string) {
	var v struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Delta struct {
				Content          json.RawMessage `json:"content"`
				ReasoningContent json.RawMessage `json:"reasoning_content"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(data), &v) != nil {
		return recNeutral, 0, ""
	}
	if e := bytes.TrimSpace(v.Error); len(e) > 0 && string(e) != "null" {
		var x struct {
			Message string          `json:"message"`
			Type    string          `json:"type"`
			Code    json.RawMessage `json:"code"`
		}
		msg := string(e)
		if json.Unmarshal(e, &x) == nil && x.Message != "" {
			msg = x.Message
		}
		status := streamStatus(x.Type, msg)
		if n, err := strconv.Atoi(strings.Trim(string(x.Code), `"`)); err == nil && n >= 400 && n < 600 {
			status = n
		}
		return recError, status, msg
	}
	for _, c := range v.Choices {
		d := c.Delta
		if nonEmptyJSON(d.Content) || nonEmptyJSON(d.ReasoningContent) || nonEmptyJSON(d.ToolCalls) {
			return recContent, 0, ""
		}
	}
	return recNeutral, 0, ""
}

func classifyResponsesRecord(data string) (int, int, string) {
	type respError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Type    string `json:"type"`
	}
	var v struct {
		Type     string          `json:"type"`
		Delta    json.RawMessage `json:"delta"`
		Code     string          `json:"code"`
		Message  string          `json:"message"`
		Error    *respError      `json:"error"`
		Response *struct {
			Error *respError `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(data), &v) != nil {
		return recNeutral, 0, ""
	}
	if v.Type == "error" || v.Type == "response.failed" {
		e := v.Error
		if e == nil && v.Response != nil {
			e = v.Response.Error
		}
		kind, msg := v.Code, v.Message
		if e != nil {
			kind, msg = e.Code+" "+e.Type, e.Message
		}
		if msg == "" {
			msg = data
		}
		return recError, streamStatus(kind, msg), msg
	}
	if strings.HasSuffix(v.Type, ".delta") {
		var s string
		if json.Unmarshal(v.Delta, &s) == nil && s != "" {
			return recContent, 0, ""
		}
	}
	return recNeutral, 0, ""
}

// nonEmptyJSON is whether a raw JSON value carries anything.
func nonEmptyJSON(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != `""` && s != "[]"
}
