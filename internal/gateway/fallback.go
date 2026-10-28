package gateway

// Fallback: a provider can name models to use when it can't take a request
// — a coding plan out of quota, a rate limit, an overloaded or failing
// vendor. The request goes to the next one only while none of the reply has
// been sent, so an agent sees one clean answer from whoever gave it, never a
// half from each. A provider that just failed that way waits at the back of
// the line for a minute, rather than costing every request a doomed try.
//
// A provider with several keys on is several candidates, one per key, in
// order, and so is a subscription with several accounts on: when one
// account runs out, the next account of the same provider takes the
// request before any fallback model does.

import (
	"bytes"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const fallbackCooldown = time.Minute

type candidate struct {
	p     provider.Provider
	model string
	rest  string // what rests after a failure: the provider, or one of its keys
}

// label names a candidate in a call's record: the provider, and the key
// when it has several on.
func (c candidate) label() string {
	if c.rest == c.p.ID {
		return c.p.ID
	}
	if c.p.Account != nil {
		return c.p.ID + " (" + c.p.Account.User + ")"
	}
	if c.p.KeyName != "" {
		return c.p.ID + " (" + c.p.KeyName + ")"
	}
	return c.p.ID + " (" + provider.Mask(c.p.Key) + ")"
}

// Model families, as far as a model's name says which vendor made it.
const (
	familyUnknown = iota
	familyAnthropic
	familyOpenAI
)

// modelFamily guesses the vendor of a model from its name: the part after
// the last slash, so "openrouter/openai/o3-mini" is OpenAI's.
func modelFamily(model string) int {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.HasPrefix(m, "claude"):
		return familyAnthropic
	case strings.HasPrefix(m, "gpt-"), strings.Contains(m, "codex"):
		return familyOpenAI
	case len(m) >= 2 && m[0] == 'o' && m[1] >= '1' && m[1] <= '9':
		return familyOpenAI
	}
	return familyUnknown
}

// keyFit ranks a key for a request, lower is better: 0 when it suits the
// model (or needs no translating), 1 when the model is unknown and the key
// needs the request translated, 2 when the key belongs to the other
// vendor's protocol. q is the provider as that key would use it.
func keyFit(q provider.Provider, k provider.KeyAccount, family int, from provider.Protocol) int {
	if k.Protocol == "" {
		return 0
	}
	switch family {
	case familyAnthropic:
		if k.Protocol == provider.Anthropic {
			return 0
		}
		return 2
	case familyOpenAI:
		if k.Protocol != provider.Anthropic {
			return 0
		}
		return 2
	}
	if q.Base(from) != "" {
		return 0
	}
	return 1
}

// perKey is a provider once per key it has on, best fit for the request
// first — or, for a signed-in agent, once per account it has on, its own
// first. A key limited to a protocol the provider has no endpoint for is
// left out.
func perKey(p provider.Provider, model string, from provider.Protocol) []candidate {
	if p.Account != nil {
		out := []candidate{{p, model, p.ID}}
		for _, q := range p.AlsoOn() {
			out = append(out, candidate{q, model, p.ID + "@" + q.Account.User})
		}
		return out
	}
	keys := p.KeysOn()
	family := modelFamily(model)
	var out []candidate
	var fit []int
	for _, k := range keys {
		q := p.WithKey(k)
		if k.Protocol != "" && q.Base(k.Protocol) == "" {
			continue
		}
		c := candidate{q, model, p.ID}
		if len(keys) > 1 {
			c.rest = p.ID + "#" + provider.KeyID(k.Key)
		}
		out = append(out, c)
		fit = append(fit, keyFit(q, k, family, from))
	}
	if len(out) == 0 {
		return []candidate{{p, model, p.ID}}
	}
	idx := make([]int, len(out))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return fit[idx[a]] < fit[idx[b]] })
	sorted := make([]candidate, len(out))
	for i, j := range idx {
		sorted[i] = out[j]
	}
	return sorted
}

// candidates is the primary and then its fallbacks, those resting after a
// recent failure moved behind the rest.
func (s *Server) candidates(p provider.Provider, model string, from provider.Protocol) []candidate {
	out := perKey(p, model, from)
	seen := map[string]bool{p.ID + "/" + model: true}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok || seen[fp.ID+"/"+fm] {
			continue
		}
		seen[fp.ID+"/"+fm] = true
		out = append(out, perKey(fp, fm, from)...)
	}
	if len(out) == 1 {
		return out
	}
	var ready, resting []candidate
	for _, c := range out {
		if s.resting(c.rest) {
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
