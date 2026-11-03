package gateway

// Fallback: a provider can name models to use when it can't take a request
// — a coding plan out of quota, a rate limit, an overloaded or failing
// vendor. The request goes to the next one only while none of the reply has
// been sent, so an agent sees one clean answer from whoever gave it, never a
// half from each. A key that just failed that way waits at the back of its
// provider's line for a minute, rather than costing every request a doomed
// try. A provider with several keys on tries them in turn before its
// fallbacks are used.

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

// candidate is one attempt: a provider with the single key to send, and the
// model to ask for.
type candidate struct {
	p     provider.Provider // Key and KeyName are the key this attempt uses
	model string
	ref   string // cooldown id: the provider and this key
	key   string // the key itself, kept out of error texts
	label string // how the call record names this attempt
}

// candidates is the primary and then its fallbacks, each tried key by key.
// Within a provider the keys still on (not resting) go first; a provider with
// no key ready goes behind the rest, as before.
func (s *Server) candidates(p provider.Provider, model string) []candidate {
	var ready, resting []candidate
	add := func(gp provider.Provider, gm string) {
		var rdy, rst []candidate
		for _, c := range keyAttempts(gp, gm) {
			if s.resting(c.ref) {
				rst = append(rst, c)
			} else {
				rdy = append(rdy, c)
			}
		}
		if len(rdy) == 0 {
			resting = append(resting, rst...)
			return
		}
		ready = append(append(ready, rdy...), rst...)
	}
	add(p, model)
	seen := map[string]bool{p.ID + "/" + model: true}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok || seen[fp.ID+"/"+fm] {
			continue
		}
		seen[fp.ID+"/"+fm] = true
		add(fp, fm)
	}
	return append(ready, resting...)
}

// keyAttempts is one candidate per key on the provider, in attempt order.
// Keys that are off are never listed.
func keyAttempts(p provider.Provider, model string) []candidate {
	keys := p.KeysOn()
	out := make([]candidate, 0, len(keys))
	for _, k := range keys {
		kp := p
		kp.Key, kp.KeyName, kp.Keys = k.Key, k.Name, nil
		label := p.ID
		if len(keys) > 1 {
			label = accountLabel(p.ID, k)
		}
		out = append(out, candidate{
			p:     kp,
			model: model,
			ref:   p.ID + "\x00" + provider.KeyRef(k.Key),
			key:   k.Key,
			label: label,
		})
	}
	return out
}

// accountLabel names a key in the call record: the provider and the key's
// name, or its masked form when it has none.
func accountLabel(id string, k provider.NamedKey) string {
	if k.Name != "" {
		return id + "(" + k.Name + ")"
	}
	return id + "(" + provider.Mask(k.Key) + ")"
}

// scrub hides a key that an error text happens to carry.
func scrub(text, key string) string {
	if key == "" || !strings.Contains(text, key) {
		return text
	}
	return strings.ReplaceAll(text, key, provider.Mask(key))
}

var restingUntil = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func (s *Server) resting(ref string) bool {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	return time.Now().Before(restingUntil.m[ref])
}

func (s *Server) rest(ref string) {
	restingUntil.Lock()
	restingUntil.m[ref] = time.Now().Add(fallbackCooldown)
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
