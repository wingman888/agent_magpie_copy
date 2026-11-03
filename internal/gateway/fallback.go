package gateway

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// restAfterFailure is how long a provider that just failed in a way another
// provider could answer is passed over. The table lives in memory only.
const restAfterFailure = time.Minute

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
// resolve are skipped, as are repeats of an earlier candidate. Fallbacks do
// not expand recursively.
func targetsFor(p provider.Provider, model string) []target {
	out := []target{{p: p, model: model}}
	seen := map[string]bool{p.ID + "\x00" + model: true}
	for _, name := range p.Fallback {
		name = strings.TrimSpace(name)
		if _, _, ok := strings.Cut(name, "/"); !ok {
			continue // fallbacks are written provider/model, never a bare model id
		}
		fp, fm, ok := provider.Resolve(name)
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

// gate holds one attempt's reply back until it is known whether it can be
// sent. A success (any status below 400, or the first body byte) is committed
// at once and passes through unchanged; an error is held in memory so it can
// be dropped and the next candidate tried, as long as nothing has reached the
// client yet.
type gate struct {
	dst       http.ResponseWriter
	h         http.Header
	code      int
	held      bytes.Buffer
	committed bool
}

func newGate(dst http.ResponseWriter) *gate {
	return &gate{dst: dst, h: http.Header{}}
}

func (g *gate) Header() http.Header { return g.h }

func (g *gate) WriteHeader(code int) {
	if g.code != 0 {
		return
	}
	g.code = code
	if code < 400 {
		g.commit()
	}
}

func (g *gate) Write(p []byte) (int, error) {
	if g.code == 0 {
		g.WriteHeader(http.StatusOK)
	}
	if !g.committed {
		if g.code >= 400 {
			return g.held.Write(p)
		}
		g.commit()
	}
	return g.dst.Write(p)
}

// Flush passes through once the reply is out; a held error is not flushed.
func (g *gate) Flush() {
	if !g.committed {
		return
	}
	if f, ok := g.dst.(http.Flusher); ok {
		f.Flush()
	}
}

// commit sends the header to the client. Nothing sent after this can be
// switched away from.
func (g *gate) commit() {
	if g.committed {
		return
	}
	g.committed = true
	dh := g.dst.Header()
	for k, v := range g.h {
		dh[k] = v
	}
	g.dst.WriteHeader(g.code)
}

// failure reports the status of a held error reply. held is false when the
// reply has already been committed or is a success.
func (g *gate) failure() (status int, held bool) {
	if g.committed || g.code < 400 {
		return g.code, false
	}
	return g.code, true
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
	if g.held.Len() > 0 {
		g.dst.Write(g.held.Bytes())
	}
}
