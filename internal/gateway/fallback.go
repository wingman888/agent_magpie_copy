package gateway

// Fallback: a provider can name models to use when it can't take a request
// — a coding plan out of quota, a rate limit, an overloaded or failing
// vendor. The request goes to the next one only while none of the reply has
// been sent, so an agent sees one clean answer from whoever gave it, never a
// half from each. A provider that just failed that way waits at the back of
// the line for a minute, rather than costing every request a doomed try.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const fallbackCooldown = time.Minute

// candidate is one attempt: a provider with one key in front, and a model.
type candidate struct {
	p       provider.Provider
	model   string
	// account names the key in the call record when its provider has several
	// enabled keys: the key's name, else its masked form. Empty otherwise.
	account string
}

// label is how a skipped attempt is named in the call record.
func (c candidate) label() string {
	if c.account == "" {
		return c.p.ID
	}
	return c.p.ID + "(" + c.account + ")"
}

// keyCandidates is one candidate per enabled key of p, in the order they are
// tried. A provider with no enabled key (a signed-in account, or one that
// needs none) is a single candidate, as it always was.
func keyCandidates(p provider.Provider, model string) []candidate {
	keys := p.KeysOn()
	if len(keys) == 0 {
		return []candidate{{p: p, model: model}}
	}
	out := make([]candidate, 0, len(keys))
	for _, k := range keys {
		q := p
		q.Key, q.KeyName, q.Keys = k.Key, k.Name, nil
		c := candidate{p: q, model: model}
		if len(keys) > 1 {
			c.account = k.Name
			if c.account == "" {
				c.account = provider.Mask(k.Key)
			}
		}
		out = append(out, c)
	}
	return out
}

// candidates is the primary's keys and then each fallback's, those resting
// after a recent failure moved behind the rest. A provider's own keys stay
// together, so its keys are all tried before any fallback; within a provider
// the resting keys follow the ready ones.
func (s *Server) candidates(p provider.Provider, model string) []candidate {
	groups := [][]candidate{keyCandidates(p, model)}
	seen := map[string]bool{p.ID + "/" + model: true}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok || seen[fp.ID+"/"+fm] {
			continue
		}
		seen[fp.ID+"/"+fm] = true
		groups = append(groups, keyCandidates(fp, fm))
	}
	if len(groups) == 1 && len(groups[0]) == 1 {
		return groups[0]
	}
	var out, back []candidate
	for _, g := range groups {
		var ready, resting []candidate
		for _, c := range g {
			if s.resting(restKey(c.p)) {
				resting = append(resting, c)
			} else {
				ready = append(ready, c)
			}
		}
		if len(ready) > 0 {
			out = append(out, ready...)
			out = append(out, resting...)
		} else {
			back = append(back, resting...)
		}
	}
	return append(out, back...)
}

// restKey names what a failure rests: one key of one provider. Each key rests
// on its own, so a provider's other keys are still tried at once.
func restKey(p provider.Provider) string {
	sum := sha256.Sum256([]byte(p.Key))
	return p.ID + "\x00" + hex.EncodeToString(sum[:8])
}

var restingUntil = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func (s *Server) resting(key string) bool {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	return time.Now().Before(restingUntil.m[key])
}

func (s *Server) rest(key string) {
	restingUntil.Lock()
	restingUntil.m[key] = time.Now().Add(fallbackCooldown)
	restingUntil.Unlock()
}

// quotaWords are how vendors say "out of quota" or "slow down" when their
// status code doesn't: some answer 400 or 403 with it.
var quotaWords = regexp.MustCompile(`(?i)quota|insufficient|balance|credit|billing|exceeded|rate.?limit|usage.?limit|limit.?reached|too many requests|overloaded|余额|额度|欠费|限流|频率|套餐|用量|上限`)

// retryable says whether another provider may do better with a request
// that failed this way.
func retryable(status int, body []byte) bool {
	switch {
	case status == 402, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 401, status == 403:
		return quotaWords.Match(body)
	}
	return false
}

// holdWriter keeps an error reply back while another provider may still
// answer: headers and body wait until release, or are dropped for the next
// try. Anything else goes straight through.
type holdWriter struct {
	w       http.ResponseWriter
	hold    bool
	header  http.Header
	status  int
	passing bool
	held    bytes.Buffer
}

func newHoldWriter(w http.ResponseWriter, hold bool) *holdWriter {
	return &holdWriter{w: w, hold: hold, header: http.Header{}}
}

func (h *holdWriter) Header() http.Header { return h.header }

func (h *holdWriter) WriteHeader(code int) {
	if h.status != 0 {
		return
	}
	h.status = code
	if h.hold && code >= 400 {
		return
	}
	h.pass()
}

func (h *holdWriter) pass() {
	dst := h.w.Header()
	for k, v := range h.header {
		dst[k] = v
	}
	h.w.WriteHeader(h.status)
	h.passing = true
}

func (h *holdWriter) Write(b []byte) (int, error) {
	if h.status == 0 {
		h.WriteHeader(http.StatusOK)
	}
	if !h.passing {
		return h.held.Write(b)
	}
	return h.w.Write(b)
}

func (h *holdWriter) Flush() {
	if f, ok := h.w.(http.Flusher); ok && h.passing {
		f.Flush()
	}
}

// failed reports a held error another provider could answer instead.
func (h *holdWriter) failed() bool {
	return !h.passing && h.status >= 400 && retryable(h.status, h.held.Bytes())
}

// release sends a held reply after all: nobody else is left to try.
func (h *holdWriter) release() {
	if h.passing || h.status == 0 {
		return
	}
	h.pass()
	h.w.Write(h.held.Bytes())
}
