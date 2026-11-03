package gateway

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// coolOff is how long a provider that just failed with a retryable error is
// tried last. It lives in memory: a restart clears it.
const coolOff = time.Minute

// coolEntry is a provider in its cool-off: until when, and the failure that
// put it there, in the form the call record shows ("id: status text").
type coolEntry struct {
	until time.Time
	item  string
}

// target is one provider and model a request may be answered by.
type target struct {
	p     provider.Provider
	model string
}

// candidates lists who may answer a request for p's model: p first, then the
// fallbacks p is configured with, in order. Unknown or unusable entries are
// skipped silently, and so are repeats. Fallbacks do not expand their own
// fallbacks. Providers in their cool-off go last.
func (s *Server) candidates(p provider.Provider, model string) []target {
	out := []target{{p, model}}
	seen := map[string]bool{p.ID + "\x00" + model: true}
	for _, ref := range p.Fallback {
		pid, m, ok := strings.Cut(strings.TrimSpace(ref), "/")
		m = strings.TrimSpace(m)
		if !ok || m == "" {
			continue
		}
		fp, err := provider.Find(pid)
		if err != nil || !fp.Ready() || !fp.Knows(m) {
			continue
		}
		key := fp.ID + "\x00" + m
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, target{*fp, m})
	}
	return s.coolingOrder(out)
}

// coolingOrder moves candidates whose provider is cooling down behind the
// others, keeping the order within each group.
func (s *Server) coolingOrder(ts []target) []target {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var warm, cold []target
	for _, t := range ts {
		if e, ok := s.cooling[t.p.ID]; ok && now.Before(e.until) {
			cold = append(cold, t)
		} else {
			warm = append(warm, t)
		}
	}
	return append(warm, cold...)
}

// cool starts the cool-off of a provider that failed with a retryable error;
// item is that failure as the call record names it.
func (s *Server) cool(id, item string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cooling == nil {
		s.cooling = make(map[string]coolEntry)
	}
	s.cooling[id] = coolEntry{until: time.Now().Add(coolOff), item: item}
}

// coolingItem returns the failure that started a provider's cool-off, if it is
// still running.
func (s *Server) coolingItem(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cooling[id]
	if !ok || !time.Now().Before(e.until) {
		return "", false
	}
	return e.item, true
}

// quotaWords mark a 400, 401 or 403 as a spent quota, a rate limit or an
// overloaded provider, which another provider may not share. They are matched
// against the lowercased error text, so they are lowercase too.
var quotaWords = []string{
	"quota", "insufficient", "balance", "credit", "billing", "exceeded",
	"rate limit", "usage limit", "too many requests", "overloaded",
	"余额", "额度", "欠费", "限流", "频率", "套餐", "用量", "上限",
}

// retryable reports whether a failure with this status and upstream error
// text is worth trying on another provider.
func retryable(status int, text string) bool {
	switch status {
	case 402, 408, 429:
		return true
	case 400, 401, 403:
		lower := strings.ToLower(text)
		for _, w := range quotaWords {
			if strings.Contains(lower, w) {
				return true
			}
		}
		return false
	}
	return status >= 500
}

// upstreamText is the provider's error text without the "Name: " prefix the
// relays put in front of it.
func upstreamText(p provider.Provider, msg string) string {
	return strings.TrimPrefix(msg, p.Name+": ")
}

// failure describes one failed attempt for the call record: "id: status text".
// Semicolons would split the record's list, so they are replaced.
func failure(p provider.Provider, status int, msg string) string {
	text := strings.Join(strings.Fields(upstreamText(p, msg)), " ")
	text = strings.ReplaceAll(text, ";", ",")
	if r := []rune(text); len(r) > 200 {
		text = string(r[:200]) + "…"
	}
	s := fmt.Sprintf("%s: %d", p.ID, status)
	if text != "" {
		s += " " + text
	}
	return s
}

// gate holds a reply until it is known whether it may reach the client. An
// error status is kept back, so the next candidate can take the request
// without the client seeing the failed attempt. Anything else is sent at once,
// as it would be without the gate. Once sent, a reply is final.
type gate struct {
	dst    http.ResponseWriter
	head   http.Header
	status int
	sent   bool
	buf    bytes.Buffer // an error body held back until the gate is released
}

func newGate(dst http.ResponseWriter) *gate {
	return &gate{dst: dst, head: http.Header{}}
}

func (g *gate) Header() http.Header {
	if g.sent {
		return g.dst.Header()
	}
	return g.head
}

func (g *gate) WriteHeader(code int) {
	if g.status != 0 || g.sent {
		return
	}
	g.status = code
	if code < 400 {
		g.commit()
	}
}

func (g *gate) Write(p []byte) (int, error) {
	if g.status == 0 {
		g.WriteHeader(http.StatusOK)
	}
	if !g.sent {
		return g.buf.Write(p)
	}
	return g.dst.Write(p)
}

// Flush passes a flush on once the reply has been sent; before that there is
// nothing to flush.
func (g *gate) Flush() {
	if !g.sent {
		return
	}
	if f, ok := g.dst.(http.Flusher); ok {
		f.Flush()
	}
}

// commit sends the held header and body to the client.
func (g *gate) commit() {
	g.sent = true
	h := g.dst.Header()
	for k, v := range g.head {
		h[k] = v
	}
	if g.status == 0 {
		g.status = http.StatusOK
	}
	g.dst.WriteHeader(g.status)
	if g.buf.Len() > 0 {
		g.dst.Write(g.buf.Bytes())
		g.buf.Reset()
	}
}

// finish sends whatever is still held, if anything.
func (g *gate) finish() {
	if !g.sent {
		g.commit()
	}
}
