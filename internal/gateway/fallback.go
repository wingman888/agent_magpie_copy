package gateway

// Fallback: a provider can name models to use when it can't take a request
// — a coding plan out of quota, a rate limit, an overloaded or failing
// vendor. The request goes to the next one only while none of the reply has
// been sent, so an agent sees one clean answer from whoever gave it, never a
// half from each. A provider that just failed that way waits at the back of
// the line for a minute, rather than costing every request a doomed try.

import (
	"bytes"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const fallbackCooldown = time.Minute

type candidate struct {
	p     provider.Provider
	model string
	// keyed is set when p is one account of a provider that has several
	// enabled keys: p.Key is that one key, and it rests on its own.
	keyed bool
}

// candidates is the primary and then its fallbacks, those resting after a
// recent failure moved behind the rest.
func (s *Server) candidates(p provider.Provider, model string) []candidate {
	out := []candidate{{p: p, model: model}}
	seen := map[string]bool{p.ID + "/" + model: true}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok || seen[fp.ID+"/"+fm] {
			continue
		}
		seen[fp.ID+"/"+fm] = true
		out = append(out, candidate{p: fp, model: fm})
	}
	if len(out) == 1 {
		return out
	}
	var ready, resting []candidate
	for _, c := range out {
		if s.resting(c.p.ID) {
			resting = append(resting, c)
		} else {
			ready = append(ready, c)
		}
	}
	return append(ready, resting...)
}

var restingUntil = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func (s *Server) resting(id string) bool {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	return time.Now().Before(restingUntil.m[id])
}

func (s *Server) rest(id string) {
	restingUntil.Lock()
	restingUntil.m[id] = time.Now().Add(fallbackCooldown)
	restingUntil.Unlock()
}

// keyCandidates turns each provider that has several enabled keys into one
// candidate per key that is ready, in the order KeysOn gives them. A key
// that failed within the last minute is not tried at all, and an off key is
// never listed. A provider whose keys are all resting is skipped as a whole,
// so the request goes on to its fallbacks. A provider with one enabled key
// that is ready stays a single candidate, as it always was.
func keyCandidates(cands []candidate) []candidate {
	var out []candidate
	for _, c := range cands {
		on := c.p.KeysOn()
		// no key at all (a keyless endpoint), or one ready key: as before
		if len(on) == 0 || (len(on) == 1 && !provider.KeyResting(c.p.ID, provider.KeyRef(on[0].Key))) {
			out = append(out, c)
			continue
		}
		for _, k := range on {
			if provider.KeyResting(c.p.ID, provider.KeyRef(k.Key)) {
				continue
			}
			kp := c.p
			kp.Key, kp.KeyName, kp.Keys = k.Key, k.Name, nil
			out = append(out, candidate{p: kp, model: c.model, keyed: true})
		}
	}
	return out
}

// accountLabel names the account a candidate used, for the call record: the
// provider and the key's name, or its masked key when it has no name. The
// full key never appears.
func accountLabel(p provider.Provider) string {
	if p.KeyName != "" {
		return p.ID + "(" + p.KeyName + ")"
	}
	return p.ID + "(" + provider.Mask(p.Key) + ")"
}

// hideKey replaces the full key in a message, should an upstream echo it.
func hideKey(msg, key string) string {
	if key == "" {
		return msg
	}
	return strings.ReplaceAll(msg, key, provider.Mask(key))
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
