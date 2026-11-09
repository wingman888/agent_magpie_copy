package gateway

// The opening of a streamed reply is what a provider sends before its first
// text: headers, message_start, pings. An error event in that opening is a
// failure of the candidate, just as a refused request is: the agent has been
// sent nothing, so another candidate may answer, or the last one may be tried
// again. See holdWriter.openStream.

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// streamStatus is the HTTP status a typed error event stands for. Types and
// codes not listed stand for a 502, which another provider may answer.
var streamStatus = map[string]int{
	"overloaded_error":        529,
	"overloaded":              529,
	"rate_limit_error":        429,
	"rate_limit_exceeded":     429,
	"rate_limited":            429,
	"insufficient_quota":      429,
	"authentication_error":    401,
	"invalid_api_key":         401,
	"permission_error":        403,
	"not_found_error":         404,
	"model_not_found":         404,
	"timeout_error":           408,
	"api_error":               500,
	"server_error":            500,
	"internal_error":          500,
	"invalid_request_error":   400,
	"invalid_request":         400,
	"invalid_prompt":          400,
	"context_length_exceeded": 400,
}

// streamFailure is the status an error event before any text stands for, and
// the message the agent is to see for it.
func streamFailure(p provider.Provider, data string) (int, string) {
	typ, code := streamErrorKind(data)
	status := 502
	for _, k := range []string{typ, code} {
		if s, ok := streamStatus[k]; ok {
			status = s
			break
		}
	}
	return status, p.Name + ": " + provider.APIError([]byte(data), "stream error")
}

// streamErrorKind is the type and code an error event names: its error
// object's, or the event's own for the shape Responses uses.
func streamErrorKind(data string) (typ, code string) {
	var v struct {
		Type     string          `json:"type"`
		Code     json.RawMessage `json:"code"`
		Error    json.RawMessage `json:"error"`
		Response struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(data), &v) != nil {
		return "", ""
	}
	for _, raw := range []json.RawMessage{v.Error, v.Response.Error} {
		var e struct {
			Type string          `json:"type"`
			Code json.RawMessage `json:"code"`
		}
		if len(raw) > 0 && json.Unmarshal(raw, &e) == nil && (e.Type != "" || len(e.Code) > 0) {
			return e.Type, rawString(e.Code)
		}
	}
	return v.Type, rawString(v.Code)
}

// rawString is a JSON string's text, or a number's or nothing's as written.
func rawString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return string(raw)
}

// startsText says an event is output the agent would read: text, thinking,
// or a tool call. Those are what ends an opening.
func startsText(ev Event) bool {
	switch ev.Kind {
	case KText, KThink, KToolArgs:
		return ev.Text != ""
	case KToolStart:
		return true
	}
	return false
}

// relayWatch reads a streamed reply's events as the raw bytes pass to the
// agent, until its text begins or it fails before then. The events are read
// with the protocol's own decoder; the bytes themselves are not changed.
type relayWatch struct {
	hw      *holdWriter
	p       provider.Provider
	from    provider.Protocol // whose error shape the agent is answered in
	dec     func(data string, emit func(Event)) error
	pending []byte // the unfinished event, without carriage returns
	done    bool   // text has begun, or the reply has failed: nothing more to read
	status  int    // when it failed before its text: the status it stands for
	msg     string
}

func newRelayWatch(hw *holdWriter, p provider.Provider, proto provider.Protocol) *relayWatch {
	return &relayWatch{hw: hw, p: p, from: proto, dec: decoder(proto)}
}

// write reads raw bytes as they pass. It reports true when the reply has
// failed before its text, so the caller stops reading it.
func (rw *relayWatch) write(b []byte) bool {
	if rw.done {
		return false
	}
	for _, c := range b {
		if c != '\r' {
			rw.pending = append(rw.pending, c)
		}
	}
	for !rw.done {
		i := bytes.Index(rw.pending, []byte("\n\n"))
		if i < 0 {
			return false
		}
		block := string(rw.pending[:i])
		rw.pending = rw.pending[i+2:]
		if rw.event(sseData(block)) {
			return true
		}
	}
	return false
}

// event reads one event's data. It reports true when the event fails the reply.
func (rw *relayWatch) event(data string) bool {
	if data == "" {
		return false
	}
	failed := false
	rw.dec(data, func(ev Event) {
		if rw.done || failed {
			return
		}
		switch {
		case ev.Kind == KError:
			rw.done = true
			st, msg := streamFailure(rw.p, data)
			if rw.hw.streamFail(rw.from, st, msg) {
				failed = true
				rw.status, rw.msg = st, msg
			}
		case startsText(ev):
			rw.done = true
			rw.hw.content()
		}
	})
	return failed
}

// sseData is the data of one server-sent event block, its lines joined.
func sseData(block string) string {
	var data []string
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	return strings.Join(data, "\n")
}

// errReplyStopped ends the reading of a reply whose opening has failed: what
// follows the error is not the agent's to see.
var errReplyStopped = errors.New("reply stopped before its text")
