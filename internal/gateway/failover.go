package gateway

// Failover beyond the first refusal: a stream that fails before it has said
// anything is as good as an error status, so it is held back until its
// first real content and dropped for the next candidate if it fails first;
// the last candidate left (or the only one) gets a couple more tries when
// what it failed with tends to pass; and an agent that hangs up ends the
// request without blaming anyone.

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// How long, and how much, a stream is held back waiting for its first
// content while another try may still take the request. Past either, it is
// passed on as it is.
var (
	preContentWait = 15 * time.Second
	preContentMax  = 1 << 20
)

// Retrying the last candidate: at most maxRetries more tries, waiting
// retryDelays between them unless the vendor said how long; a vendor that
// says longer than longestRetryWait isn't waited for.
var (
	maxRetries       = 2
	retryDelays      = []time.Duration{time.Second, 2 * time.Second}
	longestRetryWait = 8 * time.Second
	// retrySleep waits d, or until ctx ends; tests replace it.
	retrySleep = func(ctx context.Context, d time.Duration) error {
		if d <= 0 {
			return ctx.Err()
		}
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
)

// failCanceled: the agent went away; nobody is to blame.
const failCanceled = "canceled"

// statusCanceled is what a request the agent gave up on is recorded with.
const statusCanceled = 499

// retryWait says whether the last candidate is worth another try after
// failing this way, the n-th retry (0 first), and how long to wait first:
// a timeout, a server error or an overload may pass; a rate limit only when
// the vendor says when.
func retryWait(status int, h http.Header, n int) (time.Duration, bool) {
	if n >= maxRetries {
		return 0, false
	}
	ra, said := retryAfterHeader(h)
	switch status {
	case 408, 500, 502, 503, 504, 529:
	case 429:
		if !said {
			return 0, false
		}
	default:
		return 0, false
	}
	if said {
		if ra > longestRetryWait {
			return 0, false
		}
		return ra, true
	}
	if n < len(retryDelays) {
		return retryDelays[n], true
	}
	if len(retryDelays) > 0 {
		return retryDelays[len(retryDelays)-1], true
	}
	return 0, true
}

// retryAfterHeader reads Retry-After, in seconds or as a date.
func retryAfterHeader(h http.Header) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil && n >= 0 {
		return time.Duration(n * float64(time.Second)), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0), true
	}
	return 0, false
}

// unrest lifts a candidate's rest: it just answered.
func unrest(id string) {
	restingUntil.Lock()
	delete(restingUntil.m, id)
	delete(restingUntil.note, id)
	restingUntil.Unlock()
}

// holding reports whether w holds errors back for another try: only then
// is a stream held until its first content.
func holding(w http.ResponseWriter) bool {
	h, ok := w.(*holdWriter)
	return ok && h.hold
}

// What one streamed event is, before the first content.
const (
	evMeta    = iota // the reply's opening: message_start, ping, response.created…
	evContent        // the reply proper, or its end
	evError          // a failure
)

// classify tells one server-sent event of proto apart, and for an error,
// the status it stands for and its message.
func classify(proto provider.Protocol, event, data string) (kind, status int, msg string) {
	switch proto {
	case provider.Anthropic:
		var v struct {
			Type  string `json:"type"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		json.Unmarshal([]byte(data), &v)
		t := v.Type
		if t == "" {
			t = event
		}
		switch t {
		case "message_start", "ping", "":
			return evMeta, 0, ""
		case "error":
			msg = v.Error.Message
			if msg == "" {
				msg = data
			}
			return evError, errorStatus(v.Error.Type, msg, nil), msg
		}
		return evContent, 0, ""
	case provider.Chat:
		if strings.TrimSpace(data) == "[DONE]" {
			return evContent, 0, ""
		}
		var v struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Delta        map[string]json.RawMessage `json:"delta"`
				Message      map[string]json.RawMessage `json:"message"`
				FinishReason *string                    `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &v) != nil {
			return evMeta, 0, ""
		}
		if len(v.Error) > 0 && string(v.Error) != "null" {
			return chatError(v.Error)
		}
		for _, c := range v.Choices {
			if c.FinishReason != nil && *c.FinishReason != "" {
				return evContent, 0, ""
			}
			for _, d := range []map[string]json.RawMessage{c.Delta, c.Message} {
				for k, raw := range d {
					if k == "role" {
						continue
					}
					switch s := strings.TrimSpace(string(raw)); s {
					case "", "null", `""`, "[]", "{}":
					default:
						return evContent, 0, ""
					}
				}
			}
		}
		return evMeta, 0, ""
	case provider.Responses:
		var v struct {
			Type     string          `json:"type"`
			Code     json.RawMessage `json:"code"`
			Message  string          `json:"message"`
			Error    json.RawMessage `json:"error"`
			Response struct {
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
		}
		json.Unmarshal([]byte(data), &v)
		t := v.Type
		if t == "" {
			t = event
		}
		switch t {
		case "response.created", "response.in_progress", "response.queued", "":
			return evMeta, 0, ""
		case "response.failed":
			code, m := "", data
			if e := v.Response.Error; e != nil {
				code, m = e.Code, e.Message
			}
			return evError, errorStatus(code, m, nil), m
		case "error":
			code, m := strings.Trim(string(v.Code), `"`), v.Message
			if len(v.Error) > 0 && string(v.Error) != "null" {
				_, st, em := chatError(v.Error)
				if m == "" {
					m = em
				}
				if m == "" {
					m = data
				}
				return evError, st, m
			}
			if m == "" {
				m = data
			}
			return evError, errorStatus(code, m, nil), m
		}
		return evContent, 0, ""
	}
	return evContent, 0, ""
}

// chatError reads an OpenAI-style error object (or a bare string).
func chatError(raw json.RawMessage) (int, int, string) {
	var e struct {
		Message string          `json:"message"`
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
		Status  json.RawMessage `json:"status"`
	}
	if json.Unmarshal(raw, &e) != nil {
		var s string
		json.Unmarshal(raw, &s)
		if s == "" {
			s = string(raw)
		}
		return evError, errorStatus("", s, nil), s
	}
	msg := e.Message
	if msg == "" {
		msg = string(raw)
	}
	var codes []json.RawMessage
	if len(e.Code) > 0 {
		codes = append(codes, e.Code)
	}
	if len(e.Status) > 0 {
		codes = append(codes, e.Status)
	}
	kind := e.Type
	for _, c := range codes {
		if s := strings.Trim(string(c), `"`); s != "null" {
			kind += " " + s
		}
	}
	return evError, errorStatus(kind, msg, codes), msg
}

// errorStatus is the HTTP status a streamed error stands for: the one it
// names, else what its type or words say; a failure of no known kind is the
// vendor's (502).
func errorStatus(kind, msg string, codes []json.RawMessage) int {
	for _, c := range codes {
		if n, err := strconv.Atoi(strings.Trim(string(c), `"`)); err == nil && n >= 400 && n <= 599 {
			return n
		}
	}
	s := strings.ToLower(kind + " " + msg)
	switch {
	case strings.Contains(s, "overloaded"):
		return 529
	case strings.Contains(s, "insufficient_quota"), creditWords.MatchString(s) && !strings.Contains(s, "rate"):
		return 402
	case strings.Contains(s, "rate_limit"), strings.Contains(s, "rate limit"), strings.Contains(s, "too many requests"),
		strings.Contains(s, "resource_exhausted"), usedUpWords.MatchString(s):
		return 429
	case strings.Contains(s, "authentication"), strings.Contains(s, "unauthenticated"), strings.Contains(s, "invalid_api_key"):
		return 401
	case strings.Contains(s, "permission"):
		return 403
	case strings.Contains(s, "not_found"), strings.Contains(s, "model_not_found"):
		return 404
	case strings.Contains(s, "timeout"), strings.Contains(s, "timed out"):
		return 504
	case strings.Contains(s, "unavailable"):
		return 503
	case strings.Contains(s, "invalid_request"), strings.Contains(s, "invalid_prompt"), strings.Contains(s, "context_length"),
		strings.Contains(s, "request_too_large"):
		if modelWords.MatchString(s) {
			return 404
		}
		return 400
	case strings.Contains(s, "server_error"), strings.Contains(s, "api_error"), strings.Contains(s, "internal"):
		return 500
	}
	return 502
}

// eventOf is one SSE event's name and data, from its raw block.
func eventOf(block []byte) (event, data string) {
	var datas []string
	for _, line := range strings.Split(strings.ReplaceAll(string(block), "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			datas = append(datas, strings.TrimPrefix(line[5:], " "))
		}
	}
	return event, strings.Join(datas, "\n")
}

// nextBlock cuts the first complete SSE event off buf: its raw bytes, and
// how many bytes it took with the blank line ending it; 0 when none is
// complete yet.
func nextBlock(buf []byte) ([]byte, int) {
	for i := 0; i < len(buf); i++ {
		if buf[i] != '\n' {
			continue
		}
		// a blank line: "\n\n" or "\n\r\n"
		if i+1 < len(buf) && buf[i+1] == '\n' {
			return buf[:i], i + 2
		}
		if i+2 < len(buf) && buf[i+1] == '\r' && buf[i+2] == '\n' {
			return buf[:i], i + 3
		}
	}
	return nil, 0
}

// classifyBlock is classify, of one raw SSE event.
func classifyBlock(proto provider.Protocol, block []byte) (kind, status int, msg string) {
	event, data := eventOf(block)
	return classify(proto, event, data)
}
