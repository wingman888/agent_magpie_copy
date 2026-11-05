package gateway

// Keys limited to one protocol (see provider/keys.go): a key only talks to
// its own protocol's endpoint, and the keys that suit a request best go
// first. Which suit best depends on the model's family — a Claude model
// wants the Anthropic key, a GPT model an OpenAI one — and, when the name
// tells us nothing, on the key that speaks the client's protocol directly.

import (
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// family is whose model a name is.
type family int

const (
	familyUnknown family = iota
	familyAnthropic
	familyOpenAI
)

// modelFamily reads a model's family from its name, ignoring any provider
// prefix (relay/claude-opus-4-8, openrouter/openai/o3-mini).
func modelFamily(model string) family {
	name := strings.ToLower(model)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	switch {
	case strings.HasPrefix(name, "claude"):
		return familyAnthropic
	case strings.HasPrefix(name, "gpt-"), strings.Contains(name, "codex"):
		return familyOpenAI
	case len(name) >= 2 && name[0] == 'o' && name[1] >= '1' && name[1] <= '9':
		return familyOpenAI
	}
	return familyUnknown
}

// protocolFamily is whose models a protocol's endpoint serves.
func protocolFamily(proto provider.Protocol) family {
	switch proto {
	case provider.Anthropic:
		return familyAnthropic
	case provider.Chat, provider.Responses:
		return familyOpenAI
	}
	return familyUnknown
}

// Tiers of fit for a key, best first.
const (
	tierMatch   = iota // the key suits the model and needs no translation
	tierConvert        // usable only after translating, or a key with no limit
	tierOther          // a key for another vendor's models
)

// keyTier ranks how well key k suits a request for model from a client
// speaking from. A key with no limit is tierConvert: it works as it always
// has, but never ahead of a key made for the request.
func keyTier(k provider.KeyAccount, model string, from provider.Protocol) int {
	if k.Protocol == "" {
		return tierConvert
	}
	mf, kf := modelFamily(model), protocolFamily(k.Protocol)
	switch {
	case mf == familyUnknown:
		if k.Protocol == from {
			return tierMatch
		}
	case mf == kf:
		return tierMatch
	case kf != familyUnknown:
		return tierOther
	}
	return tierConvert
}

// keyUsable is whether a key takes part in requests at all: a key limited
// to a protocol the provider has no endpoint for is ignored.
func keyUsable(p provider.Provider, k provider.KeyAccount) bool {
	return k.Protocol == "" || p.Base(k.Protocol) != ""
}

// perKeyFor is perKey with the keys ordered by fit for the request, and each
// one talking only through the endpoint of its protocol. When no key is
// usable it is perKey as it always was.
func perKeyFor(p provider.Provider, model string, from provider.Protocol) []candidate {
	if p.Account != nil {
		return perKey(p, model)
	}
	keys := p.KeysOn()
	var usable []provider.KeyAccount
	for _, k := range keys {
		if keyUsable(p, k) {
			usable = append(usable, k)
		}
	}
	if len(usable) == 0 {
		return perKey(p, model)
	}
	sort.SliceStable(usable, func(i, j int) bool {
		return keyTier(usable[i], model, from) < keyTier(usable[j], model, from)
	})
	multi := len(keys) >= 2
	out := make([]candidate, 0, len(usable))
	for _, k := range usable {
		rest := p.ID
		if multi {
			rest = p.ID + "#" + provider.KeyID(k.Key)
		}
		out = append(out, candidate{p.WithKey(k), model, rest})
	}
	return out
}

// countProvider is p as it may count tokens on Anthropic's endpoint: with a
// key that can speak Anthropic, the Anthropic-limited keys first. ok is
// false when no such key exists, and then nothing may be sent there.
func countProvider(p provider.Provider) (provider.Provider, bool) {
	if p.Account != nil {
		return p, p.Anthropic != ""
	}
	keys := p.KeysOn()
	if len(keys) == 0 {
		return p, p.Anthropic != ""
	}
	for _, want := range []provider.Protocol{provider.Anthropic, ""} {
		for _, k := range keys {
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
