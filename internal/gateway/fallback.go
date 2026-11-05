package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// target is a provider and the model it is asked for: the one the client
// named, or one of its fallbacks.
type target struct {
	p     provider.Provider
	model string
}

// hop is one try in a fallback chain: one key or account of a target, or the
// target's provider itself.
type hop struct {
	tgt    int               // index of the target in the chain
	model  string            // the model this try asks for
	view   provider.Provider // the provider as seen through this key or account
	to     provider.Protocol // endpoint the reply comes from; from when relayed, "" when none
	relay  bool              // the client's own endpoint is used, so the reply is relayed as is
	claude bool              // a Claude Code subscription, run through the agent binary
	msg    string            // set when the try cannot be made at all
	ref    string            // cooldown ref within the target; "" for the target itself
	key    string            // label of the key used, for the call record; "" when none
	desc   string            // how the try is named in the call record when it fails
	last   bool              // the last try of its target
}

// fallbackTargets resolves the fallback list of a provider into targets, in
// order. Entries that do not resolve are skipped silently, as are repeats of
// the primary or of an earlier entry.
func fallbackTargets(p provider.Provider, model string) []target {
	seen := map[string]bool{p.ID + "\x00" + model: true}
	var out []target
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok {
			continue
		}
		key := fp.ID + "\x00" + fm
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, target{p: fp, model: fm})
	}
	return out
}

// serveChain sends a request through the targets in turn. Each target tries
// its keys (or its signed-in accounts) first. A try that fails for a reason
// worth retrying (see keyRetryable) hands the request to the next try, as long
// as no reply has started; the last try's error goes back to the client as it
// is. A target whose every try failed is tried last for keyCooldown. It
// returns the target and endpoint of the last try, its status and message, and
// a description of the tries that failed on the way ("" when none did).
// It also returns the key label of the try that answered ("" when it failed,
// or when the try used no named or routed key).
func (s *Server) serveChain(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte, ts []target, u *Usage) (target, provider.Protocol, int, string, string, string) {
	var hops []hop
	for _, i := range s.targetOrder(ts) {
		hops = append(hops, s.hopsOf(i, ts[i], from, body)...)
	}
	var failed []string
	var final hop
	var status int
	var msg string
	for n, h := range hops {
		final = h
		hw := newHoldWriter(w, n < len(hops)-1)
		status, msg = s.runHop(hw, r, h, from, body, u)
		if hw.isHeld() && keyRetryable(status, hw.body()) {
			if h.ref != "" {
				s.cool(ts[h.tgt].p.ID, h.ref)
			}
			if h.last {
				s.cool(ts[h.tgt].p.ID, "")
			}
			failed = append(failed, h.desc)
			continue
		}
		hw.release()
		break
	}
	key := ""
	if status < 400 {
		key = final.key
	}
	return ts[final.tgt], final.to, status, msg, strings.Join(failed, "; "), key
}

// targetOrder lists the targets in the order to try them. Those that failed
// within keyCooldown are left out while others are ready; when all are cooling,
// all are listed in their order.
func (s *Server) targetOrder(ts []target) []int {
	var ready, cooled []int
	for i, t := range ts {
		if s.cooling(t.p.ID, "") {
			cooled = append(cooled, i)
		} else {
			ready = append(ready, i)
		}
	}
	if len(ready) > 0 {
		return ready
	}
	return cooled
}

// cooling reports whether a key, account or provider (ref "") failed within
// keyCooldown.
func (s *Server) cooling(providerID, ref string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.keyCool[providerID+"\x00"+ref]
	return ok && time.Since(t) < keyCooldown
}

// hopsOf lists the tries of one target, in order. It always returns at least
// one try, so a target that cannot be reached still explains why.
func (s *Server) hopsOf(i int, t target, from provider.Protocol, body []byte) []hop {
	p := t.p
	var hs []hop
	switch {
	case p.Account != nil && p.Account.Agent == "claude":
		hs = []hop{{view: p, to: provider.Anthropic, claude: true, desc: p.ID}}
	case p.Account != nil && len(p.Account.Alternates()) > 0:
		cands := append([]provider.Provider{p}, p.Account.Alternates()...)
		refs := make([]string, len(cands))
		for k, c := range cands {
			refs[k] = accountRef(c)
		}
		for _, k := range s.coolOrder(p.ID, refs) {
			c := cands[k]
			h := hop{view: c, ref: refs[k], desc: accountFailedDesc(p.ID, c)}
			h.relay = c.Base(from) != "" && (c.Account == nil || !c.Account.Stream || streamOf(body))
			h.to = endpointOf(c, from, h.relay)
			hs = append(hs, h)
		}
	case p.Account == nil && p.Routed():
		atts := s.coolLast(p.ID, p.Attempts(from, t.model))
		for _, a := range atts {
			hs = append(hs, hop{view: a.View, to: a.To, relay: a.To == from && a.View.Base(from) != "",
				ref: a.Key.Ref, key: keyLabel(a.Key), desc: failedDesc(p.ID, a.Key.Name)})
		}
		if len(atts) == 0 {
			msg := p.Name + " has no key for this endpoint"
			if len(p.Speaks()) == 0 {
				msg = p.Name + " has no endpoint configured"
			}
			hs = []hop{{view: p, msg: msg, desc: p.ID}}
		}
	default:
		h := hop{view: p, desc: p.ID}
		h.relay = p.Base(from) != "" && (p.Account == nil || !p.Account.Stream || streamOf(body))
		h.to = endpointOf(p, from, h.relay)
		hs = []hop{h}
	}
	for k := range hs {
		hs[k].tgt, hs[k].model = i, t.model
		if hs[k].to == "" && hs[k].msg == "" && !hs[k].claude {
			hs[k].msg = p.Name + " has no endpoint configured"
		}
	}
	hs[len(hs)-1].last = true
	return hs
}

// endpointOf is the endpoint a try uses: the client's own when relayed, else
// the provider's first native one. "" when the provider has none.
func endpointOf(p provider.Provider, from provider.Protocol, relay bool) provider.Protocol {
	if relay {
		return from
	}
	if sp := p.Speaks(); len(sp) > 0 {
		return sp[0]
	}
	return ""
}

// runHop makes one try and writes its reply to w.
func (s *Server) runHop(w http.ResponseWriter, r *http.Request, h hop, from provider.Protocol, body []byte, u *Usage) (int, string) {
	switch {
	case h.claude:
		return s.serveClaudeSubscription(w, r, from, h.model, body, u)
	case h.msg != "":
		return writeError(w, from, http.StatusBadGateway, h.msg), h.msg
	case h.relay:
		return s.passthrough(w, r, h.view, from, h.model, body, u)
	}
	return s.translate(w, r, h.view, from, h.to, h.model, body, u)
}

// openingError looks at the first event of a stream whose reply is still held
// back. When that event is an error the provider raised before answering, the
// HTTP status it stands for and its message are returned; the status is 0 when
// there is no such error. The reader returned yields the whole stream as it was.
func openingError(rd io.Reader) (io.Reader, int, string) {
	br := bufio.NewReaderSize(rd, 16<<10)
	var head bytes.Buffer
	for head.Len() < 64<<10 {
		line, err := br.ReadBytes('\n')
		head.Write(line)
		if (len(bytes.TrimSpace(line)) == 0 && head.Len() > len(line)) || err != nil {
			break
		}
	}
	all := io.MultiReader(bytes.NewReader(head.Bytes()), br)
	var data []string
	for _, ln := range strings.Split(head.String(), "\n") {
		if strings.HasPrefix(ln, "data:") {
			data = append(data, strings.TrimSpace(ln[len("data:"):]))
		}
	}
	payload := strings.Join(data, "")
	var v struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(payload), &v) != nil {
		return all, 0, ""
	}
	if v.Type != "error" && (len(v.Error) == 0 || string(v.Error) == "null") {
		return all, 0, ""
	}
	var e struct {
		Message string `json:"message"`
	}
	json.Unmarshal(v.Error, &e)
	msg := e.Message
	if msg == "" {
		msg = payload
	}
	return all, streamErrorStatus(msg), msg
}

// streamErrorStatus is the status an error event sent inside a 200 stream
// stands for, when its wording says the provider cannot answer now. It is 0
// when it does not, and the error then reaches the client as it is.
func streamErrorStatus(msg string) int {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "overload"):
		return 529
	case containsAny(m, "rate limit", "rate_limit", "too many requests"):
		return 429
	case containsAny(m, "quota", "insufficient", "balance", "exhaust", "额度", "余额"):
		return 429
	case containsAny(m, "unavailable", "server error", "internal error", "timeout"):
		return 503
	}
	return 0
}

// containsAny reports whether s holds any of the substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
