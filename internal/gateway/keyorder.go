package gateway

// Which of a provider's keys a request tries first. A key can be limited to
// one protocol (provider.KeyAccount.Protocol): a relay's Anthropic key is no
// use on its OpenAI endpoint, and the other way round. A limited key only
// ever goes to its own protocol's endpoint (provider.Provider.WithKey), and
// the keys are tried best fit first, so a Claude request reaches the
// Anthropic key before the OpenAI one, and a GPT request the reverse.

import (
	"regexp"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// family is the vendor a model belongs to, as far as its name says.
type family int

const (
	famUnknown family = iota
	famAnthropic
	famOpenAI
)

// openAIName matches the o-series reasoning models: o1 through o9, o3-mini.
var openAIName = regexp.MustCompile(`^o[1-9]`)

// modelFamily reads the vendor from a model name, ignoring any provider/
// prefix: "openrouter/openai/o3-mini" is OpenAI, "deepseek-chat" unknown.
func modelFamily(model string) family {
	name := strings.ToLower(model)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	switch {
	case strings.HasPrefix(name, "claude"):
		return famAnthropic
	case strings.HasPrefix(name, "gpt-"), strings.Contains(name, "codex"), openAIName.MatchString(name):
		return famOpenAI
	}
	return famUnknown
}

// keyFamily is the vendor a key limited to proto belongs to: Anthropic for
// the Anthropic protocol, OpenAI for Chat and Responses. An unlimited key
// (empty proto) belongs to none.
func keyFamily(proto provider.Protocol) family {
	switch proto {
	case provider.Anthropic:
		return famAnthropic
	case provider.Chat, provider.Responses:
		return famOpenAI
	}
	return famUnknown
}

// fit ranks a candidate for a request by model and the client's protocol;
// lower is better.
//
//	0  the key is this model's vendor's key, or the model's vendor is unknown
//	   and the key reaches the client's protocol without translation
//	1  the request has to be translated to reach the key's endpoint
//	2  the key is another vendor's (an OpenAI key for a Claude model)
//
// An unlimited key is never another vendor's, so it ranks by the protocol
// alone, as it always did.
func fit(c candidate, model string, from provider.Protocol) int {
	fam := modelFamily(model)
	kf := keyFamily(c.p.KeyProtocol)
	switch {
	case fam != famUnknown && kf != famUnknown && kf != fam:
		return 2
	case fam != famUnknown && kf == fam:
		return 0
	case c.p.Base(from) != "":
		return 0
	}
	return 1
}

// usableCandidates is the provider once per key it has on that can take the
// request, best fit first; keys of equal fit keep their order. A key limited
// to a protocol the provider has no endpoint for is left out: it is ignored,
// not an error. The result is empty when no key is usable.
func usableCandidates(p provider.Provider, model string, from provider.Protocol) []candidate {
	var out []candidate
	for _, k := range p.KeysOn() {
		if k.Protocol != "" && p.Base(k.Protocol) == "" {
			continue
		}
		out = append(out, candidate{p.WithKey(k), model, p.ID + "#" + provider.KeyID(k.Key)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return fit(out[i], model, from) < fit(out[j], model, from)
	})
	return out
}

// anthropicKey is the provider as a count_tokens request should reach it:
// with the best key that has an Anthropic endpoint. usable is false when no
// key on the provider can reach one, and the count is estimated instead.
func anthropicKey(p provider.Provider, model string) (q provider.Provider, usable bool) {
	if len(p.KeysOn()) == 0 {
		// no keys at all (a local server): as it always was
		return p, p.Anthropic != ""
	}
	for _, c := range usableCandidates(p, model, provider.Anthropic) {
		if c.p.Anthropic != "" {
			return c.p, true
		}
	}
	return p, false
}
