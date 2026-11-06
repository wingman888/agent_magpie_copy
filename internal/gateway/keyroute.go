package gateway

// Keys limited to one protocol (provider.KeyAccount.Protocol) are only ever
// sent to that protocol's endpoint: each candidate carries a copy of the
// provider made by WithKey, which keeps just that endpoint. Among a
// provider's keys, the ones that suit the request go first: a Claude model
// to an Anthropic key, a GPT model to an OpenAI one, and for a model whose
// family is unknown, a key that speaks the client's protocol directly. Keys
// that fit less well stay behind as the next try. A provider with no limited
// key keeps the order it has always had.

import (
	"regexp"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// family is the vendor line a model belongs to, judged from its name.
type family int

const (
	unknownFamily family = iota
	anthropicFamily
	openAIFamily
)

// openAIName matches OpenAI model names that don't start with "gpt-":
// the o-series (o1, o3-mini, ...).
var openAIName = regexp.MustCompile(`^o[1-9]`)

// familyOf says which family a model belongs to. A "provider/" prefix is
// ignored, so "relay/claude-opus-4-8" and "openrouter/openai/o3-mini" both
// judge by their last part. Names it can't place (deepseek-chat) are unknown.
func familyOf(model string) family {
	name := strings.ToLower(model)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	switch {
	case strings.HasPrefix(name, "claude"):
		return anthropicFamily
	case strings.HasPrefix(name, "gpt-"), strings.Contains(name, "codex"), openAIName.MatchString(name):
		return openAIFamily
	}
	return unknownFamily
}

// limited reports whether any of p's keys is limited to one protocol.
func limited(p provider.Provider) bool {
	if p.KeyProtocol != "" {
		return true
	}
	for _, k := range p.Keys {
		if k.Protocol != "" {
			return true
		}
	}
	return false
}

// keyRank is how well a key suits a request: 0 fits best. kp is the key's
// protocol limit (empty for all), from the client's protocol, fam the model's
// family.
func keyRank(p provider.Provider, kp provider.Protocol, fam family, from provider.Protocol) int {
	switch fam {
	case anthropicFamily:
		switch kp {
		case provider.Anthropic:
			return 0
		case "":
			return 1
		}
		return 2
	case openAIFamily:
		switch kp {
		case provider.Chat, provider.Responses:
			return 0
		case "":
			return 1
		}
		return 2
	}
	// family unknown: a key that talks the client's protocol directly is
	// preferred over one that needs translating
	if from != "" && (kp == from || (kp == "" && p.Base(from) != "")) {
		return 0
	}
	return 1
}

// providerPlan is p once per usable key, best fit first, for a request from
// the client's protocol from. A key limited to a protocol p lacks is left
// out. With no limited key, or no usable one, it is perKey as it has always
// been.
func providerPlan(p provider.Provider, model string, from provider.Protocol) []candidate {
	if p.Account != nil || !limited(p) {
		return perKey(p, model)
	}
	keys := p.KeysOn()
	fam := familyOf(model)
	type ranked struct {
		c    candidate
		rank int
	}
	var rs []ranked
	for _, k := range keys {
		if k.Protocol != "" && p.Base(k.Protocol) == "" {
			continue // limited to an endpoint this provider doesn't have: ignored
		}
		rest := p.ID
		if len(keys) > 1 {
			rest = p.ID + "#" + provider.KeyID(k.Key)
		}
		rs = append(rs, ranked{candidate{p.WithKey(k), model, rest}, keyRank(p, k.Protocol, fam, from)})
	}
	if len(rs) == 0 {
		return perKey(p, model)
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].rank < rs[j].rank })
	out := make([]candidate, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.c)
	}
	return out
}

// plan is the primary's candidates, then its fallbacks', as candidates
// gives them, with each provider's keys ordered for the request from the
// client's protocol from.
func (s *Server) plan(p provider.Provider, model string, from provider.Protocol) []candidate {
	out := providerPlan(p, model, from)
	seen := map[string]bool{p.ID + "/" + model: true}
	for _, id := range p.Fallback {
		fp, fm, ok := provider.Resolve(id)
		if !ok || seen[fp.ID+"/"+fm] {
			continue
		}
		seen[fp.ID+"/"+fm] = true
		out = append(out, providerPlan(fp, fm, from)...)
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

// anthropicView is p as it reaches Anthropic's endpoint, for token counting:
// the key to send with, or false when no key may reach that endpoint.
func anthropicView(p provider.Provider) (provider.Provider, bool) {
	if !limited(p) {
		return p, p.Anthropic != ""
	}
	for _, want := range []provider.Protocol{provider.Anthropic, ""} {
		for _, k := range p.KeysOn() {
			if k.Protocol != want {
				continue
			}
			if q := p.WithKey(k); q.Anthropic != "" {
				return q, true
			}
		}
	}
	return p, false
}
