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
	aside bool   // a key made for another protocol than the pool the request is served from
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
	sort.SliceStable(out, func(i, j int) bool { return keyRank(out[i].p, model, from) < keyRank(out[j].p, model, from) })
	return out, unlisted
}

// keyRank is keyFit, then whether the key is made for the very protocol the
// request is best served in over one that takes any.
func keyRank(q provider.Provider, model string, from provider.Protocol) int {
	rank := keyFit(q, model, from) * 2
	want := from
	if modelFamily(model) == provider.Anthropic {
		want = provider.Anthropic
	}
	if q.KeyProtocol != want {
		rank++
	}
	return rank
}

// pools splits a provider's candidates, best fit first, into the pool the
// request is served from — the keys of the protocol that suits it best —
// and the keys of other protocols, which wait behind it in order of fit. An
// account is always in the pool.
func pools(cs []candidate, model string, from provider.Protocol) (pool, aside []candidate) {
	if len(cs) == 0 || cs[0].p.Account != nil {
		return cs, nil
	}
	best := keyRank(cs[0].p, model, from)
	for _, c := range cs {
		if keyRank(c.p, model, from) == best {
			pool = append(pool, c)
		} else {
			c.aside = true
			aside = append(aside, c)
		}
	}
	return pool, aside
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

// candidates is the primary and then its fallbacks, each provider's keys
// or accounts as its routing orders them, those resting after a recent
// failure moved behind the rest.
func (s *Server) candidates(p provider.Provider, model string, from provider.Protocol) []candidate {
	out, _ := s.plan(p, model, from)
	return out
}

// ask is what a request says of itself that routing goes by.
type ask struct {
	cv    convo
	scope string // what the conversation's answerer is kept for: provider/model, or group/<id>
	group *groupAsk
}

// groupAsk is a request for a routing group.
type groupAsk struct {
	g  provider.Group
	ms []provider.Member
}

// plan is candidates, and for the routing trace what each stood where it
// did by, and those left out.
func (s *Server) plan(p provider.Provider, model string, from provider.Protocol) ([]candidate, planned) {
	out, pl, _ := s.planFor(p, model, from, ask{scope: p.ID + "/" + model})
	return out, pl
}

// planFor is plan for a request that may be of a conversation, or for a
// routing group; and for the trace, what affinity made of it.
func (s *Server) planFor(p provider.Provider, model string, from provider.Protocol, q ask) ([]candidate, planned, *Affinity) {
	var pl planned
	mode, routing := p.Affinity, p.Routing
	if q.group != nil {
		mode, routing = q.group.g.Affinity, q.group.g.Routing
	}
	rec, has := lastAnswer(q.cv.id, q.scope)
	turns := func(cs []candidate) []candidate { return nextTurn(mode, rec, has, cs) }
	var all weighing
	all.lefts = map[string]left{}
	merge := func(wg weighing) {
		for k, v := range wg.lefts {
			all.lefts[k] = v
		}
		for k, v := range wg.tokens {
			if all.tokens == nil {
				all.tokens = map[string]float64{}
			}
			all.tokens[k] = v
		}
	}
	var out []candidate
	var ws []Weighed
	put := func(cs []candidate, wg weighing, fallback, rotating bool) {
		for i, c := range cs {
			w := weighed(c, c.p, wg, fallback, from)
			w.Aside = c.aside
			w.Turn = rotating && i == 0 && !c.aside
			out, ws = append(out, c), append(ws, w)
		}
	}
	unlist := func(ls []candidate, fallback bool) {
		for _, c := range ls {
			w := weighed(c, c.p, weighing{}, fallback, from)
			w.Unlisted = true
			pl.left = append(pl.left, w)
		}
	}
	add := func(pv provider.Provider, m string, fallback bool) {
		cs, unlisted := perKeyOf(pv, m, from)
		pool, aside := pools(cs, m, from)
		pool, wg := weighWith(pv.Routing, pv.ID+"/"+m, pv.Account == nil, pool, m, from, turns)
		merge(wg)
		rotating := pv.Routing == provider.Rotate && len(pool) > 1
		put(pool, wg, fallback, rotating)
		put(aside, weighing{}, fallback, false)
		unlist(unlisted, fallback)
	}
	if g := q.group; g != nil {
		type member struct{ pool, aside []candidate }
		var ms []member
		for _, m := range g.ms {
			cs, unlisted := perKeyOf(m.Provider, m.Model, from)
			pool, aside := pools(cs, m.Model, from)
			ms = append(ms, member{pool, aside})
			unlist(unlisted, false)
		}
		var asides []candidate
		if g.g.Routing == provider.Ordered {
			for i, m := range ms {
				pool, wg := weighWith(g.ms[i].Provider.Routing, g.ms[i].Provider.ID+"/"+g.ms[i].Model, g.ms[i].Provider.Account == nil, m.pool, g.ms[i].Model, from, turns)
				merge(wg)
				put(pool, wg, false, g.ms[i].Provider.Routing == provider.Rotate && len(pool) > 1 && len(out) == 0)
				asides = append(asides, m.aside...)
			}
		} else {
			var pool []candidate
			for _, m := range ms {
				pool = append(pool, m.pool...)
				asides = append(asides, m.aside...)
			}
			pool, wg := weighWith(g.g.Routing, q.scope, false, pool, "", from, turns)
			merge(wg)
			put(pool, wg, false, g.g.Routing == provider.Rotate && len(pool) > 1)
		}
		put(asides, weighing{}, false, false)
	} else {
		add(p, model, false)
		seen := map[string]bool{p.ID + "/" + model: true}
		for _, id := range p.Fallback {
			fp, fm, ok := provider.Resolve(id)
			if !ok || seen[fp.ID+"/"+fm] {
				continue
			}
			seen[fp.ID+"/"+fm] = true
			add(fp, fm, true)
		}
	}
	if len(out) == 1 {
		if r, ok := restOf(out[0].rest); ok {
			ws[0].Rest = &r // tried all the same: there is no other
		}
		pl.order = ws
		return out, pl, nil
	}
	var ready, resting []candidate
	var wReady, wResting []Weighed
	for i, c := range out {
		if r, ok := restOf(c.rest); ok {
			ws[i].Rest = &r
			resting, wResting = append(resting, c), append(wResting, ws[i])
		} else {
			ready, wReady = append(ready, c), append(wReady, ws[i])
		}
	}
	var aff *Affinity
	if len(out) > 1 && q.cv.id != "" {
		spent := func(rest string) bool { l, ok := all.lefts[rest]; return ok && l.used >= usedShare }
		a := affinityOf(mode, routing == provider.Rotate, q.cv, rec, has, out, spent, time.Now())
		aff = &a
		if a.Kept {
			for i, c := range ready {
				if c.rest == rec.last && i > 0 {
					ready = append([]candidate{c}, append(ready[:i:i], ready[i+1:]...)...)
					wReady = append([]Weighed{wReady[i]}, append(wReady[:i:i], wReady[i+1:]...)...)
					break
				}
			}
		}
	}
	pl.order = append(wReady, wResting...)
	return append(ready, resting...), pl, aff
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
