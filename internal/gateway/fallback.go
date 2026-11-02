package gateway

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// restPeriod is how long a provider that just failed with a switchable error
// is tried only after every other candidate.
const restPeriod = time.Minute

// candidate is one provider and model a request may be served by.
type candidate struct {
	p       provider.Provider
	model   string
	order   int  // place in the configured order: 0 is the model asked for
	resting bool // the provider failed recently and was moved back
}

// restTable remembers providers that recently could not take a request.
// It lives only as long as the gateway process.
type restTable struct {
	mu    sync.Mutex
	until map[string]time.Time
	why   map[string]string
	now   func() time.Time
}

func newRestTable() *restTable {
	return &restTable{until: make(map[string]time.Time), why: make(map[string]string), now: time.Now}
}

// mark starts id's rest; reason is what it failed with.
func (t *restTable) mark(id, reason string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.until[id] = t.now().Add(restPeriod)
	t.why[id] = reason
}

// reason is what a resting provider last failed with.
func (t *restTable) reason(id string) string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.why[id]
}

func (t *restTable) resting(id string) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	u, ok := t.until[id]
	if !ok {
		return false
	}
	if !t.now().Before(u) {
		delete(t.until, id)
		delete(t.why, id)
		return false
	}
	return true
}

func (t *restTable) reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.until = make(map[string]time.Time)
	t.why = make(map[string]string)
}

// candidates lists who may serve a request for model on p: p itself, then
// p's own fallbacks in order. Fallbacks that do not resolve, or repeat an
// earlier provider and model, are skipped; fallbacks' own fallbacks are not
// followed. Providers resting after a recent failure go last, in order.
func (s *Server) candidates(p provider.Provider, model string) []candidate {
	out := []candidate{{p: p, model: model}}
	seen := map[string]bool{p.ID + "\x00" + model: true}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok {
			continue
		}
		key := fp.ID + "\x00" + fm
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, candidate{p: fp, model: fm, order: len(out)})
	}
	if len(out) < 2 {
		return out
	}
	var ready, resting []candidate
	for _, c := range out {
		if s.rest.resting(c.p.ID) {
			c.resting = true
			resting = append(resting, c)
		} else {
			ready = append(ready, c)
		}
	}
	return append(ready, resting...)
}

// switchPhrases mark a 400/401/403 as the provider being out of quota,
// rate limited or overloaded rather than the request being wrong.
var switchPhrases = []string{
	"quota", "insufficient", "balance", "credit", "billing", "exceeded",
	"rate limit", "rate_limit", "ratelimit", "usage limit", "usage_limit", "too many requests", "overloaded",
	"余额", "额度", "欠费", "限流", "频率", "套餐", "用量", "上限",
}

// switchable reports whether an error reply means another provider should
// be tried: the provider cannot take the request, not that it is malformed.
func switchable(status int, bodies ...[]byte) bool {
	switch {
	case status == 402, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 401, status == 403:
		for _, b := range bodies {
			msg := strings.ToLower(string(b))
			for _, ph := range switchPhrases {
				if strings.Contains(msg, ph) {
					return true
				}
			}
		}
	}
	return false
}

// fallbackWriter stands between one attempt and the client. A success
// status goes straight through, and from then on so does everything else.
// An error reply is held back until the attempt ends, so the gateway can
// still decide to try someone else without the client seeing it.
type fallbackWriter struct {
	dst       http.ResponseWriter
	header    http.Header
	status    int
	committed bool
	buf       bytes.Buffer
	upstream  []byte // the provider's raw error body, when one was read
}

func newFallbackWriter(dst http.ResponseWriter) *fallbackWriter {
	return &fallbackWriter{dst: dst, header: make(http.Header)}
}

func (w *fallbackWriter) Header() http.Header { return w.header }

func (w *fallbackWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	if code < 400 {
		w.commit()
	}
}

func (w *fallbackWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.committed {
		return w.dst.Write(p)
	}
	return w.buf.Write(p)
}

func (w *fallbackWriter) Flush() {
	if !w.committed {
		return
	}
	if f, ok := w.dst.(http.Flusher); ok {
		f.Flush()
	}
}

// commit sends the held reply to the client; after it, nothing is held.
func (w *fallbackWriter) commit() {
	if w.committed || w.status == 0 {
		return
	}
	w.committed = true
	h := w.dst.Header()
	for k, v := range w.header {
		h[k] = v
	}
	w.dst.WriteHeader(w.status)
	if w.buf.Len() > 0 {
		w.dst.Write(w.buf.Bytes())
		w.buf.Reset()
	}
}

// noteUpstreamError keeps the provider's raw error body for deciding
// whether to fall back, when w is a fallbackWriter.
func noteUpstreamError(w http.ResponseWriter, b []byte) {
	if fw, ok := w.(*fallbackWriter); ok {
		fw.upstream = b
	}
}

// failureReason is why an attempt failed: its status and a short message.
func failureReason(status int, msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	if msg == "" {
		return fmt.Sprint(status)
	}
	return fmt.Sprintf("%d %s", status, msg)
}
