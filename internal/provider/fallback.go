package provider

import (
	"strings"
)

// A fallback is a "provider/model" the gateway may ask when a provider cannot
// answer: out of quota, rate limited, overloaded, or down. The list is kept in
// the order the user gave it; that order is the priority.

// FallbackTarget is one resolved fallback: a ready provider and the model
// magpie asks it for.
type FallbackTarget struct {
	Provider Provider
	Model    string
}

// ParseFallback splits a "provider/model" entry at its first slash. ok is
// false when either part is missing.
func ParseFallback(entry string) (id, model string, ok bool) {
	id, model, ok = strings.Cut(strings.TrimSpace(entry), "/")
	id, model = strings.TrimSpace(id), strings.TrimSpace(model)
	if !ok || id == "" || model == "" {
		return "", "", false
	}
	return id, model, true
}

// Fallbacks resolves a fallback list, in order. An entry that does not parse,
// names no provider magpie has, or names one that is not ready is skipped
// without a word.
func Fallbacks(list []string) []FallbackTarget {
	var out []FallbackTarget
	for _, entry := range list {
		id, model, ok := ParseFallback(entry)
		if !ok {
			continue
		}
		p, err := Find(id)
		if err != nil || !p.Ready() {
			continue
		}
		out = append(out, FallbackTarget{Provider: *p, Model: model})
	}
	return out
}

// SetFallback replaces a provider's fallback list. Empty entries and repeats
// are dropped; an empty list clears the fallbacks.
func SetFallback(id string, list []string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.Fallback = cleanList(list)
	return Save(*p)
}

// CatalogNames splits a catalog value into the models.dev ids it names, in the
// order given: lowercase, trimmed, with blanks and repeats dropped.
func CatalogNames(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name != "" && !contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}
