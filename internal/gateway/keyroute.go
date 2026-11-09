package gateway

// Keys limited to a protocol: a relay may sell one key for its Anthropic
// endpoint (Claude models) and another for its OpenAI endpoint (GPT models).
// A request takes the key that fits its model best, best first; a key limited
// to a protocol the provider lacks is left out, and a key sent to another
// model family's endpoint is only ever the last try.

import (
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// family is the vendor family a model id belongs to, by its name alone.
type family int

const (
	familyUnknown family = iota
	familyAnthropic
	familyOpenAI
)

// familyOf reads a model's family from its name, ignoring any "provider/"
// prefix: claude* is Anthropic; gpt-*, *codex* and o1..o9* are OpenAI.
func familyOf(model string) family {
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

// Fit ranks how well a key suits a request; lower is better.
const (
	fitExact   = iota // the key's own family, or directly the client's protocol
	fitConvert        // usable, but the request must be translated
	fitOther          // another family's key: last resort
)

// keyFit ranks one key for a model and the client's protocol.
func keyFit(p provider.Provider, k provider.KeyAccount, fam family, from provider.Protocol) int {
	switch fam {
	case familyAnthropic:
		switch k.Protocol {
		case provider.Anthropic:
			return fitExact
		case "":
			return fitConvert
		}
		return fitOther
	case familyOpenAI:
		switch k.Protocol {
		case provider.Anthropic:
			return fitOther
		case "":
			return fitConvert
		}
		return fitExact
	}
	// a model we can't place: a key that speaks the client's protocol wins
	if k.Protocol == from || (k.Protocol == "" && p.Base(from) != "") {
		return fitExact
	}
	return fitConvert
}

// hasLimit is whether any of the keys is limited to a protocol.
func hasLimit(keys []provider.KeyAccount) bool {
	for _, k := range keys {
		if k.Protocol != "" {
			return true
		}
	}
	return false
}

// routeKeys is the keys a request may use, best fit first and, among equal
// fits, in their configured order. Empty when no key can be used at all.
func routeKeys(p provider.Provider, keys []provider.KeyAccount, model string, from provider.Protocol) []provider.KeyAccount {
	type ranked struct {
		k   provider.KeyAccount
		fit int
	}
	fam := familyOf(model)
	var rs []ranked
	for _, k := range keys {
		if k.Protocol != "" && p.Base(k.Protocol) == "" {
			continue // limited to an endpoint this provider doesn't have
		}
		rs = append(rs, ranked{k, keyFit(p, k, fam, from)})
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].fit < rs[j].fit })
	out := make([]provider.KeyAccount, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.k)
	}
	return out
}
