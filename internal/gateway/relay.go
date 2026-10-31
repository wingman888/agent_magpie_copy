package gateway

// Candidates: one request may be answered by several things in turn — the
// provider's enabled keys (relay), the other subscription accounts switched
// on, and finally the provider's fallback models. A candidate that fails with
// a quota/rate/availability error before anything was sent to the client is
// abandoned and rests for a minute; the next one answers instead.

import (
	"bytes"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// relayCooldown is how long a failed candidate is tried last. A var for tests.
var relayCooldown = time.Minute

// relayAttempt is one way of answering a request.
type relayAttempt struct {
	p     provider.Provider // already narrowed to one key / one account
	model string
	label string // shown in Call.Fallback when this relayAttempt fails
	cool  string // relayCooldown identity
}

// ---- model families ---------------------------------------------------------

var relayOSeries = regexp.MustCompile(`^o\d`)

func relayBareModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return m
}

func relayIsClaude(model string) bool { return strings.Contains(relayBareModel(model), "claude") }

func relayIsOpenAI(model string) bool {
	m := relayBareModel(model)
	return strings.HasPrefix(m, "gpt") || relayOSeries.MatchString(m) || strings.Contains(m, "codex") || strings.HasPrefix(m, "chatgpt")
}

// ---- keys ------------------------------------------------------------------

// relayKeyVariant is p narrowed to one key; a key limited to a protocol also
// loses the other endpoints, so nothing can send it elsewhere.
func relayKeyVariant(p provider.Provider, k provider.KeyInfo) (provider.Provider, bool) {
	p.Key, p.KeyName, p.KeyProtocol, p.Keys = k.Key, k.Name, k.Protocol, nil
	if k.Protocol == "" {
		return p, true
	}
	if p.Base(k.Protocol) == "" {
		return p, false
	}
	for _, other := range provider.Protocols {
		if other == k.Protocol {
			continue
		}
		switch other {
		case provider.Chat:
			p.Chat = ""
		case provider.Responses:
			p.Responses = ""
		case provider.Anthropic:
			p.Anthropic = ""
		}
	}
	return p, true
}

// relayKeyScore ranks a key variant for a model; lower is better.
func relayKeyScore(p provider.Provider, model string, from provider.Protocol) int {
	limit := p.KeyProtocol
	switch {
	case relayIsClaude(model):
		switch {
		case limit == provider.Anthropic, limit == "" && p.Anthropic != "":
			return 0
		case limit == "":
			return 1
		}
		return 2
	case relayIsOpenAI(model):
		openai := p.Chat != "" || p.Responses != ""
		switch {
		case limit == provider.Chat, limit == provider.Responses, limit == "" && openai:
			return 0
		case limit == "":
			return 1
		}
		return 2
	}
	if p.Base(from) != "" {
		return 0
	}
	return 1
}

// relayKeyVariants lists a provider's usable keys, best first for the model.
func relayKeyVariants(p provider.Provider, model string, from provider.Protocol) []provider.Provider {
	keys := p.KeyList()
	if len(keys) == 0 {
		return []provider.Provider{p}
	}
	type scored struct {
		p provider.Provider
		s int
	}
	var all []scored
	for _, k := range keys {
		if k.Off {
			continue
		}
		v, ok := relayKeyVariant(p, k)
		if !ok {
			continue
		}
		all = append(all, scored{v, relayKeyScore(v, model, from)})
	}
	var out []provider.Provider
	for s := 0; s <= 2; s++ {
		for _, c := range all {
			if c.s == s {
				out = append(out, c.p)
			}
		}
	}
	return out
}

// relayAnthropicKeys are the keys that may count Anthropic tokens: not the ones
// limited to OpenAI endpoints.
func relayAnthropicKeys(p provider.Provider) []provider.Provider {
	if p.Anthropic == "" {
		return nil
	}
	keys := p.KeyList()
	if len(keys) == 0 {
		return []provider.Provider{p}
	}
	var out []provider.Provider
	for _, k := range keys {
		if k.Off || (k.Protocol != "" && k.Protocol != provider.Anthropic) {
			continue
		}
		if v, ok := relayKeyVariant(p, k); ok {
			out = append(out, v)
		}
	}
	return out
}

// ---- candidates --------------------------------------------------------------

func (s *Server) expand(p provider.Provider, model string, from provider.Protocol) []relayAttempt {
	var vs []provider.Provider
	if p.Account != nil {
		vs = []provider.Provider{p}
		if p.Account.Agent == "codex" { // Claude runs through its own binary, which holds one login
			vs = provider.LoginProviders(p)
		}
	} else {
		vs = relayKeyVariants(p, model, from)
	}
	var out []relayAttempt
	for i, v := range vs {
		a := relayAttempt{p: v, model: model, label: p.ID, cool: p.ID + "\x00" + model}
		switch {
		case v.Account != nil && i > 0:
			a.label += " (" + v.Account.User + ")"
			a.cool += "\x00" + strings.ToLower(v.Account.User)
		case v.Account == nil && v.Key != "":
			a.cool += "\x00" + provider.KeyRef(v.Key)
			if v.KeyName != "" {
				a.label += " (" + v.KeyName + ")"
			}
		}
		out = append(out, a)
	}
	return out
}

// candidates lists, in order, who may answer: the provider's own keys or
// accounts, then each fallback. Resting candidates go last.
func (s *Server) candidates(p provider.Provider, model string, from provider.Protocol) []relayAttempt {
	var all []relayAttempt
	seen := map[string]bool{}
	add := func(p provider.Provider, model string) {
		for _, a := range s.expand(p, model, from) {
			if !seen[a.cool] {
				seen[a.cool] = true
				all = append(all, a)
			}
		}
	}
	add(p, model)
	for _, f := range p.Fallback {
		pid, m, ok := strings.Cut(strings.TrimSpace(f), "/")
		m = strings.TrimSpace(m)
		if !ok || m == "" {
			continue
		}
		fp, err := provider.Find(strings.TrimSpace(pid))
		if err != nil || !fp.Ready() {
			continue
		}
		add(*fp, m)
	}
	var ready, resting []relayAttempt
	for _, a := range all {
		if s.resting(a.cool) {
			resting = append(resting, a)
		} else {
			ready = append(ready, a)
		}
	}
	return append(ready, resting...)
}

func (s *Server) resting(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().Before(s.rest[id])
}

func (s *Server) rested(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rest == nil {
		s.rest = map[string]time.Time{}
	}
	s.rest[id] = time.Now().Add(relayCooldown)
}

// ---- when to move on ---------------------------------------------------------

var relayQuotaWords = []string{
	"quota", "insufficient", "balance", "exhaust", "credit", "billing", "rate limit", "usage limit", "limit reached",
	"额度", "余额", "用完", "用尽", "欠费", "配额", "限额",
}

func relayHasQuotaWords(body []byte) bool {
	low := strings.ToLower(string(body))
	for _, w := range relayQuotaWords {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

// relayRetryable reports whether an upstream error means "this candidate cannot
// answer now" rather than "the request is wrong".
func relayRetryable(status int, body []byte) bool {
	switch status {
	case 429, 402, 502, 503, 504, 529:
		return true
	case 403:
		return relayHasQuotaWords(body)
	case 400, 401, 404, 413, 422:
		return false
	}
	if status >= 500 {
		low := strings.ToLower(string(body))
		return strings.Contains(low, "overloaded") || strings.Contains(low, "unavailable")
	}
	return false
}

// ---- holding back an error ---------------------------------------------------

// relayGate passes a successful reply to the client as it comes, but keeps an
// error reply back so the caller can swap in another candidate.
type relayGate struct {
	w       http.ResponseWriter
	h       http.Header
	started bool
	held    bool
	status  int
	buf     bytes.Buffer
}

func newRelayGate(w http.ResponseWriter) *relayGate { return &relayGate{w: w, h: http.Header{}} }

func (g *relayGate) Header() http.Header { return g.h }

func (g *relayGate) start(code int) {
	g.started = true
	dst := g.w.Header()
	for k, v := range g.h {
		dst[k] = v
	}
	g.w.WriteHeader(code)
}

func (g *relayGate) WriteHeader(code int) {
	if g.started || g.held {
		return
	}
	if code >= 400 {
		g.held, g.status = true, code
		return
	}
	g.start(code)
}

func (g *relayGate) Write(p []byte) (int, error) {
	if g.held {
		if g.buf.Len() < 1<<20 {
			g.buf.Write(p)
		}
		return len(p), nil
	}
	if !g.started {
		g.start(http.StatusOK)
	}
	return g.w.Write(p)
}

func (g *relayGate) Flush() {
	if g.held {
		return
	}
	if !g.started {
		g.start(http.StatusOK)
	}
	if f, ok := g.w.(http.Flusher); ok {
		f.Flush()
	}
}

// release sends a held error to the client.
func (g *relayGate) release() {
	if !g.held {
		return
	}
	g.held = false
	g.start(g.status)
	g.w.Write(g.buf.Bytes())
}
