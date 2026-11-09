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
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const fallbackCooldown = time.Minute

// candidate is one try: a provider, narrowed to one of its keys when it has
// several enabled ones, and the model to ask it for.
type candidate struct {
	p     provider.Provider
	model string
	rest  string // what a failure rests: the provider, or one of its keys
	label string // how a failure is named in the call record
}

// candidates is the primary and then its fallbacks. Each provider's keys come
// in try order, and a key that failed recently goes behind its provider's
// other keys. A provider whose keys have all just failed goes behind the
// rest, so fallbacks are only reached once every key of the primary is used up.
func (s *Server) candidates(p provider.Provider, model string) []candidate {
	seen := map[string]bool{p.ID + "/" + model: true}
	groups := [][]candidate{s.keyCandidates(p, model)}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok || seen[fp.ID+"/"+fm] {
			continue
		}
		seen[fp.ID+"/"+fm] = true
		groups = append(groups, s.keyCandidates(fp, fm))
	}
	if len(groups) == 1 {
		return groups[0]
	}
	var ready, resting []candidate
	for _, g := range groups {
		if s.allResting(g) {
			resting = append(resting, g...)
		} else {
			ready = append(ready, g...)
		}
	}
	return append(ready, resting...)
}

// keyCandidates lists the tries for one provider: one per enabled key in
// order, with the keys that are resting after the others. A provider with no
// keys (a signed-in account, or one that needs none) is a single try, as it
// always was.
func (s *Server) keyCandidates(p provider.Provider, model string) []candidate {
	on := p.KeysOn()
	var refs []string
	for _, k := range p.KeyList() {
		if k.On {
			refs = append(refs, k.ID)
		}
	}
	if len(on) == 0 || len(refs) != len(on) {
		return []candidate{{p: p, model: model, rest: p.ID, label: p.ID}}
	}
	var ready, resting []candidate
	for i, k := range on {
		kp := p
		kp.Key, kp.KeyName, kp.Keys = k.Key, k.Name, nil
		c := candidate{p: kp, model: model, rest: p.ID + "#" + refs[i], label: p.ID}
		if len(on) > 1 {
			c.label = p.ID + "(" + accountName(k) + ")"
		}
		if s.resting(c.rest) {
			resting = append(resting, c)
		} else {
			ready = append(ready, c)
		}
	}
	return append(ready, resting...)
}

// accountName names a key in the call record: its name, or its masked form
// when it has none. The full key never goes into a record.
func accountName(k provider.KeyEntry) string {
	if k.Name != "" {
		return k.Name
	}
	return provider.Mask(k.Key)
}

// allResting reports whether every try in a group is resting.
func (s *Server) allResting(g []candidate) bool {
	for _, c := range g {
		if !s.resting(c.rest) {
			return false
		}
	}
	return true
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
