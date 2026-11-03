package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// restAfterFailure is how long a provider that just failed in a way another
// provider could answer is passed over. The table lives in memory only.
const restAfterFailure = time.Minute

// probeLimit is how much of a success reply is held while waiting for its
// first event to end. A longer first event is judged as it stands.
const probeLimit = 64 << 10

// switchKeywords mark a 400, 401 or 403 as a quota, rate or overload problem
// rather than a bad request. Matched case-insensitively.
var switchKeywords = []string{
	"quota", "insufficient", "balance", "credit", "billing", "exceeded",
	"rate limit", "usage limit", "too many requests", "overloaded",
	"余额", "额度", "欠费", "限流", "频率", "套餐", "用量", "上限",
}

// switchable reports whether a failed reply from a provider may be retried on
// the next candidate. body is the error reply the client would have seen.
func switchable(status int, body []byte) bool {
	switch {
	case status == http.StatusPaymentRequired, status == http.StatusRequestTimeout,
		status == http.StatusTooManyRequests, status >= 500:
		return true
	case status == http.StatusBadRequest, status == http.StatusUnauthorized, status == http.StatusForbidden:
		msg := strings.ToLower(string(body))
		for _, kw := range switchKeywords {
			if strings.Contains(msg, kw) {
				return true
			}
		}
	}
	return false
}

// target is one provider and the vendor's model id to try it with.
type target struct {
	p     provider.Provider
	model string
	// rest is the note of the provider's last switchable failure, set when
	// the provider is resting and so has been moved behind the others.
	rest string
}

// restEntry is a provider resting after a switchable failure.
type restEntry struct {
	until time.Time
	note  string // "<providerId>: <reason>" of that failure
}

// targetsFor lists the candidates for a request: the resolved primary first,
// then the primary's own Fallback entries in order. Entries that do not
// resolve, or name a model the provider does not offer, are skipped, as are
// repeats of an earlier candidate. Fallbacks do not expand recursively.
func targetsFor(p provider.Provider, model string) []target {
	out := []target{{p: p, model: model}}
	seen := map[string]bool{p.ID + "\x00" + model: true}
	for _, name := range p.Fallback {
		fp, fm, ok := FallbackTarget(name)
		if !ok {
			continue
		}
		key := fp.ID + "\x00" + fm
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, target{p: fp, model: fm})
	}
	return out
}

// FallbackTarget resolves one Fallback entry ("provider/model") to the
// provider and vendor model it names. It reports false for entries that are
// not written provider/model or that the provider does not offer. The CLI
// uses it to check entries before saving, so both agree on what is known.
func FallbackTarget(name string) (provider.Provider, string, bool) {
	name = strings.TrimSpace(name)
	if _, _, ok := strings.Cut(name, "/"); !ok {
		return provider.Provider{}, "", false // fallbacks are written provider/model, never a bare model id
	}
	fp, fm, ok := provider.Resolve(name)
	if !ok || !offers(fp, fm) {
		return provider.Provider{}, "", false
	}
	return fp, fm, true
}

// offers reports whether a provider serves a model: the user picked it, or
// its model catalog lists it. Resolve alone accepts any name after the slash.
func offers(p provider.Provider, model string) bool {
	for _, id := range p.Models {
		if id == model {
			return true
		}
	}
	for _, m := range p.Available() {
		if m.ID == model {
			return true
		}
	}
	return false
}

// order moves candidates whose provider is resting behind the others. They
// are kept, so a lone resting provider is still tried last.
func (s *Server) order(ts []target) []target {
	var ready, resting []target
	for _, t := range ts {
		if note, ok := s.restNote(t.p.ID); ok {
			t.rest = note
			resting = append(resting, t)
		} else {
			ready = append(ready, t)
		}
	}
	return append(ready, resting...)
}

// rest marks a provider as having just failed in a switchable way, with the
// note describing that failure.
func (s *Server) rest(providerID, note string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resting == nil {
		s.resting = make(map[string]restEntry)
	}
	s.resting[providerID] = restEntry{until: time.Now().Add(restAfterFailure), note: note}
}

// restNote returns the failure note of a provider that is still resting.
func (s *Server) restNote(providerID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.resting[providerID]
	if !ok || !time.Now().Before(e.until) {
		return "", false
	}
	return e.note, true
}

// reasonOf is one entry of a call's Fallback note: "<providerId>: <reason>".
// Separators the note itself uses are kept out of the reason.
func reasonOf(p provider.Provider, status int, msg string) string {
	reason := strings.TrimSpace(strings.TrimPrefix(msg, p.Name+": "))
	if reason == "" {
		reason = http.StatusText(status)
	}
	reason = strings.Join(strings.Fields(reason), " ")
	reason = strings.ReplaceAll(reason, ";", ",")
	if rs := []rune(reason); len(rs) > 200 {
		reason = string(rs[:200]) + "…"
	}
	return fmt.Sprintf("%s: %d %s", p.ID, status, reason)
}

// errKind joins an upstream error's type and code into the one string that
// errorStatus reads.
func errKind(typ string, code json.RawMessage) string {
	c := strings.Trim(strings.TrimSpace(string(code)), `"`)
	return strings.TrimSpace(typ + " " + c)
}

// errorStatus is the HTTP status an upstream error type or code stands for,
// judged by its kind alone so that a message cannot change the answer. It
// returns 0 when the kind says nothing it recognises.
func errorStatus(kind string) int {
	k := strings.ToLower(kind)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(k, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("overloaded"):
		return 529 // the status writeError gives an overloaded error
	case has("rate_limit", "rate limit", "too_many_requests", "resource_exhausted"):
		return http.StatusTooManyRequests
	case has("insufficient_quota", "quota", "billing", "payment", "balance", "credit"):
		return http.StatusPaymentRequired
	case has("timeout"):
		return http.StatusRequestTimeout
	case has("invalid_request", "invalid_argument", "bad_request", "context_length"):
		return http.StatusBadRequest
	case has("authentication"):
		return http.StatusUnauthorized
	case has("permission"):
		return http.StatusForbidden
	case has("not_found"):
		return http.StatusNotFound
	case has("api_error", "server_error", "internal", "unavailable"):
		return http.StatusInternalServerError
	}
	return 0
}

// inbandStatus reads the first event of a success reply. When that event is
// an error, it returns the status the error stands for and its message;
// otherwise 0. An error whose kind is unrecognised is judged by its message
// keywords alone, as a 400 is.
func inbandStatus(ev []byte) (int, string) {
	var name string
	var data []string
	for _, line := range strings.Split(string(ev), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[len("data:"):], " "))
		}
	}
	if len(data) == 0 {
		return 0, ""
	}
	var v struct {
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(strings.Join(data, "\n")), &v) != nil {
		return 0, ""
	}
	isErr := name == "error" || v.Type == "error"
	var e struct {
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	}
	if len(v.Error) > 0 && string(v.Error) != "null" {
		isErr = true
		if json.Unmarshal(v.Error, &e) != nil {
			_ = json.Unmarshal(v.Error, &e.Message) // a bare string error
		}
	}
	if !isErr {
		return 0, ""
	}
	msg := e.Message
	if msg == "" {
		msg = v.Message
	}
	st := errorStatus(errKind(e.Type, e.Code) + " " + errKind(v.Type, v.Code))
	if st == 0 {
		st = http.StatusBadRequest
	}
	return st, msg
}

// eventEnd returns the offset just past the first complete SSE event in b, or
// -1 when there is none yet.
func eventEnd(b []byte) int {
	i := bytes.Index(b, []byte("\n\n"))
	j := bytes.Index(b, []byte("\r\n\r\n"))
	switch {
	case i < 0 && j < 0:
		return -1
	case i < 0:
		return j + 4
	case j < 0:
		return i + 2
	case i < j:
		return i + 2
	}
	return j + 4
}

// sseStart reports whether a reply begins like a server-sent event stream.
func sseStart(t []byte) bool {
	for _, pfx := range []string{"event:", "data:", ":", "id:", "retry:"} {
		if bytes.HasPrefix(t, []byte(pfx)) {
			return true
		}
	}
	return false
}

// gate holds one attempt's reply back until it is known whether it can be
// sent. A success is sent once its first event has shown it is not an error:
// until then nothing reaches the client, so the next candidate can still
// answer. An error reply, or a success whose first event is an error, is held
// in memory and dropped if the next candidate is tried; nothing of a success
// goes out after the first event has been judged a success.
type gate struct {
	dst       http.ResponseWriter
	h         http.Header
	code      int          // the status the attempt wrote; 0 until it writes one
	buf       bytes.Buffer // held back: an error reply, or a success not yet judged
	committed bool         // the header and the held bytes have gone to dst
	inband    int          // status of an error event held in a success reply; 0 if none
	inbandMsg string       // that error's message
}

func newGate(dst http.ResponseWriter) *gate {
	return &gate{dst: dst, h: http.Header{}}
}

func (g *gate) Header() http.Header { return g.h }

// WriteHeader records the status. The header goes out with the first bytes
// that are sent (see commit), so a success can still be switched away from.
func (g *gate) WriteHeader(code int) {
	if g.code != 0 {
		return
	}
	g.code = code
}

func (g *gate) Write(p []byte) (int, error) {
	if g.code == 0 {
		g.WriteHeader(http.StatusOK)
	}
	if g.committed {
		return g.dst.Write(p)
	}
	g.buf.Write(p)
	if g.code >= 400 || g.inband > 0 {
		return len(p), nil
	}
	return len(p), g.probe(false)
}

// probe judges a success reply before any of it is sent. A whole JSON reply
// or a stream whose first event is not an error is committed; a first event
// that is an error is held as a failure. final means no more bytes will come.
func (g *gate) probe(final bool) error {
	b := g.buf.Bytes()
	t := bytes.TrimLeft(b, " \t\r\n")
	switch {
	case len(t) == 0:
		return nil
	case t[0] == '{' || t[0] == '[':
		return g.commit()
	case !sseStart(t):
		if len(t) < 6 && !final {
			return nil // could still turn out to be "data:" and so on
		}
		return g.commit()
	}
	end := eventEnd(b)
	if end < 0 {
		if !final && len(b) < probeLimit {
			return nil
		}
		end = len(b)
	}
	if st, msg := inbandStatus(b[:end]); st != 0 {
		g.inband, g.inbandMsg = st, msg
		return nil
	}
	return g.commit()
}

// Flush passes through once the reply is out; a held reply is not flushed.
func (g *gate) Flush() {
	if !g.committed {
		return
	}
	if f, ok := g.dst.(http.Flusher); ok {
		f.Flush()
	}
}

// commit sends the header and whatever is held to the client. Nothing sent
// after this can be switched away from.
func (g *gate) commit() error {
	if g.committed {
		return nil
	}
	g.committed = true
	dh := g.dst.Header()
	for k, v := range g.h {
		dh[k] = v
	}
	g.dst.WriteHeader(g.code)
	if g.buf.Len() == 0 {
		return nil
	}
	_, err := g.dst.Write(g.buf.Bytes())
	g.buf.Reset()
	return err
}

// setInband marks the reply as failed by an error event of the given status
// (0 when unknown), so the error is held instead of sent. A reply already
// committed cannot be changed.
func (g *gate) setInband(status int, msg string) {
	if g.committed || g.inband > 0 {
		return
	}
	if status == 0 {
		status = http.StatusBadRequest
	}
	g.inband, g.inbandMsg = status, msg
}

// failure reports the status of a reply that may still be switched away from:
// a held error, or a success whose first event was an error. held is false
// once the reply has gone out, or when it is a success.
func (g *gate) failure() (status int, held bool) {
	if !g.committed && g.code < 400 && g.inband == 0 {
		g.probe(true) // judge what there is; a reply with no full event is a success
	}
	switch {
	case g.committed:
		return g.code, false
	case g.inband > 0:
		return g.inband, true
	case g.code >= 400:
		return g.code, true
	}
	return g.code, false
}

// release sends whatever is held to the client as it is.
func (g *gate) release() {
	if g.committed {
		return
	}
	if g.code == 0 {
		g.code = http.StatusBadGateway
	}
	g.commit()
}

// markInband tells a gate that the reply it is writing carries an error event
// of the given kind. It does nothing for a plain writer.
func markInband(w http.ResponseWriter, kind string, msg string) {
	if g, ok := w.(*gate); ok {
		g.setInband(errorStatus(kind), msg)
	}
}
