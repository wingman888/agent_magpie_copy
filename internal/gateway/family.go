package gateway

import (
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// family is the vendor family a model name belongs to.
type family int

const (
	familyUnknown family = iota
	familyAnthropic
	familyOpenAI
)

// modelFamily guesses a model's family from its name, ignoring any
// provider/ prefix: claude* is Anthropic; gpt-*, *codex* and o1..o9 are OpenAI.
func modelFamily(model string) family {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.HasPrefix(m, "claude"):
		return familyAnthropic
	case strings.HasPrefix(m, "gpt-"), strings.Contains(m, "codex"):
		return familyOpenAI
	case len(m) >= 2 && m[0] == 'o' && m[1] >= '1' && m[1] <= '9':
		return familyOpenAI
	}
	return familyUnknown
}

// keyRank says how well a key (as a provider already cut down to its
// protocol) suits a request: 0 best, 1 needs translation, 2 a key made for
// the other family of models.
func keyRank(q provider.Provider, fam family, from provider.Protocol) int {
	switch fam {
	case familyAnthropic:
		switch q.KeyProtocol {
		case provider.Anthropic:
			return 0
		case provider.Chat, provider.Responses:
			return 2
		}
		return 1
	case familyOpenAI:
		switch q.KeyProtocol {
		case provider.Chat, provider.Responses:
			return 0
		case provider.Anthropic:
			return 2
		}
		return 1
	}
	if q.Base(from) != "" {
		return 0
	}
	return 1
}

// keyCandidates is a provider once per usable key, best suited first.
func keyCandidates(p provider.Provider, model string, from provider.Protocol, keys []provider.KeyAccount) []candidate {
	type ranked struct {
		c    candidate
		rank int
	}
	fam := modelFamily(model)
	var list []ranked
	for _, k := range keys {
		if k.Protocol != "" && p.Base(k.Protocol) == "" {
			continue // no endpoint for this key: it sits out
		}
		q := p.WithKey(k)
		rest := p.ID
		if len(keys) >= 2 {
			rest = p.ID + "#" + provider.KeyID(k.Key)
		}
		list = append(list, ranked{candidate{q, model, rest}, keyRank(q, fam, from)})
	}
	if len(list) == 0 {
		return []candidate{{p, model, p.ID}}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].rank < list[j].rank })
	out := make([]candidate, len(list))
	for i, r := range list {
		out[i] = r.c
	}
	return out
}
