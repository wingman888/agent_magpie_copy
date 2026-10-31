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
	// aside marks a key made for another protocol than the request needs:
	// it is tried only after the keys that suit it.
	aside bool
	// scope names what rotates it: the provider's model, or the group.
	scope string
	// fit is how well the key's own protocol suits the request (see keyFit).
	fit int
}

// id tells a candidate apart from the others of its group: the same key
// can serve more than one of its provider's models.
func (c candidate) id() string { return c.rest + "\x00" + c.model }

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
// spoke, so nothing is translated that needn't be. The keys that suit it
// less come after, marked aside (see poolsOf).
func perKey(p provider.Provider, model string, from provider.Protocol) []candidate {
	out, _ := perKeyOf(p, model, from)
	return out
}

// perKeyOf is perKey, and the accounts or keys it left out as not listing
// the model.
func perKeyOf(p provider.Provider, model string, from provider.Protocol) (out, left []candidate) {
	primary, aside, left := poolsOf(p, model, from)
	return append(primary, aside...), left
}

// poolsOf splits a provider's keys by how well they suit the request: the
// primary pool is the best fitting tier that has any key, and routing,
// rotation and affinity work inside it alone. The keys of the other tiers —
// those with no protocol named first, then those made for another one — are
// the aside: tried only after the primary pool, in that order. left are the
// accounts or keys the vendor's list says lack the model.
func poolsOf(p provider.Provider, model string, from provider.Protocol) (primary, aside, left []candidate) {
	if p.Account != nil {
		all := []candidate{{p: p, model: model, rest: p.ID}}
		for _, q := range p.AlsoOn() {
			all = append(all, candidate{p: q, model: model, rest: p.ID + "@" + q.Account.User})
		}
		// an account whose plan lacks the model (a Free one behind a Plus)
		// would only answer 400; it is tried only when none lists it
		for _, c := range all {
			if c.p.Account.Lists(model) {
				primary = append(primary, c)
			} else {
				left = append(left, c)
			}
		}
		if len(primary) == 0 {
			return all, nil, nil
		}
		return primary, nil, left
	}
	keys := p.KeysOn()
	var out, unlisted []candidate
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
		return []candidate{{p: p, model: model, rest: p.ID}}, nil, nil
	}
	want := requestProtocol(out, model, from)
	best := 3
	for i := range out {
		out[i].fit = keyFit(out[i].p, want)
		best = min(best, out[i].fit)
	}
	for tier := best; tier <= 2; tier++ {
		for _, c := range out {
			if c.fit != tier {
				continue
			}
			if tier == best {
				primary = append(primary, c)
			} else {
				c.aside = true
				aside = append(aside, c)
			}
		}
	}
	return primary, aside, unlisted
}

// requestProtocol is the protocol a request needs of a key: the one it came
// in on (from) — except when its model is at home in another protocol (a
// Claude model in Anthropic's, a GPT one in OpenAI's) and the provider has a
// key made for exactly that one, which then serves it with nothing
// translated, whichever endpoint the agent used.
func requestProtocol(cs []candidate, model string, from provider.Protocol) provider.Protocol {
	fam := modelFamily(model)
	if fam == "" || fam == from {
		return from
	}
	for _, c := range cs {
		kp := c.p.KeyProtocol
		if kp == fam {
			return fam
		}
	}
	return from
}

// keyFit ranks how well a key suits a request needing protocol want, best
// first: 0 a key made for that protocol, 1 a key made for none in
// particular, 2 a key made for another protocol. The key's protocol is its
// own, or for the main key the provider's KeyProtocol.
func keyFit(q provider.Provider, want provider.Protocol) int {
	switch {
	case q.KeyProtocol == "":
		return 1
	case q.KeyProtocol == want:
		return 0
	}
	return 2
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

// candidates is the primary and then its fallbacks, each provider's keys
// or accounts as its routing orders them, those resting after a recent
// failure moved behind the rest.
func (s *Server) candidates(p provider.Provider, model string, from provider.Protocol) []candidate {
	out, _ := s.plan(p, model, from)
	return out
}

// plan is candidates, and for the routing trace what each stood where it
// did by, and those left out.
func (s *Server) plan(p provider.Provider, model string, from provider.Protocol) ([]candidate, planned) {
	return s.planFor(nil, p, model, from)
}

// planFor is plan for a request that belongs to a conversation (rq), whose
// turns rotate and whose last answerer is kept to when affinity says so.
func (s *Server) planFor(rq *reqInfo, p provider.Provider, model string, from provider.Protocol) ([]candidate, planned) {
	var pl planned
	add := func(q provider.Provider, m string, fallback bool) []candidate {
		primary, aside, left := poolsOf(q, m, from)
		cs, wg := routePool(rq, q, primary, m, from)
		for i, c := range cs {
			w := weighed(c, q, wg, fallback, from)
			w.Turn = i == 0 && q.Routing == provider.Rotate && len(cs) > 1
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
		return append(cs, aside...)
	}
	out := add(p, model, false)
	seen := map[string]bool{p.ID + "/" + model: true}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok || seen[fp.ID+"/"+fm] {
			continue
		}
		seen[fp.ID+"/"+fm] = true
		out = append(out, add(fp, fm, true)...)
	}
	return s.settle(rq, &pl, out, p.Affinity)
}

// planGroup is the candidates of a routing group: the keys and accounts of
// all its members weighed together. A member's own fallback is not the
// group's.
func (s *Server) planGroup(rq *reqInfo, g provider.Group, ms []provider.Member, from provider.Protocol) ([]candidate, planned) {
	var pl planned
	var asides, lefts []candidate
	var out []candidate
	emit := func(cs []candidate, wg weighing, routing string) {
		for i, c := range cs {
			w := weighed(c, c.p, wg, false, from)
			w.Routing = routing
			w.Turn = i == 0 && routing == provider.Rotate && len(cs) > 1
			pl.order = append(pl.order, w)
		}
		out = append(out, cs...)
	}
	if g.Routing == provider.Ordered {
		// member by member, each as its own provider's routing orders it
		for _, m := range ms {
			primary, aside, left := poolsOf(m.Provider, m.Model, from)
			cs, wg := routePool(rq, m.Provider, primary, m.Model, from)
			emit(cs, wg, m.Provider.Routing)
			asides, lefts = append(asides, aside...), append(lefts, left...)
		}
	} else {
		var pool []candidate
		for _, m := range ms {
			primary, aside, left := poolsOf(m.Provider, m.Model, from)
			pool = append(pool, primary...)
			asides, lefts = append(asides, aside...), append(lefts, left...)
		}
		cs, wg := routePool(rq, provider.Provider{ID: groupPrefix + g.ID, Routing: g.Routing}, pool, "", from)
		emit(cs, wg, g.Routing)
	}
	for _, c := range asides {
		pl.order = append(pl.order, weighed(c, c.p, weighing{}, false, from))
	}
	out = append(out, asides...)
	for _, c := range lefts {
		w := weighed(c, c.p, weighing{}, false, from)
		w.Unlisted = true
		pl.left = append(pl.left, w)
	}
	return s.settle(rq, &pl, out, g.Affinity)
}

// groupPrefix is what a request's model starts with to name a routing group.
const groupPrefix = "group/"

// routePool orders one pool of candidates as the routing of rp says. A tool
// result going back inside a turn that rotation started stays on the key
// that took the turn; a new turn moves on to the next.
func routePool(rq *reqInfo, rp provider.Provider, cs []candidate, model string, from provider.Protocol) ([]candidate, weighing) {
	key := turnKey(rp.ID, model)
	var wg weighing
	pinned := false
	if rp.Routing == provider.Rotate && rq != nil && rq.within && len(cs) > 1 {
		if rest := rq.pinned(key); rest != "" {
			for i, c := range cs {
				if c.id() == rest {
					cs = append(append([]candidate{}, cs[i:]...), cs[:i]...)
					pinned = true
					break
				}
			}
		}
	}
	if !pinned {
		cs, wg = weigh(rp, cs, model, from)
	}
	out := append([]candidate{}, cs...)
	for i := range out {
		out[i].scope = key
	}
	return out, wg
}

// settle moves the candidates resting after a recent failure behind the
// rest, then lets affinity put the one that last answered this
// conversation first.
func (s *Server) settle(rq *reqInfo, pl *planned, out []candidate, affinity string) ([]candidate, planned) {
	if len(out) == 1 {
		if r, ok := restOf(out[0].rest); ok {
			pl.order[0].Rest = &r // tried all the same: there is no other
		}
		return out, *pl
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
	out = append(ready, resting...)
	if rq != nil && len(out) > 1 {
		out, pl.order, rq.affinity = rq.keepTo(affinity, out, pl.order)
	}
	return out, *pl
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
