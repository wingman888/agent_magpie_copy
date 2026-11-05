package gateway

// A key can be limited to one protocol (provider.KeyAccount.Protocol, and
// Provider.KeyProtocol for the first key). Which key a request tries first
// depends on the model's vendor line: a Claude model goes to the Anthropic
// key, a GPT model to the OpenAI-style key. Keys that fit less well stay in
// the list, behind the ones that fit, as a fallback.

import (
	"regexp"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// family is the vendor line a model belongs to, judged from its name.
type family int

const (
	familyUnknown family = iota
	familyAnthropic
	familyOpenAI
)

// openAIModel matches the o-series reasoning models: o1 through o9.
var openAIModel = regexp.MustCompile(`^o[1-9]`)

// modelFamily says which vendor line a model name belongs to. A provider
// prefix ("openrouter/openai/o3-mini") is ignored; anything unrecognized,
// such as "deepseek-chat", is familyUnknown.
func modelFamily(model string) family {
	name := model
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.HasPrefix(name, "claude"):
		return familyAnthropic
	case strings.HasPrefix(name, "gpt-"), strings.Contains(name, "codex"), openAIModel.MatchString(name):
		return familyOpenAI
	}
	return familyUnknown
}

// keyTier ranks a key for a model sent by client protocol from. 0 fits
// best, 1 needs a protocol conversion (or is unlimited), 2 belongs to the
// other vendor line.
func keyTier(model string, from, kp provider.Protocol) int {
	switch modelFamily(model) {
	case familyAnthropic:
		switch kp {
		case provider.Anthropic:
			return 0
		case "":
			return 1
		}
		return 2
	case familyOpenAI:
		switch kp {
		case provider.Chat, provider.Responses:
			return 0
		case "":
			return 1
		}
		return 2
	}
	// model unknown: a key that speaks the client's protocol directly first
	switch {
	case kp == "":
		return 1
	case kp == from:
		return 0
	}
	return 2
}

// usableKeys is the keys that may take a request for model from client
// protocol from, best first; keys of equal rank keep their order. A key
// limited to a protocol the provider has no endpoint for is left out. ok is
// false when no key is left, and the caller then keeps the keys as they are.
func usableKeys(p provider.Provider, model string, from provider.Protocol, keys []provider.KeyAccount) (out []provider.KeyAccount, ok bool) {
	type ranked struct {
		k    provider.KeyAccount
		tier int
	}
	var rs []ranked
	for _, k := range keys {
		if k.Protocol != "" && p.Base(k.Protocol) == "" {
			continue
		}
		rs = append(rs, ranked{k, keyTier(model, from, k.Protocol)})
	}
	if len(rs) == 0 {
		return nil, false
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].tier < rs[j].tier })
	for _, r := range rs {
		out = append(out, r.k)
	}
	return out, true
}

// anthropicKey is the provider to send a count_tokens request to, with the
// key that fits best. ok is false when no key may go to the Anthropic
// endpoint: only keys limited to another protocol are usable.
func anthropicKey(p provider.Provider, model string) (q provider.Provider, ok bool) {
	ordered, usable := usableKeys(p, model, provider.Anthropic, p.KeysOn())
	if !usable {
		// no key fits any endpoint: keep the default, the provider as it is
		return p, true
	}
	for _, k := range ordered {
		if k.Protocol == "" || k.Protocol == provider.Anthropic {
			return p.WithKey(k), true
		}
	}
	return p, false
}
