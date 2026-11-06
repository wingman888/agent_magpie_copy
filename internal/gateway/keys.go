package gateway

import (
	"bytes"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyCooldown is how long a key that just failed is passed over in favour of
// the provider's other keys.
const keyCooldown = time.Minute

// heldWriter sits between one relay attempt and the client. An error reply
// (400 and up) is held back, so the next key can be tried without the client
// seeing the failure; anything else goes straight through.
type heldWriter struct {
	dst   http.ResponseWriter
	hdr   http.Header
	state int // stateUndecided, statePassing or stateWithheld
	code  int
	body  bytes.Buffer
}

const (
	stateUndecided = iota // nothing written yet
	statePassing          // the reply has started; it goes to the client as it comes
	stateWithheld         // an error reply, held back
)

func newHeldWriter(dst http.ResponseWriter) *heldWriter {
	return &heldWriter{dst: dst, hdr: http.Header{}}
}

// reset readies the writer for another attempt.
func (h *heldWriter) reset() {
	h.hdr = http.Header{}
	h.state, h.code = stateUndecided, 0
	h.body.Reset()
}

func (h *heldWriter) Header() http.Header {
	if h.hdr == nil {
		h.hdr = http.Header{}
	}
	return h.hdr
}

func (h *heldWriter) WriteHeader(code int) {
	if h.state != stateUndecided {
		return
	}
	if code >= 400 {
		h.state, h.code = stateWithheld, code
		return
	}
	h.commit(code)
}

// commit sends the status and the headers set so far to the client.
func (h *heldWriter) commit(code int) {
	out := h.dst.Header()
	for k, v := range h.hdr {
		out[k] = v
	}
	h.dst.WriteHeader(code)
	h.state = statePassing
}

func (h *heldWriter) Write(p []byte) (int, error) {
	switch h.state {
	case stateUndecided:
		h.commit(http.StatusOK)
	case stateWithheld:
		return h.body.Write(p)
	}
	return h.dst.Write(p)
}

func (h *heldWriter) Flush() {
	if h.state != statePassing {
		return
	}
	if f, ok := h.dst.(http.Flusher); ok {
		f.Flush()
	}
}

// release sends a withheld error reply to the client. It does nothing else.
func (h *heldWriter) release() {
	if h.state != stateWithheld {
		return
	}
	h.commit(h.code)
	h.dst.Write(h.body.Bytes())
}

// keyTry is one attempt: a credential and the endpoint its request goes to.
type keyTry struct {
	p       provider.Provider // the provider with this credential's key in place
	cred    provider.Credential
	proto   provider.Protocol
	score   int  // lower is tried first
	cooling bool // failed recently; tried after the others
}

// relayKeys sends a request to a provider that has API keys and writes the
// reply to w. It is tryKeys with the held reply released at the end, for a
// caller that has no other provider to fall back on.
func (s *Server) relayKeys(w http.ResponseWriter, r *http.Request, p provider.Provider, from provider.Protocol, model string, body []byte, call *Call) {
	hw := newHeldWriter(w)
	failed, status, msg := s.tryKeys(hw, r, p, from, model, body, call)
	hw.release()
	call.Status, call.Error = status, msg
	if len(failed) > 0 {
		call.Fallback = strings.Join(failed, ", ")
	}
}

// tryKeys sends a request to a provider that has API keys, trying them in
// turn. A key that answers with a retryable error is passed over and the next
// one tried, as long as nothing has reached the client yet. The reply goes
// through hw, which holds back an error: when the last key fails retryably
// the held error is left for the caller, which may still move on to another
// provider. failed names the keys that failed on the way, and status and msg
// are the final attempt's outcome.
func (s *Server) tryKeys(hw *heldWriter, r *http.Request, p provider.Provider, from provider.Protocol, model string, body []byte, call *Call) (failed []string, status int, msg string) {
	tries := s.keyTries(p, from, model)
	if len(tries) == 0 {
		msg = p.Name + " has no endpoint configured"
		if len(p.Speaks()) > 0 {
			msg = p.Name + " has no key for a protocol it serves"
		}
		writeError(hw, from, 502, msg)
		return nil, 502, msg
	}
	for i, t := range tries {
		hw.reset()
		call.To = t.proto
		call.Key = keyTag(t.cred)
		if t.proto == from {
			status, msg = s.passthrough(hw, r, t.p, from, model, body, &call.Usage)
		} else {
			status, msg = s.translate(hw, r, t.p, from, t.proto, model, body, &call.Usage)
		}
		if hw.state == stateWithheld && retryable(status, msg) {
			s.markCooling(p.ID, t.cred.Ref)
			failed = append(failed, failLabel(p, t.cred))
			if i < len(tries)-1 {
				continue
			}
		}
		break
	}
	return failed, status, msg
}

// keyTries orders the attempts for a request: the keys that can reach an
// endpoint the vendor has, best fit for the model first, keys that failed
// recently last.
func (s *Server) keyTries(p provider.Provider, from provider.Protocol, model string) []keyTry {
	fam := modelFamily(model)
	var out []keyTry
	for _, c := range p.Credentials() {
		proto, ok := credentialEndpoint(p, c, from)
		if !ok {
			continue
		}
		pk := p
		pk.Key = c.Key
		out = append(out, keyTry{p: pk, cred: c, proto: proto,
			score: placeScore(fam, proto, from), cooling: s.cooling(p.ID, c.Ref)})
	}
	// SliceStable keeps the user's order between equal scores.
	sort.SliceStable(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if x.cooling != y.cooling {
			return !x.cooling
		}
		return x.score < y.score
	})
	return out
}

// credentialEndpoint is the protocol a credential's request is sent with: the
// one the key is limited to; otherwise the client's own when the vendor has
// it, otherwise the vendor's first. ok is false when the credential has no
// endpoint at this vendor.
func credentialEndpoint(p provider.Provider, c provider.Credential, from provider.Protocol) (provider.Protocol, bool) {
	if c.Protocol != "" {
		return c.Protocol, p.Base(c.Protocol) != ""
	}
	if p.Base(from) != "" {
		return from, true
	}
	if to := p.Speaks(); len(to) > 0 {
		return to[0], true
	}
	return "", false
}

// anthropicCredential is the first enabled key that may be sent to the
// Anthropic endpoint, that is one not limited to an OpenAI protocol.
func anthropicCredential(p provider.Provider) (provider.Credential, bool) {
	for _, c := range p.Credentials() {
		if c.Protocol == "" || c.Protocol == provider.Anthropic {
			return c, true
		}
	}
	return provider.Credential{}, false
}

// family is the vendor line a model belongs to, as far as routing cares.
type family int

const (
	familyOther family = iota
	familyClaude
	familyOpenAI
)

var openAISeries = regexp.MustCompile(`^o\d`)

// modelFamily reads the family from the model's name; a "vendor/" prefix is
// ignored.
func modelFamily(model string) family {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "claude"):
		return familyClaude
	case strings.Contains(m, "gpt"), strings.Contains(m, "codex"), openAISeries.MatchString(m):
		return familyOpenAI
	}
	return familyOther
}

// placeScore ranks an attempt for a model. A Claude model goes to Anthropic
// first and an OpenAI-family model to the OpenAI protocols; an attempt that
// does not suit the family ranks behind one that does. Among attempts that
// suit equally, one that speaks the client's protocol directly (no
// translation) is preferred.
func placeScore(f family, proto, from provider.Protocol) int {
	s := 0
	switch f {
	case familyClaude:
		if proto != provider.Anthropic {
			s += 2
		}
	case familyOpenAI:
		if proto == provider.Anthropic {
			s += 2
		}
	}
	if proto != from {
		s++
	}
	return s
}

// retryable is true for replies that mean this key cannot answer now, so
// another key may: rate limits, exhausted quota, overload and server trouble.
// A bad request is not retryable and goes back to the client as it is.
func retryable(status int, msg string) bool {
	switch {
	case status == 402, status == 429, status == 529, status >= 500:
		return true
	case status == 403:
		m := strings.ToLower(msg)
		for _, w := range quotaWords {
			if strings.Contains(m, w) {
				return true
			}
		}
	}
	return false
}

// quotaWords mark a 403 that means the account ran out or is limited, not
// that it may not do this.
var quotaWords = []string{"quota", "insufficient", "balance", "exhaust", "exceed", "limit",
	"额度", "余额", "用完", "用尽", "超出", "超限"}

// claudeTransient reads a Claude Code failure's text: only one that names a
// limit, an overload or an outage may move on to another account. The binary
// reports everything as 502, so the text is what tells a rejected request
// (which must go back to the client as it is) from a busy account.
func claudeTransient(msg string) bool {
	m := strings.ToLower(msg)
	for _, w := range []string{"rate limit", "rate_limit", "429", "529", "overload",
		"usage limit", "quota", "insufficient", "unavailable", "502", "503", "504",
		"timed out", "timeout", "额度", "余额", "用完", "用尽", "超出", "超限"} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

// keyTag names the key an attempt was sent with, for the recent calls: its
// name, or its fingerprint when it has none.
func keyTag(c provider.Credential) string {
	if c.Name != "" {
		return c.Name
	}
	return c.Ref
}

// failLabel names a failed attempt for the recent calls: the provider, and
// the key's name when it has one.
func failLabel(p provider.Provider, c provider.Credential) string {
	if c.Name == "" {
		return p.ID
	}
	return p.ID + " (" + c.Name + ")"
}

// cooling reports whether a key failed within the cooldown.
func (s *Server) cooling(providerID, ref string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().Before(s.cool[providerID+"\x00"+ref])
}

func (s *Server) markCooling(providerID, ref string) {
	if ref == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cool == nil {
		s.cool = make(map[string]time.Time)
	}
	s.cool[providerID+"\x00"+ref] = time.Now().Add(keyCooldown)
}
