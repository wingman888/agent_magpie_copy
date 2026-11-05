package gateway

// The first bytes of a stream. An upstream may open a stream and fail it
// before a word of the reply is out: Anthropic's overloaded error after
// message_start and ping, a Chat error chunk, a Responses response.failed.
// The gateway reads a stream up to its first content before it sends the
// agent anything, so such a stream can still be given up for another
// candidate, and the agent never sees its message id. The wait is bounded:
// past firstByteWait or firstByteLimit, what was read goes out and the rest
// is relayed as it comes.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

var (
	firstByteWait  = 15 * time.Second // how long a stream may hold back its first content
	firstByteLimit = 1 << 20          // and how many bytes it may hold back
)

// errFound stops the event walk once a stream's first content or error is in.
var errFound = errors.New("found")

// streamFault is an error a stream gave before any content: the status it
// stands for, and what it said.
type streamFault struct {
	status int
	msg    string
}

type readChunk struct {
	b   []byte
	err error
}

// pump reads r on its own goroutine, so a wait for its bytes can give up.
// It stops when done closes.
func pump(r io.Reader, done <-chan struct{}) <-chan readChunk {
	ch := make(chan readChunk)
	go func() {
		defer close(ch)
		for {
			buf := make([]byte, 32<<10)
			n, err := r.Read(buf)
			if n > 0 || err != nil {
				select {
				case ch <- readChunk{buf[:n], err}:
				case <-done:
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

// chanReader reads what pump has read, in order.
type chanReader struct {
	ch  <-chan readChunk
	buf []byte
	err error
}

func (c *chanReader) Read(p []byte) (int, error) {
	for len(c.buf) == 0 {
		if c.err != nil {
			return 0, c.err
		}
		ch, ok := <-c.ch
		if !ok {
			c.err = io.EOF
			continue
		}
		c.buf, c.err = ch.b, ch.err
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

// awaitContent reads a stream up to its first content and returns the reader
// to relay from, with what was read included. When an error came first, it
// returns the fault and no reader: none of that stream is to be sent on.
func awaitContent(rd io.Reader, done <-chan struct{}) (io.Reader, *streamFault) {
	ch := pump(rd, done)
	cr := &chanReader{ch: ch}
	timer := time.NewTimer(firstByteWait)
	defer timer.Stop()
	var held []byte
	checked := 0 // held[:checked] holds complete events already read
	for {
		var c readChunk
		select {
		case <-timer.C:
			return io.MultiReader(bytes.NewReader(held), cr), nil
		case got, ok := <-ch:
			if !ok {
				got = readChunk{err: io.EOF}
			}
			c = got
		}
		held = append(held, c.b...)
		var fault *streamFault
		content := false
		if cut := eventsEnd(held); cut > checked {
			readSSE(bytes.NewReader(held[checked:cut]), func(event, data string) error {
				switch verdict, f := classifyEvent(event, data); verdict {
				case verdictContent:
					content = true
					return errFound
				case verdictError:
					fault = f
					return errFound
				}
				return nil
			})
			checked = cut
		}
		if fault != nil {
			return nil, fault
		}
		if content || c.err != nil || len(held) > firstByteLimit {
			return io.MultiReader(bytes.NewReader(held), cr), nil
		}
	}
}

// eventsEnd is where the last complete event in held ends, or -1 when none is.
func eventsEnd(held []byte) int {
	cut := -1
	if i := bytes.LastIndex(held, []byte("\n\n")); i >= 0 {
		cut = i + 2
	}
	if j := bytes.LastIndex(held, []byte("\n\r\n")); j >= 0 && j+3 > cut {
		cut = j + 3
	}
	return cut
}

const (
	verdictOpen = iota // nothing conclusive yet
	verdictContent
	verdictError
)

// streamErr is the error part of a stream event, in any of the three APIs:
// Anthropic's and Chat's error object, or Responses' error event and
// response.failed.
type streamErr struct {
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
}

// classifyEvent says whether one stream event is the first content of the
// reply, an error that ends it before any content, or neither.
func classifyEvent(event, data string) (int, *streamFault) {
	// message is raw: message_start carries an object there, which must not
	// stop the rest of the event from being read
	var v struct {
		Type     string          `json:"type"`
		Code     json.RawMessage `json:"code"`
		Message  json.RawMessage `json:"message"`
		Error    *streamErr      `json:"error"`
		Response *struct {
			Error *streamErr `json:"error"`
		} `json:"response"`
		Choices []struct {
			Delta struct {
				Content          string            `json:"content"`
				ReasoningContent string            `json:"reasoning_content"`
				ToolCalls        []json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(data), &v) != nil {
		return verdictOpen, nil
	}
	isError := v.Error != nil || event == "error" || v.Type == "error" || v.Type == "response.failed"
	if isError {
		var message string
		json.Unmarshal(v.Message, &message)
		e := streamErr{Type: v.Type, Code: v.Code, Message: message}
		if v.Error != nil {
			e = *v.Error
		} else if v.Response != nil && v.Response.Error != nil {
			e = *v.Response.Error
		}
		return verdictError, faultOf(e, data)
	}
	if v.Type == "content_block_delta" || strings.HasSuffix(v.Type, ".delta") {
		return verdictContent, nil
	}
	for _, c := range v.Choices {
		if c.Delta.Content != "" || c.Delta.ReasoningContent != "" || len(c.Delta.ToolCalls) > 0 {
			return verdictContent, nil
		}
	}
	return verdictOpen, nil
}

// faultOf names the status an error event stands for: a numeric code when it
// has one, else what its type or code says.
func faultOf(e streamErr, data string) *streamFault {
	code := strings.Trim(string(e.Code), `"`)
	status := 0
	if n := atoiStatus(code); n >= 400 && n <= 599 {
		status = n
	}
	if status == 0 {
		status = errorStatus(e.Type + " " + code)
	}
	msg := e.Message
	if msg == "" {
		msg = data
	}
	if e.Type != "" {
		msg = e.Type + ": " + msg
	}
	return &streamFault{status: status, msg: msg}
}

func atoiStatus(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// errorStatus is the HTTP status an error of this type or code stands for.
func errorStatus(text string) int {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "overloaded"):
		return 529
	case strings.Contains(t, "rate_limit"), strings.Contains(t, "rate limit"), strings.Contains(t, "quota"), strings.Contains(t, "insufficient"):
		return 429
	case strings.Contains(t, "authentication"), strings.Contains(t, "invalid_api_key"):
		return 401
	case strings.Contains(t, "permission"):
		return 403
	case strings.Contains(t, "not_found"), strings.Contains(t, "not found"):
		return 404
	case strings.Contains(t, "invalid_request"), strings.Contains(t, "invalid request"):
		return 400
	}
	return 500
}
