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
// request before any fallback model does. A key can be made for one
// protocol only, as some relays hand them out; see perKey.

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
	// aside: a key made for another protocol than the one that suits the
	// request best, tried only after those that do
	aside bool
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
// signed-in agent, once per account it has on, its own first. A key made
// for one protocol only serves on that one's endpoint, and the keys that
// suit the request go first: the Anthropic key for a Claude model, the
// OpenAI one for a GPT model, else the key that speaks what the agent
// spoke, so nothing is translated that needn't be.
func perKey(p provider.Provider, model string, from provider.Protocol) []candidate {
	out, _ := perKeyOf(p, model, from)
	return out
}

// perKeyOf is perKey, and the accounts or keys it left out as not listing
// the model.
func perKeyOf(p provider.Provider, model string, from provider.Protocol) (out, left []candidate) {
	if p.Account != nil {
		all := []candidate{{p: p, model: model, rest: p.ID}}
		for _, q := range p.AlsoOn() {
			all = append(all, candidate{p: q, model: model, rest: p.ID + "@" + q.Account.User})
		}
		// an account whose plan lacks the model (a Free one behind a Plus)
		// would only answer 400; it is tried only when none lists it
		for _, c := range all {
			if c.p.Account.Lists(model) {
				out = append(out, c)
			} else {
				left = append(left, c)
			}
		}
		if len(out) == 0 {
			return all, nil
		}
		return out, left
	}
	keys := p.KeysOn()
	var unlisted []candidate
	for _, k := range keys {
		q := p.WithKey(k)
		if len(q.Speaks()) == 0 {
			continue // made for a protocol this provider has no endpoint for
		}
		rest := p.ID
		if len(keys) > 1 {
			rest += "#" + provider.KeyID(k.Key)
		}
		if !p.Serves(k, model) {
			// the vendor lists the model to another key only
			unlisted = append(unlisted, candidate{p: q, model: model, rest: rest})
			continue
		}
		out = append(out, candidate{p: q, model: model, rest: rest})
	}
	if len(out) == 0 {
		out, unlisted = unlisted, nil // no key lists it: try them all the same
	}
	if len(out) == 0 {
		return []candidate{{p: p, model: model, rest: p.ID}}, nil
	}
	sort.SliceStable(out, func(i, j int) bool { return keyFit(out[i].p, model, from) < keyFit(out[j].p, model, from) })
	return out, unlisted
}

// keyFit ranks how well a key suits a request, best first: 0 fits, 1 needs
// the request translated, 2 is made for another vendor's models.
func keyFit(q provider.Provider, model string, from provider.Protocol) int {
	if q.KeyProtocol == "" {
		return 0
	}
	switch modelFamily(model) {
	case provider.Anthropic:
		if q.KeyProtocol == provider.Anthropic {
			return 0
		}
		return 2
	case provider.Chat:
		if q.KeyProtocol != provider.Anthropic {
			return 0
		}
		return 2
	}
	if q.Base(from) != "" {
		return 0
	}
	return 1
}

// modelFamily is the protocol a model is at home in, when its name says:
// Anthropic for Claude, Chat (standing for OpenAI's) for GPT and the o-series.
func modelFamily(model string) provider.Protocol {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.HasPrefix(m, "claude"):
		return provider.Anthropic
	case strings.HasPrefix(m, "gpt-"), strings.Contains(m, "codex"), len(m) > 1 && m[0] == 'o' && m[1] >= '1' && m[1] <= '9':
		return provider.Chat
	}
	return ""
}

// protoTier ranks how a key's protocol meets the request's, best first: 0
// made for the protocol the agent spoke, 1 made for none in particular, 2
// made for another. A key's protocol is its own; the first key's is the
// provider's KeyProtocol.
func protoTier(q provider.Provider, from provider.Protocol) int {
	switch q.KeyProtocol {
	case "":
		return 1
	case from:
		return 0
	}
	return 2
}

// pools splits a provider's keys into the pool that suits the request best
// — routing, turns and affinity work within it alone — and the rest, set
// aside to be tried after it. How a key suits is protoTier alone: made for
// the request's protocol, then made for none, then made for another; the
// pool is the best tier present, the others follow in that order, marked
// aside. A key's protocol is its own; the first key's, the provider's
// KeyProtocol. keyFit only orders keys within a tier. Accounts are one pool.
//
// The request's protocol is the one the agent spoke — save when the model's
// name says what vendor it is at home with (Claude, GPT) and a key here is
// made expressly for that vendor's protocol: then that one counts, so a
// Claude model goes to its Anthropic key whatever the agent spoke.
func pools(cs []candidate, from provider.Protocol) (pool, aside []candidate) {
	if len(cs) < 2 || cs[0].p.Account != nil {
		return cs, nil
	}
	want := poolProtocol(cs, from)
	tiers := make([][]candidate, 3)
	for _, c := range cs {
		t := protoTier(c.p, want)
		tiers[t] = append(tiers[t], c)
	}
	for _, t := range tiers {
		sort.SliceStable(t, func(i, j int) bool { return keyFit(t[i].p, t[i].model, from) < keyFit(t[j].p, t[j].model, from) })
	}
	for i, t := range tiers {
		if len(t) == 0 {
			continue
		}
		pool = t
		for _, r := range tiers[i+1:] {
			for _, c := range r {
				c.aside = true
				aside = append(aside, c)
			}
		}
		break
	}
	return pool, aside
}

// poolProtocol is the protocol pools ranks keys against: from, or the
// model's home protocol when a key is made expressly for it.
func poolProtocol(cs []candidate, from provider.Protocol) provider.Protocol {
	f := modelFamily(cs[0].model)
	if f == "" || f == from {
		return from
	}
	for _, c := range cs {
		if c.p.KeyProtocol == f {
			return f
		}
	}
	return from
}

// candidates is the primary and then its fallbacks, each provider's keys
// or accounts as its routing orders them, those resting after a recent
// failure moved behind the rest.
func (s *Server) candidates(p provider.Provider, model string, from provider.Protocol) []candidate {
	out, _ := s.plan(p, model, from, session{})
	return out
}

// planOne is one provider's candidates for a model, its pool as its routing
// orders them and then those set aside, noted in pl for the trace.
func planOne(pl *planned, q provider.Provider, m string, from provider.Protocol, fallback bool, s session) []candidate {
	cs, left := perKeyOf(q, m, from)
	pool, aside := pools(cs, from)
	pool, wg := weigh(q, pool, m, from, s)
	for i, c := range pool {
		w := weighed(c, q, wg, fallback, from)
		w.Turn = i == 0 && q.Routing == provider.Rotate && len(pool) > 1
		pl.order = append(pl.order, w)
	}
	for _, c := range aside {
		pl.order = append(pl.order, weighed(c, q, weighing{}, fallback, from))
	}
	for _, c := range left {
		w := weighed(c, q, weighing{}, fallback, from)
		w.Unlisted = true
		pl.left = append(pl.left, w)
	}
	return append(pool, aside...)
}

// plan is candidates, and for the routing trace what each stood where it
// did by, and those left out.
func (s *Server) plan(p provider.Provider, model string, from provider.Protocol, ss session) ([]candidate, planned) {
	var pl planned
	out := planOne(&pl, p, model, from, false, ss)
	seen := map[string]bool{p.ID + "/" + model: true}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok || seen[fp.ID+"/"+fm] {
			continue
		}
		seen[fp.ID+"/"+fm] = true
		out = append(out, planOne(&pl, fp, fm, from, true, ss)...)
	}
	return restLast(out, pl)
}

// planGroup is a routing group's candidates: the keys and accounts of all
// its members ready now, weighed together by the group's routing — or, in
// order, member by member, each by its own provider's routing. A member's
// own fallbacks are not the group's. The same provider's model counts once.
func (s *Server) planGroup(g provider.Group, ms []provider.Member, from provider.Protocol, ss session) ([]candidate, planned) {
	var pl planned
	var out []candidate
	seen := map[string]bool{}
	member := func(m provider.Member) bool {
		k := m.Provider.ID + "/" + m.Model
		if seen[k] {
			return false
		}
		seen[k] = true
		return true
	}
	if g.Routing == provider.Ordered {
		for _, m := range ms {
			if member(m) {
				out = append(out, planOne(&pl, m.Provider, m.Model, from, false, ss)...)
			}
		}
		return restLast(out, pl)
	}
	owner := map[string]provider.Provider{}
	key := func(c candidate) string { return c.rest + "\x00" + c.model }
	var pool, aside []candidate
	for _, m := range ms {
		if !member(m) {
			continue
		}
		cs, left := perKeyOf(m.Provider, m.Model, from)
		p1, a1 := pools(cs, from)
		for _, c := range append(append([]candidate{}, p1...), a1...) {
			owner[key(c)] = m.Provider
		}
		pool, aside = append(pool, p1...), append(aside, a1...)
		for _, c := range left {
			w := weighed(c, m.Provider, weighing{}, false, from)
			w.Unlisted = true
			pl.left = append(pl.left, w)
		}
	}
	pool, wg := weighBy(g.Routing, g.Ref(), pool, ss)
	for i, c := range pool {
		w := weighed(c, owner[key(c)], wg, false, from)
		w.Turn = i == 0 && g.Routing == provider.Rotate && len(pool) > 1
		pl.order = append(pl.order, w)
	}
	for _, c := range aside {
		pl.order = append(pl.order, weighed(c, owner[key(c)], weighing{}, false, from))
	}
	return restLast(append(pool, aside...), pl)
}

// restLast moves those resting after a recent failure behind the rest, in
// the trace too; one alone is tried all the same, there being no other.
func restLast(out []candidate, pl planned) ([]candidate, planned) {
	if len(out) == 1 {
		if r, ok := restOf(out[0].rest); ok {
			pl.order[0].Rest = &r
		}
		return out, pl
	}
	var ready, resting []candidate
	var wReady, wResting []Weighed
	for i, c := range out {
		if r, ok := restOf(c.rest); ok {
			pl.order[i].Rest = &r
			resting, wResting = append(resting, c), append(wResting, pl.order[i])
		} else {
			ready, wReady = append(ready, c), append(wReady, pl.order[i])
		}
	}
	pl.order = append(wReady, wResting...)
	return append(ready, resting...), pl
}

var restingUntil = struct {
	sync.Mutex
	m    map[string]time.Time
	note map[string]Rest // why each rests
}{m: map[string]time.Time{}, note: map[string]Rest{}}

func (s *Server) resting(id string) bool {
	_, ok := restOf(id)
	return ok
}

// restOf is why a candidate is resting, while it is.
func restOf(id string) (Rest, bool) {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	until := restingUntil.m[id]
	if !time.Now().Before(until) {
		return Rest{}, false
	}
	r := restingUntil.note[id]
	r.Until = until
	return r, true
}

// quotaWords are how vendors say "out of quota" or "slow down" when their
// status code doesn't: some answer 400 or 403 with it.
var quotaWords = regexp.MustCompile(`(?i)quota|insufficient|balance|credit|billing|exceeded|rate.?limit|usage.?limit|limit.?reached|hit your .*limit|limit.{0,24}resets|too many requests|overloaded|余额|额度|欠费|限流|频率|套餐|用量|上限`)

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
