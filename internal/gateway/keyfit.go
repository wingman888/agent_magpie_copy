package gateway

// Choosing a provider's key for a request. A key can be limited to one
// protocol (a relay's Anthropic key works only on its Anthropic endpoint, its
// OpenAI key only on the OpenAI one). Such a key is used only with that
// protocol's endpoint (provider.WithKey), keys whose protocol the provider
// lacks are left out, and the rest are ordered so the key that suits the
// model best goes first. Keys that fit equally keep their order, and none is
// dropped: the ones behind stay on as the next in line.

import (
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// family is the protocol family a model belongs to, judged by its name.
type family int

const (
	unknownFamily   family = iota // deepseek-chat and the like
	anthropicFamily               // claude-*
	openAIFamily                  // gpt-*, o1-o9 (o3-mini), *codex*
)

// modelFamily reads a model's family from its name. A provider prefix such as
// "openrouter/openai/" is dropped first, and case does not matter.
func modelFamily(model string) family {
	name := strings.ToLower(model)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	switch {
	case strings.HasPrefix(name, "claude"):
		return anthropicFamily
	case strings.HasPrefix(name, "gpt-"), strings.Contains(name, "codex"),
		len(name) >= 2 && name[0] == 'o' && name[1] >= '1' && name[1] <= '9':
		return openAIFamily
	}
	return unknownFamily
}

// keyFit ranks how well a key suits a request to p; lower is better.
//
//	0  the key speaks the model's own family: an Anthropic-limited key for
//	   Claude, a non-Anthropic key for GPT. A model of unknown family takes
//	   a key that speaks the client's protocol directly.
//	1  no limit (the key takes any protocol), or a key that needs a protocol
//	   conversion for an unknown-family model
//	2  a key for the other family: an OpenAI-class key for Claude, or an
//	   Anthropic key for GPT
func keyFit(p provider.Provider, k provider.KeyAccount, model string, from provider.Protocol) int {
	switch modelFamily(model) {
	case anthropicFamily:
		switch k.Protocol {
		case provider.Anthropic:
			return 0
		case "":
			return 1
		}
		return 2
	case openAIFamily:
		switch k.Protocol {
		case "":
			return 1
		case provider.Anthropic:
			return 2
		}
		return 0
	}
	if k.Protocol == from || (k.Protocol == "" && p.Base(from) != "") {
		return 0
	}
	return 1
}

// usableKey says whether a key takes part in requests to p. A key limited to
// a protocol the provider has no endpoint for does not.
func usableKey(p provider.Provider, k provider.KeyAccount) bool {
	return k.Protocol == "" || p.Base(k.Protocol) != ""
}

// orderKeys sorts keys best fit first for a request for model from a client
// speaking from. The sort is stable, so equally fitting keys keep their order.
func orderKeys(p provider.Provider, keys []provider.KeyAccount, model string, from provider.Protocol) []provider.KeyAccount {
	out := slices.Clone(keys)
	slices.SortStableFunc(out, func(a, b provider.KeyAccount) int {
		return keyFit(p, a, model, from) - keyFit(p, b, model, from)
	})
	return out
}
