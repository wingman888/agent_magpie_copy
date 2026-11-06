package gateway

// Key protocols: a key may be limited to one protocol's endpoint (a relay
// that sells a key for its Anthropic endpoint and another for its OpenAI
// one). Such a key only ever reaches that endpoint. Among a provider's keys,
// the one that suits the model goes first: a Claude model on an Anthropic
// key, a GPT model on an OpenAI key. Keys that suit it less stay behind as
// the next tries, never dropped.

import (
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

type family int

const (
	famUnknown family = iota
	famAnthropic
	famOpenAI
)

// modelFamily says whose model a name is, from the name alone. A vendor
// prefix (provider/, openrouter/openai/) is ignored.
func modelFamily(model string) family {
	name := strings.ToLower(model)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	switch {
	case strings.HasPrefix(name, "claude"):
		return famAnthropic
	case strings.HasPrefix(name, "gpt-"), strings.Contains(name, "codex"):
		return famOpenAI
	case len(name) >= 2 && name[0] == 'o' && name[1] >= '1' && name[1] <= '9':
		return famOpenAI
	}
	return famUnknown
}

// fit ranks how well key k suits a request for model from client protocol
// from: 0 is the best. Unlimited keys sit in the middle for known families,
// so a model name alone never favours them; for an unknown model a key that
// speaks the client's protocol directly is best.
func fit(p provider.Provider, k provider.KeyAccount, model string, from provider.Protocol) int {
	switch modelFamily(model) {
	case famAnthropic:
		switch k.Protocol {
		case provider.Anthropic:
			return 0
		case "":
			return 1
		}
		return 2
	case famOpenAI:
		switch k.Protocol {
		case provider.Anthropic:
			return 2
		case "":
			return 1
		}
		return 0
	}
	if k.Protocol == from || (k.Protocol == "" && p.Base(from) != "") {
		return 0
	}
	return 1
}

// usable reports whether a key can be used on p at all: an unlimited key
// always, a limited one only when p has that protocol's endpoint.
func usable(p provider.Provider, k provider.KeyAccount) bool {
	return k.Protocol == "" || p.Base(k.Protocol) != ""
}
