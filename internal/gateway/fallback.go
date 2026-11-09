package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// target is one provider and the model magpie asks it for.
type target struct {
	p     provider.Provider
	model string
}

// result is how a request routed through its targets ended.
type result struct {
	to     provider.Protocol
	status int
	text   string
	chain  string // candidates that failed before the end, for the call record
	end    target // the target that ended the request
}

// outcome is one provider's answer. Nothing is written to the client unless
// written is set, so the caller can still move the request on.
type outcome struct {
	to       provider.Protocol
	status   int
	text     string
	failed   []string // keys of this provider that were passed over
	lastFail string   // the key that failed last, set with retry
	retry    bool     // the failure may move the request to the next target
	written  bool     // the reply succeeded and is already on the wire
}

// targets is the primary target followed by the provider's fallback list, in
// order. Entries that cannot be read, name no ready provider, or name a
// Claude subscription (which answers on its own, outside routing) are skipped
// silently. Targets whose provider failed a moment ago go after the others.
func (s *Server) targets(p provider.Provider, model string) []target {
	ts := []target{{p, model}}
	for _, e := range p.Fallback {
		pid, m, ok := strings.Cut(strings.TrimSpace(e), "/")
		m = strings.TrimSpace(m)
		if !ok || m == "" {
			continue
		}
		fp, err := provider.Find(pid)
		if err != nil || !fp.Ready() || (fp.Account != nil && fp.Account.Agent == "claude") {
			continue
		}
		if fp.ID == p.ID && m == model {
			continue
		}
		ts = append(ts, target{*fp, m})
	}
	var fresh, rested []target
	for _, t := range ts {
		if s.restingProvider(t.p.ID) {
			rested = append(rested, t)
		} else {
			fresh = append(fresh, t)
		}
	}
	return append(fresh, rested...)
}

// restingProvider reports whether a provider failed over to another target
// within the last keyRest.
func (s *Server) restingProvider(pid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().Before(s.resting[pid+"\x00"])
}

// restProvider starts the rest period of a whole provider. The key is the
// provider id with an empty key ref, which no real key can have.
func (s *Server) restProvider(pid string) { s.rest(pid, "") }

// routeAll tries the targets in order until one answers. A failure that
// retryable accepts moves on while nothing has been written to the client.
// The last target's failure is the answer, written in the client's shape.
func (s *Server) routeAll(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte, ts []target, u *Usage) result {
	var chain []string
	for i, t := range ts {
		o := s.try(w, r, t.p, from, t.model, body, u)
		chain = append(chain, o.failed...)
		res := result{to: o.to, status: o.status, text: o.text, end: t}
		if o.written {
			res.chain = strings.Join(chain, "; ")
			return res
		}
		if i == len(ts)-1 || !o.retry || r.Context().Err() != nil {
			res.status = writeError(w, from, o.status, o.text)
			res.chain = strings.Join(chain, "; ")
			return res
		}
		chain = append(chain, o.lastFail)
		s.restProvider(t.p.ID)
	}
	return result{to: from, status: 502, text: "no target tried", end: ts[len(ts)-1]}
}

// try sends a request through one provider's keys, best first. A key whose
// failure is retryable hands over to the next key. The provider's failure
// is returned unwritten, so routeAll can decide what happens next.
func (s *Server) try(w http.ResponseWriter, r *http.Request, p provider.Provider, from provider.Protocol, model string, body []byte, u *Usage) outcome {
	if len(p.Speaks()) == 0 {
		return outcome{to: from, status: 502, text: p.Name + " has no endpoint configured"}
	}
	choices := s.restedFirst(p.ID, p.KeyChoices(model, from))
	if len(choices) == 0 {
		return outcome{to: from, status: 502, text: p.Name + " has no key for this endpoint"}
	}
	var failed []string
	for i, c := range choices {
		pc := p.Using(c)
		up := s.open(r, pc, from, model, body)
		if up.bad != "" {
			return outcome{to: from, status: 400, text: up.bad, failed: failed}
		}
		code, text := 0, ""
		switch {
		case up.err != nil:
			code, text = 502, p.Name+": "+up.err.Error()
		case up.res.StatusCode >= 400:
			b, _ := io.ReadAll(io.LimitReader(up.res.Body, 1<<20))
			up.res.Body.Close()
			code, text = up.res.StatusCode, p.Name+": "+provider.APIError(b, up.res.Status)
		case streamOf(body):
			// a failure may come as the first event of a 200 stream
			if c2, t2, bad := startFailure(up.res); bad {
				up.res.Body.Close()
				code, text = c2, p.Name+": "+t2
			}
		}
		if code == 0 {
			code, text = s.finish(w, up, p.Name, from, u)
			return outcome{to: up.to, status: code, text: text, failed: failed, written: true}
		}
		if r.Context().Err() != nil || !retryable(code, text) {
			return outcome{to: up.to, status: code, text: text, failed: failed}
		}
		s.rest(p.ID, c.Ref)
		name := candidateName(p, c) + ": " + text
		if i == len(choices)-1 {
			return outcome{to: up.to, status: code, text: text, failed: failed, lastFail: name, retry: true}
		}
		failed = append(failed, name)
	}
	return outcome{to: from, status: 502, text: "no key tried"}
}

// replayBody is a reply body whose first bytes were read ahead.
type replayBody struct {
	io.Reader
	io.Closer
}

// startFailure reads the first event of a streamed reply. A vendor may send
// a failure inside a 200 reply, before any text. When that event is one that
// moves a request on, it is reported with the status it stands for. Either
// way the bytes read are put back in front of the body.
func startFailure(res *http.Response) (int, string, bool) {
	closer := res.Body
	rd, sse := eventStream(res)
	br := bufio.NewReader(rd)
	if !sse {
		res.Body = &replayBody{Reader: br, Closer: closer}
		return 0, "", false
	}
	var block []byte
	for len(block) < 64<<10 {
		line, err := br.ReadBytes('\n')
		block = append(block, line...)
		if err != nil || (len(bytes.TrimSpace(line)) == 0 && len(bytes.TrimSpace(block)) > 0) {
			break
		}
	}
	res.Body = &replayBody{Reader: io.MultiReader(bytes.NewReader(block), br), Closer: closer}
	return streamError(block)
}

// streamError reads the first event of a stream. It is a failure when the
// event is an error whose text names a rate limit, overload, exhausted quota
// or an unavailable service. Any other first event is not a failure.
func streamError(block []byte) (int, string, bool) {
	var event string
	var data []byte
	for _, line := range bytes.Split(block, []byte("\n")) {
		line = bytes.TrimSpace(line)
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			event = strings.TrimSpace(string(line[len("event:"):]))
		case bytes.HasPrefix(line, []byte("data:")):
			data = append(data, bytes.TrimSpace(line[len("data:"):])...)
		}
	}
	if len(data) == 0 {
		return 0, "", false
	}
	kind, msg, isErr := errorOf(data)
	if event == "error" || event == "response.failed" {
		isErr = true
	}
	if !isErr {
		return 0, "", false
	}
	if msg == "" {
		msg = string(data)
	}
	code := streamCode(kind + " " + msg)
	return code, msg, code != 0
}

// errorOf reads an error out of one event's data, in the shapes vendors use:
// {"error":{...}}, {"type":"error",...} and Responses' {"response":{"error":...}}.
func errorOf(data []byte) (kind, msg string, isErr bool) {
	var v struct {
		Type     string          `json:"type"`
		Error    json.RawMessage `json:"error"`
		Response struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &v) != nil {
		return "", "", false
	}
	raw := v.Error
	if len(raw) == 0 || string(raw) == "null" {
		raw = v.Response.Error
	}
	if len(raw) == 0 || string(raw) == "null" {
		return v.Type, "", v.Type == "error"
	}
	var e struct {
		Type    string `json:"type"`
		Code    any    `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &e) != nil {
		var s string
		json.Unmarshal(raw, &s)
		return "", s, true
	}
	return fmt.Sprint(e.Type, " ", e.Code), e.Message, true
}

// streamCode maps an in-stream error's text to the status it stands for, or
// 0 when it is not one that moves a request on.
func streamCode(text string) int {
	m := strings.ToLower(text)
	switch {
	case hasAny(m, "rate limit", "rate_limit", "too many requests", "429"):
		return 429
	case hasAny(m, "overload", "529"):
		return 529
	case hasAny(m, "quota", "insufficient", "exhaust", "balance", "credit", "额度", "余额", "用尽"):
		return 402
	case hasAny(m, "unavailable", "server_error", "timeout", "503"):
		return 503
	}
	return 0
}

// hasAny reports whether s contains any of the words.
func hasAny(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}
