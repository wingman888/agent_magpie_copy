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

// The vendor family a model belongs to, from its name alone.
const (
	familyAnthropic = "anthropic"
	familyOpenAI    = "openai"
	familyUnknown   = "unknown"
)

var openAIModelStart = regexp.MustCompile(`^o[1-9]`)

// modelFamily says which family a model id belongs to. A "provider/" prefix
// is ignored; a name that says neither Claude nor OpenAI is unknown.
func modelFamily(model string) string {
	name := model
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(name)
	switch {
	case strings.HasPrefix(name, "claude"):
		return familyAnthropic
	case strings.HasPrefix(name, "gpt-"), strings.Contains(name, "codex"), openAIModelStart.MatchString(name):
		return familyOpenAI
	}
	return familyUnknown
}

// keyFamily is the model family a key limited to a protocol is for; empty
// for a key that is for any.
func keyFamily(proto provider.Protocol) string {
	switch proto {
	case provider.Anthropic:
		return familyAnthropic
	case provider.Chat, provider.Responses:
		return familyOpenAI
	}
	return ""
}

// keyTier ranks how well a key suits a request, 0 best: the key of the
// model's own family first, then a key for any model, then a key for
// another family. When the name says nothing, a key that speaks the client's
// protocol directly is best and one that needs translating is not.
func keyTier(from provider.Protocol, family string, p provider.Provider, k provider.KeyAccount) int {
	if family != familyUnknown {
		switch kf := keyFamily(k.Protocol); {
		case kf == family:
			return 0
		case kf == "":
			return 1
		default:
			return 2
		}
	}
	if k.Protocol == from || (k.Protocol == "" && p.Base(from) != "") {
		return 0
	}
	return 1
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

// perKey is a provider once per key it has on, in order — or, for a
// signed-in agent, once per account it has on, its own first. A key limited
// to one protocol is used on that protocol's endpoint only, and is left out
// when the provider has no such endpoint. The keys that suit the model and
// the client's protocol best go first; the others keep their order.
func perKey(from provider.Protocol, p provider.Provider, model string) []candidate {
	if p.Account != nil {
		out := []candidate{{p, model, p.ID}}
		for _, q := range p.AlsoOn() {
			out = append(out, candidate{q, model, p.ID + "@" + q.Account.User})
		}
		return out
	}
	keys := p.KeysOn()
	if len(keys) == 0 {
		return []candidate{{p, model, p.ID}}
	}
	label := func(k provider.KeyAccount) string {
		if len(keys) < 2 {
			return p.ID
		}
		return p.ID + "#" + provider.KeyID(k.Key)
	}
	family := modelFamily(model)
	type ranked struct {
		c    candidate
		tier int
	}
	var out []ranked
	for _, k := range keys {
		if k.Protocol != "" && p.Base(k.Protocol) == "" {
			continue // limited to a protocol this provider lacks: not in use
		}
		out = append(out, ranked{candidate{p.WithKey(k), model, label(k)}, keyTier(from, family, p, k)})
	}
	if len(out) == 0 {
		// no key fits this provider: as before, every key is tried on all of it
		if len(keys) < 2 {
			return []candidate{{p, model, p.ID}}
		}
		all := make([]candidate, 0, len(keys))
		for _, k := range keys {
			q := p
			q.Key, q.KeyName, q.KeyProtocol = k.Key, k.Name, k.Protocol
			all = append(all, candidate{q, model, label(k)})
		}
		return all
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].tier < out[j].tier })
	cs := make([]candidate, len(out))
	for i, r := range out {
		cs[i] = r.c
	}
	return cs
}

// candidates is the primary and then its fallbacks, those resting after a
// recent failure moved behind the rest.
func (s *Server) candidates(from provider.Protocol, p provider.Provider, model string) []candidate {
	out := perKey(from, p, model)
	seen := map[string]bool{p.ID + "/" + model: true}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok || seen[fp.ID+"/"+fm] {
			continue
		}
		seen[fp.ID+"/"+fm] = true
		out = append(out, perKey(from, fp, fm)...)
	}
	return s.restLast(out)
}

// restLast moves the candidates resting after a recent failure behind the rest.
func (s *Server) restLast(out []candidate) []candidate {
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

// anthropicKey is the provider as its best key sees it for counting tokens
// with Anthropic's API: the first key, in the order requests try them, that
// has an Anthropic endpoint. None when every key is for another protocol.
func (s *Server) anthropicKey(p provider.Provider, model string) (provider.Provider, bool) {
	for _, c := range s.restLast(perKey(provider.Anthropic, p, model)) {
		if c.p.Anthropic != "" {
			return c.p, true
		}
	}
	return provider.Provider{}, false
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
