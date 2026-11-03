package gateway

// Key routing: which of a provider's keys, and which of its endpoints, a
// request goes to, in what order, and when a failure moves on to the next.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyRest is how long a key that just failed, or a provider whose keys all
// failed, is left alone while there is something else to try.
const keyRest = time.Minute

// attempt is one try at a provider: one of its keys, and the endpoint that
// key is sent to, asking for one model.
type attempt struct {
	p          provider.Provider // the provider as this key reaches it
	prov       string            // the provider's id
	model      string            // the model id sent upstream; a fallback names its own
	group      int               // which target of the request this belongs to: 0 is the requested model, then the fallbacks in order
	key        provider.KeyOption
	keyed      bool              // the provider has keys of its own (not a keyless or signed-in one)
	restricted bool              // the key is limited to one protocol
	to         provider.Protocol // the endpoint the request goes to
	relay      bool              // to is the client's own protocol: relayed without translation
	score      int               // lower is tried first; see rank

	// set by order: whether the provider or this key is resting
	provResting, keyResting bool
}

// label names the attempt in a call record: the provider, and the key's name
// when the key has one.
func (a attempt) label() string {
	if a.key.Name != "" {
		return a.prov + " (" + a.key.Name + ")"
	}
	return a.prov
}

// plan lists every try a request may make: the requested provider's keys
// first, then the keys of each fallback target in the order the list gives.
// A Claude Code subscription runs only through its own binary, so it is never
// a fallback target.
func plan(p provider.Provider, model string, from provider.Protocol, body []byte) []attempt {
	targets := append([]provider.FallbackTarget{{Provider: p, Model: model}}, provider.Fallbacks(p.Fallback)...)
	var out []attempt
	seen := map[string]bool{}
	for g, t := range targets {
		if seen[t.Provider.ID+"\x00"+t.Model] {
			continue
		}
		seen[t.Provider.ID+"\x00"+t.Model] = true
		if g > 0 && t.Provider.Account != nil && t.Provider.Account.Agent == "claude" {
			continue
		}
		for _, a := range attempts(t.Provider, from, t.Model, body) {
			a.group = g
			out = append(out, a)
		}
	}
	return out
}

// withKey is the provider as one of its keys reaches it: with that key and, for
// a key limited to one protocol, only that protocol's base URL. Nothing sent
// with the key can then reach another endpoint, translation included.
func withKey(p provider.Provider, k provider.KeyOption) provider.Provider {
	p.Key = k.Key
	if k.Protocol != "" {
		if k.Protocol != provider.Chat {
			p.Chat = ""
		}
		if k.Protocol != provider.Responses {
			p.Responses = ""
		}
		if k.Protocol != provider.Anthropic {
			p.Anthropic = ""
		}
	}
	return p
}

// defaultEndpoint is where a key without a protocol limit is sent: the
// client's own protocol when the provider speaks it, else the provider's
// first protocol, with the request translated.
func defaultEndpoint(p provider.Provider, from provider.Protocol, body []byte) (to provider.Protocol, relay, ok bool) {
	if p.Base(from) != "" && (p.Account == nil || !p.Account.Stream || streamOf(body)) {
		return from, true, true
	}
	speaks := p.Speaks()
	if len(speaks) == 0 {
		return "", false, false
	}
	return speaks[0], false, true
}

// attempts lists every try the request may make, in no particular order: one
// per enabled key that has an endpoint for the request. A key limited to a
// protocol the provider lacks takes no part.
func attempts(p provider.Provider, from provider.Protocol, model string, body []byte) []attempt {
	keys := p.EnabledKeys()
	if len(keys) == 0 {
		// keyless providers (local servers) and signed-in accounts
		keys = []provider.KeyOption{{}}
	}
	var out []attempt
	for _, k := range keys {
		if k.Protocol == "" {
			to, relay, ok := defaultEndpoint(p, from, body)
			if !ok {
				continue
			}
			out = append(out, attempt{p: withKey(p, k), prov: p.ID, model: model, key: k, keyed: k.Ref != "", to: to, relay: relay,
				score: rank(model, from, to, false)})
			continue
		}
		if p.Base(k.Protocol) == "" {
			continue
		}
		out = append(out, attempt{p: withKey(p, k), prov: p.ID, model: model, key: k, keyed: true, restricted: true, to: k.Protocol,
			relay: k.Protocol == from, score: rank(model, from, k.Protocol, true)})
	}
	return out
}

// countAttempts are the keys that may answer count_tokens: the Anthropic
// protocol is the only one it speaks, so keys limited to another protocol
// never see it.
func countAttempts(p provider.Provider) []provider.Provider {
	if p.Anthropic == "" {
		return nil
	}
	keys := p.EnabledKeys()
	if len(keys) == 0 {
		return []provider.Provider{p}
	}
	var out []provider.Provider
	for _, k := range keys {
		if k.Protocol != "" && k.Protocol != provider.Anthropic {
			continue
		}
		out = append(out, withKey(p, k))
	}
	return out
}

// rank scores an attempt; lower goes first. A key limited to the endpoint the
// model is best served by goes first, an unrestricted key that reaches a fit
// endpoint next, then the unrestricted key that must translate, and a key
// limited to some other protocol comes last, as a last resort.
func rank(model string, from, to provider.Protocol, restricted bool) int {
	fit := fits(model, from, to)
	switch {
	case restricted && fit:
		return 0
	case fit:
		return 1
	case restricted:
		return 3
	}
	return 2
}

// fits reports whether an endpoint is the one a model is best served by:
// Claude models on Anthropic, GPT, o-series and codex models on Chat or
// Responses, and anything else on the client's own protocol, which needs no
// translation.
func fits(model string, from, to provider.Protocol) bool {
	switch {
	case isClaude(model):
		return to == provider.Anthropic
	case isOpenAI(model):
		return to == provider.Chat || to == provider.Responses
	}
	return to == from
}

func isClaude(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
}

var openAIName = regexp.MustCompile(`^(gpt|chatgpt|codex|o\d)`)

// isOpenAI recognizes OpenAI's model names, with or without a "vendor/" prefix.
func isOpenAI(model string) bool {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return openAIName.MatchString(m) || strings.Contains(m, "codex")
}

// order puts the attempts in the order to try them: providers that are not
// resting first, then the requested model before its fallbacks, then keys
// that are not resting, then by score. A key or provider that just failed
// waits its turn behind the others, though it is still tried when nothing
// else is left.
func (s *Server) order(tries []attempt) {
	now := time.Now()
	s.mu.Lock()
	for i := range tries {
		a := &tries[i]
		a.provResting = now.Before(s.rests[providerRestKey(a.prov)])
		a.keyResting = a.keyed && now.Before(s.rests[a.prov+"\x00"+a.key.Ref])
	}
	s.mu.Unlock()
	sort.SliceStable(tries, func(i, j int) bool {
		a, b := tries[i], tries[j]
		if a.provResting != b.provResting {
			return !a.provResting
		}
		if a.group != b.group {
			return a.group < b.group
		}
		if a.keyResting != b.keyResting {
			return !a.keyResting
		}
		return a.score < b.score
	})
}

// rest records that a key just failed.
func (s *Server) rest(id, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rests[id+"\x00"+ref] = time.Now().Add(keyRest)
}

// restProvider records that a provider failed every try it had for one
// target. Its rest key has no NUL separator, so it never collides with a key's.
func (s *Server) restProvider(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rests[providerRestKey(id)] = time.Now().Add(keyRest)
}

func providerRestKey(id string) string { return "\x01" + id }

// upstreamFail is an attempt that failed before any of its reply reached
// the client. The caller may try another attempt, or write the error itself.
type upstreamFail struct {
	status int
	msg    string
	retry  bool // the failure is one that another key or endpoint may get past
}

// retriable reports whether an upstream HTTP error should move the request to
// the next attempt: out of quota or rate limited, overloaded, or the service
// is down. A 403 counts only when its text says the quota ran out. A plain
// bad request does not.
func retriable(status int, body []byte) bool {
	switch status {
	case 402, 429, 500, 502, 503, 504, 529:
		return true
	case 403:
		return retryText(string(body))
	}
	return false
}

var retryWords = []string{
	"rate limit", "rate_limit", "too many requests", "overload", "quota", "insufficient",
	"balance", "credit", "usage limit", "unavailable", "capacity", "限流", "额度", "余额", "过载",
}

// retryText reports whether an error text names one of the failures another
// attempt may get past.
func retryText(text string) bool {
	t := strings.ToLower(text)
	for _, w := range retryWords {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// statusOfText gives the status an in-stream error stands for, for the
// record and for a final error to the client.
func statusOfText(text string) int {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "overload"), strings.Contains(t, "529"):
		return 529
	case strings.Contains(t, "rate limit"), strings.Contains(t, "rate_limit"),
		strings.Contains(t, "too many requests"), strings.Contains(t, "429"), strings.Contains(t, "限流"):
		return 429
	case strings.Contains(t, "quota"), strings.Contains(t, "insufficient"), strings.Contains(t, "balance"),
		strings.Contains(t, "credit"), strings.Contains(t, "额度"), strings.Contains(t, "余额"):
		return 402
	}
	return 502
}

// firstEvent reads the first server-sent event of a reply, or the whole reply
// when it is not a stream, so the reply can be judged before anything is sent.
func firstEvent(br *bufio.Reader) []byte {
	var buf []byte
	for len(buf) < 64<<10 {
		line, err := br.ReadBytes('\n')
		buf = append(buf, line...)
		if err != nil {
			break
		}
		if len(bytes.TrimSpace(line)) == 0 && len(bytes.TrimSpace(buf)) > 0 {
			break
		}
	}
	return buf
}

// eventData is the data of an event block, joined; a block with no data lines
// is taken whole, as a plain JSON reply is.
func eventData(raw []byte) []byte {
	var out []byte
	hasData := false
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data:")) {
			hasData = true
			out = append(out, bytes.TrimSpace(line[len("data:"):])...)
		}
	}
	if !hasData {
		return bytes.TrimSpace(raw)
	}
	return out
}

// streamError judges the first event of a reply: when it is an error that
// another attempt may get past, it is a failure to move on from.
func streamError(held []byte, name string) (bool, int, string) {
	if len(held) == 0 {
		return false, 0, ""
	}
	data := eventData(held)
	isErr := bytes.Contains(held, []byte("event: error"))
	var m struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &m) == nil && (m.Type == "error" || (len(m.Error) > 0 && string(m.Error) != "null")) {
		isErr = true
	}
	if !isErr || !retryText(string(data)) {
		return false, 0, ""
	}
	return true, statusOfText(string(data)), name + ": " + provider.APIError(data, "stream error")
}
