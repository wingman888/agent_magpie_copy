package gateway

// A reply that streams is held back until its first content. An error that
// comes before any of it — an Anthropic error event, a Chat error chunk, a
// Responses response.failed or error event — is the candidate's failure, as
// an error status would be, so another candidate can still take the request
// and the agent has seen none of the one that failed: not its message_start,
// not its pings. Once content has gone out, an error is the agent's to see.
// The wait is capped: past headWait, or headLimit bytes, the reply is passed
// on as it comes.

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// headWait and headLimit cap how long, and how much, a reply is held back
// waiting for its first content. The wait is checked as each piece of the
// reply arrives.
var (
	headWait  = 15 * time.Second
	headLimit = 1 << 20
)

// streamErr is an error a stream reported before any content, as the status
// it stands for.
type streamErr struct {
	status int
	msg    string
}

// openStream returns the reply's stream from its beginning once it has
// content, has ended, or has been held long enough; or, when the stream
// failed before any content, the failure, with nothing of the stream.
func openStream(rd io.Reader, proto provider.Protocol) (io.Reader, *streamErr) {
	started := time.Now()
	var held []byte // everything read so far
	var line []byte // the part of the current line read so far
	var f sseFrame  // the event being read
	buf := make([]byte, 32<<10)
	for {
		n, err := rd.Read(buf)
		held = append(held, buf[:n]...)
		rest := rd
		if err != nil {
			rest = errReader{err}
		}
		for _, b := range buf[:n] {
			if b != '\n' {
				line = append(line, b)
				continue
			}
			text := strings.TrimSuffix(string(line), "\r")
			line = line[:0]
			if text != "" {
				f.add(text)
				continue
			}
			bad, content := f.judge(proto)
			f = sseFrame{}
			if bad != nil {
				return nil, bad
			}
			if content {
				return io.MultiReader(bytes.NewReader(held), rest), nil
			}
		}
		if err != nil {
			if len(line) > 0 {
				f.add(string(line))
			}
			if f.event != "" || len(f.data) > 0 {
				if bad, _ := f.judge(proto); bad != nil {
					return nil, bad
				}
			}
			return io.MultiReader(bytes.NewReader(held), rest), nil
		}
		if time.Since(started) > headWait || len(held) > headLimit {
			return io.MultiReader(bytes.NewReader(held), rd), nil
		}
	}
}

// sseFrame is one event of a stream, as far as it has been read.
type sseFrame struct {
	event string
	data  []string
}

func (f *sseFrame) add(text string) {
	switch {
	case strings.HasPrefix(text, "event:"):
		f.event = strings.TrimSpace(strings.TrimPrefix(text, "event:"))
	case strings.HasPrefix(text, "data:"):
		f.data = append(f.data, strings.TrimPrefix(strings.TrimPrefix(text, "data:"), " "))
	}
}

// judge says what a whole event is: a failure, the first content of the
// reply (text, a tool call, reasoning), or neither.
func (f sseFrame) judge(proto provider.Protocol) (*streamErr, bool) {
	data := strings.TrimSpace(strings.Join(f.data, "\n"))
	if data == "" || data[0] != '{' {
		if f.event == "error" {
			return &streamErr{status: 500, msg: data}, false
		}
		return nil, false
	}
	var v struct {
		Type     string          `json:"type"`
		Error    json.RawMessage `json:"error"`
		Response struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
		Delta   json.RawMessage `json:"delta"`
		Choices []struct {
			Delta map[string]json.RawMessage `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(data), &v) != nil {
		return nil, false
	}
	failed := ""
	switch {
	case isSet(v.Error): // Anthropic's error event, a Chat error chunk
		failed = string(v.Error)
	case isSet(v.Response.Error): // Responses' response.failed
		failed = string(v.Response.Error)
	case v.Type == "error" || v.Type == "response.failed" || f.event == "error" || f.event == "response.failed":
		failed = data // Responses' error event: the whole of it says what failed
	}
	if failed != "" {
		return streamFailure(failed), false
	}
	switch proto {
	case provider.Chat:
		for _, c := range v.Choices {
			for _, k := range []string{"content", "reasoning_content", "reasoning"} {
				if hasText(c.Delta[k]) {
					return nil, true
				}
			}
			for _, k := range []string{"tool_calls", "function_call"} {
				if isSet(c.Delta[k]) {
					return nil, true
				}
			}
		}
		return nil, false
	case provider.Responses:
		return nil, strings.HasSuffix(v.Type, ".delta") && hasText(v.Delta)
	}
	return nil, v.Type == "content_block_start" || v.Type == "content_block_delta"
}

// streamFailure is what an error a stream reported before any content
// stands for, as a status: the vendor's error code or type says which, as
// its status code would. Whatever it says is not a status is a server error.
func streamFailure(raw string) *streamErr {
	var e struct {
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	}
	msg, kind := raw, ""
	if json.Unmarshal([]byte(raw), &e) == nil {
		if e.Message != "" {
			msg = e.Message
		}
		kind = e.Type + " " + strings.Trim(string(e.Code), `"`)
	}
	status := 500
	code, err := strconv.Atoi(strings.TrimSpace(strings.Trim(string(e.Code), `"`)))
	k := strings.ToLower(kind)
	switch {
	case err == nil && code >= 400 && code <= 599:
		status = code
	case strings.Contains(k, "overload"):
		status = 529
	case strings.Contains(k, "rate_limit"), strings.Contains(k, "rate limit"), strings.Contains(k, "quota"), strings.Contains(k, "too_many"):
		status = 429
	case strings.Contains(k, "authentication"), strings.Contains(k, "invalid_api_key"):
		status = 401
	case strings.Contains(k, "permission"):
		status = 403
	case strings.Contains(k, "not_found"):
		status = 404
	case strings.Contains(k, "invalid_request"):
		status = 400
	}
	return &streamErr{status: status, msg: msg}
}

// isSet is a JSON value that is there, and not null.
func isSet(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null"
}

// hasText is a JSON value that is set and is not an empty string.
func hasText(raw json.RawMessage) bool {
	return isSet(raw) && string(raw) != `""`
}

// errReader is a reader that only fails, with the error it was given.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
