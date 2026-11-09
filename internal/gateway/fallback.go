package gateway

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// candidate is one provider and model a request may be sent to, with the
// keys (or accounts) to try there, in order.
type candidate struct {
	p      provider.Provider
	model  string
	plans  []provider.KeyPlan
	claude bool // a Claude Code subscription, served through the Claude Code binary
}

// candidates lists where a request may go, best first: the provider and model
// the client named, then the entries of its fallback list that resolve. A
// provider that failed within the cooldown moves behind the others; it is
// still tried when nothing else works. Candidates with no endpoint are left out.
func (s *Server) candidates(p provider.Provider, model string, from provider.Protocol) []candidate {
	all := []candidate{s.candidateFor(p, model, from)}
	seen := map[string]bool{p.ID + "\x00" + model: true}
	for _, entry := range p.Fallback {
		fp, fm, ok := resolveFallback(entry)
		if !ok || seen[fp.ID+"\x00"+fm] {
			continue
		}
		seen[fp.ID+"\x00"+fm] = true
		all = append(all, s.candidateFor(fp, fm, from))
	}
	var hot, cold []candidate
	for _, c := range all {
		switch {
		case len(c.plans) == 0:
		case s.providerCooling(c.p.ID):
			cold = append(cold, c)
		default:
			hot = append(hot, c)
		}
	}
	return append(hot, cold...)
}

// candidateFor is the keys of one provider to try for a model, best first.
func (s *Server) candidateFor(p provider.Provider, model string, from provider.Protocol) candidate {
	if p.Account != nil && p.Account.Agent == "claude" {
		return candidate{p: p, model: model, claude: true, plans: []provider.KeyPlan{{Provider: p}}}
	}
	return candidate{p: p, model: model, plans: s.coolLast(p.ID, p.KeyPlans(from, model))}
}

// resolveFallback reads one fallback entry, "provider/model". Anything that
// does not name a known provider and a model is skipped silently.
func resolveFallback(entry string) (provider.Provider, string, bool) {
	pid, model, ok := strings.Cut(strings.TrimSpace(entry), "/")
	model = strings.TrimSpace(model)
	if !ok || strings.TrimSpace(pid) == "" || model == "" {
		return provider.Provider{}, "", false
	}
	p, err := provider.Find(strings.TrimSpace(pid))
	if err != nil || !p.Ready() {
		return provider.Provider{}, "", false
	}
	return *p, model, true
}

// attempt is the outcome of tryCandidates.
type attempt struct {
	served string            // the provider that answered, or failed last
	model  string            // the model it was asked for
	to     provider.Protocol // the upstream protocol used
	status int
	msg    string
	failed string // the candidates that failed first, "" when none did
	key    string // the key that served, as in keyLabel
}

// tryCandidates sends the request to each candidate in turn, and each key of a
// candidate in turn. A failure that means "out of quota, rate limited, overloaded
// or down" hands the request to the next one, as long as the client has not seen
// the reply yet. The last candidate's reply is final.
func (s *Server) tryCandidates(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte, cands []candidate, u *Usage) attempt {
	var out attempt
	var failed []string
	for ci, c := range cands {
		lastCand := ci == len(cands)-1
		out.served, out.model = c.p.ID, c.model
		for i, pl := range c.plans {
			last := lastCand && i == len(c.plans)-1
			hw := newHoldWriter(w, !last)
			to, status, msg := s.sendOnce(hw, r, from, body, c, pl, !last, u)
			out.to, out.status, out.msg = to, status, msg
			if hw.held && retryable(hw.status, hw.buf.Bytes()) {
				s.coolKey(c.p.ID, pl.Ref)
				failed = append(failed, planLabel(pl))
				continue
			}
			out.key = keyLabel(pl)
			hw.release()
			out.failed = strings.Join(failed, ", ")
			return out
		}
		// every key of this candidate failed; rest it for a while
		s.coolProvider(c.p.ID)
	}
	out.failed = strings.Join(failed, ", ")
	return out
}

// sendOnce sends the request with one key of one candidate.
func (s *Server) sendOnce(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte, c candidate, pl provider.KeyPlan, canRetry bool, u *Usage) (provider.Protocol, int, string) {
	if c.claude {
		status, msg := s.serveClaudeSubscription(w, r, from, c.model, body, u)
		return provider.Anthropic, status, msg
	}
	to, relay := pickUpstream(pl, from, body)
	if relay {
		status, msg := s.passthrough(w, r, pl.Provider, from, c.model, body, u, canRetry)
		return to, status, msg
	}
	status, msg := s.translate(w, r, pl.Provider, from, to, c.model, body, u, canRetry)
	return to, status, msg
}

// retryable reports whether a failed reply is worth sending elsewhere: out of
// quota or rate limited (see retryableKey), or the server failed or is down.
func retryable(status int, body []byte) bool {
	return retryableKey(status, body) || status >= 500
}

// retryableText reports whether an error message, sent by a provider in the
// middle of a 200 stream before any content, says the provider is out of
// quota, rate limited or overloaded.
func retryableText(msg string) bool {
	m := strings.ToLower(msg)
	for _, w := range []string{"rate limit", "ratelimit", "rate_limit", "quota", "credit", "balance",
		"insufficient", "exhaust", "overload", "capacity", "unavailable", "429", "529", "额度", "余额", "限流", "过载"} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

// startError reports whether the first event of a stream is an error that
// retryableText recognises. raw is that event, as it came.
func startError(raw []byte) (string, bool) {
	var msg string
	readSSE(bytes.NewReader(raw), func(event, data string) error {
		if msg == "" && (event == "error" || strings.Contains(data, `"error"`)) {
			msg = data
		}
		return nil
	})
	if msg == "" || !retryableText(msg) {
		return "", false
	}
	return msg, true
}

// peekEvent reads the first event of a stream, through its blank line. It
// returns the raw bytes read and a reader for the rest of the stream.
func peekEvent(rd io.Reader) ([]byte, io.Reader) {
	br := bufio.NewReaderSize(rd, 4<<10)
	var buf []byte
	for len(buf) < 64<<10 {
		line, err := br.ReadBytes('\n')
		buf = append(buf, line...)
		if err != nil {
			break
		}
		if len(bytes.TrimSpace(line)) == 0 && bytes.Contains(buf, []byte("data:")) {
			break
		}
	}
	return buf, br
}

// coolProvider records that a provider failed for every key just now.
func (s *Server) coolProvider(providerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.providerCool == nil {
		s.providerCool = make(map[string]time.Time)
	}
	s.providerCool[providerID] = time.Now()
}

// providerCooling reports whether a provider failed within the cooldown.
func (s *Server) providerCooling(providerID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.providerCool[providerID]
	return ok && time.Since(t) < keyCooldown
}
