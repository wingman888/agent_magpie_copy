package gateway

// Several keys per provider: which key, and which protocol it may reach, a
// request tries first; and how a failing candidate is passed over.

import (
	"bytes"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyCooldown is how long a candidate that just failed is passed over.
const keyCooldown = time.Minute

// attemptKey is one way to reach a provider: one of its keys, with the
// provider narrowed to the single protocol that key is limited to.
type attemptKey struct {
	view  provider.Provider
	ref   string
	name  string
	proto provider.Protocol // "" is any protocol
	rank  int
	model string // the model this candidate is asked for (a fallback's own)
}

func (a attemptKey) id() string { return a.view.ID + "\x00" + a.ref }

// label names the candidate in a call's fallback note.
func (a attemptKey) label() string {
	if a.name == "" {
		return a.view.ID
	}
	return a.view.ID + " (" + a.name + ")"
}

var oSeries = regexp.MustCompile(`^o[0-9]`)

// modelFamily sorts models by the vendor whose keys should serve them first.
func modelFamily(model string) string {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "claude"):
		return "claude"
	case strings.HasPrefix(m, "gpt"), strings.Contains(m, "codex"), oSeries.MatchString(m):
		return "openai"
	}
	return ""
}

// keyRank orders keys for a model: 0 is the best fit, larger is a last resort.
// Claude models want Anthropic keys, GPT-like models want OpenAI keys; other
// models want a key that speaks the client's protocol, so nothing is translated.
func keyRank(family string, proto provider.Protocol, p provider.Provider, from provider.Protocol) int {
	switch family {
	case "claude":
		switch proto {
		case provider.Anthropic:
			return 0
		case "":
			return 1
		}
		return 2
	case "openai":
		switch proto {
		case provider.Chat, provider.Responses:
			return 0
		case "":
			return 1
		}
		return 2
	}
	if proto == "" {
		if p.Base(from) != "" {
			return 0
		}
		return 1
	}
	if proto == from {
		return 0
	}
	return 1
}

// narrow keeps only the endpoint of one protocol, so a key limited to it can
// never reach another.
func narrow(p provider.Provider, proto provider.Protocol) provider.Provider {
	if proto != provider.Chat {
		p.Chat = ""
	}
	if proto != provider.Responses {
		p.Responses = ""
	}
	if proto != provider.Anthropic {
		p.Anthropic = ""
	}
	return p
}

// keyOrder lists the ways to reach p for a request arriving in protocol from
// for model, best first. Keys that are off, limited to an endpoint p lacks, or
// unreadable are left out. A candidate that failed within keyCooldown goes
// after the others, but is still tried.
func (s *Server) keyOrder(p provider.Provider, from provider.Protocol, model string) []attemptKey {
	var out []attemptKey
	if p.Account != nil {
		// a subscription: the login the agent is signed in as, then the
		// switched-on logins in saved order (see provider.AccountViews)
		for _, v := range p.AccountViews() {
			out = append(out, attemptKey{view: v, ref: "login:" + strings.ToLower(v.Account.User), name: v.Account.User})
		}
	}
	keys := p.KeyList()
	if len(keys) == 0 && p.Account == nil {
		out = append(out, attemptKey{view: p})
	}
	for _, k := range keys {
		if k.Off || !provider.ValidKeyProtocol(k.Protocol) {
			continue
		}
		v := p
		v.Key = k.Key
		if k.Protocol != "" {
			if p.Base(k.Protocol) == "" {
				continue
			}
			v = narrow(v, k.Protocol)
		}
		out = append(out, attemptKey{view: v, ref: k.Ref, name: k.Name, proto: k.Protocol})
	}
	fam := modelFamily(model)
	for i := range out {
		out[i].rank = keyRank(fam, out[i].proto, p, from)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].rank < out[j].rank })

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var live, cooled []attemptKey
	for _, a := range out {
		if t, ok := s.cool[a.id()]; ok && now.Before(t) {
			cooled = append(cooled, a)
		} else {
			live = append(live, a)
		}
	}
	return append(live, cooled...)
}

// cooldown marks a candidate as failed for keyCooldown.
func (s *Server) cooldown(a attemptKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cool == nil {
		s.cool = map[string]time.Time{}
	}
	s.cool[a.id()] = time.Now().Add(keyCooldown)
}

// retryable reports whether a failed reply is worth passing on to the next
// candidate: out of quota, rate limited, overloaded or down. Anything else,
// such as a malformed request, is the client's to see.
func retryable(status int, msg string) bool {
	switch status {
	case 402, 429, 502, 503, 504, 529:
		return true
	case 403:
		m := strings.ToLower(msg)
		for _, w := range []string{"quota", "exceed", "exhaust", "insufficient", "balance", "credit", "limit", "额度", "余额"} {
			if strings.Contains(m, w) {
				return true
			}
		}
	}
	return false
}

// gate holds back a candidate's reply until it is known to be a success. An
// error reply is kept (status and body) so it can be dropped; a success goes
// straight through from its first byte, so streaming is unchanged.
type gate struct {
	out       http.ResponseWriter
	hdr       http.Header
	code      int // the held error status, else 0
	body      bytes.Buffer
	committed bool
}

func newGate(w http.ResponseWriter) *gate {
	return &gate{out: w, hdr: http.Header{}}
}

// failed reports whether an error reply is being held.
func (g *gate) failed() bool { return g.code >= 400 && !g.committed }

func (g *gate) Header() http.Header {
	if g.committed {
		return g.out.Header()
	}
	return g.hdr
}

func (g *gate) commit(code int) {
	if g.committed {
		return
	}
	g.committed = true
	h := g.out.Header()
	for k, v := range g.hdr {
		h[k] = v
	}
	g.out.WriteHeader(code)
}

func (g *gate) WriteHeader(code int) {
	if g.committed || g.code != 0 {
		return
	}
	if code >= 400 {
		g.code = code
		return
	}
	g.commit(code)
}

func (g *gate) Write(b []byte) (int, error) {
	if g.committed {
		return g.out.Write(b)
	}
	if g.code != 0 {
		return g.body.Write(b)
	}
	g.commit(http.StatusOK)
	return g.out.Write(b)
}

func (g *gate) Flush() {
	if g.failed() {
		return
	}
	g.commit(http.StatusOK)
	if f, ok := g.out.(http.Flusher); ok {
		f.Flush()
	}
}

// release sends a held error reply to the real writer.
func (g *gate) release() {
	if g.committed || g.code == 0 {
		return
	}
	g.commit(g.code)
	g.out.Write(g.body.Bytes())
}
