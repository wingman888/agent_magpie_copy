package provider

import (
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// modelKey is the form in which two providers' spellings of one model meet:
// the same model written as claude-opus-5-5, anthropic/claude-opus-5.5,
// Claude-Opus-5.5 or claude-opus-5-5-20260801 has the same key. Rules:
//   - case is ignored;
//   - a vendor prefix (everything up to the last "/") is dropped;
//   - a dot between two digits is a version separator, so 5.5 is 5-5;
//   - a trailing 8-digit date ("-20260801") is dropped;
//   - a variant after ":" (such as ":batch") is a different model, so it is
//     kept in the key;
//   - any other dot (v1.beta) is left alone.
func modelKey(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	base, variant, hasVariant := strings.Cut(id, ":")
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	base = versionDotsToHyphens(base)
	base = stripDateSuffix(base)
	if hasVariant {
		return base + ":" + variant
	}
	return base
}

// versionDotsToHyphens turns a dot between two digits into a hyphen, and
// leaves every other dot as it is.
func versionDotsToHyphens(s string) string {
	b := []byte(s)
	for i := 1; i+1 < len(b); i++ {
		if b[i] == '.' && isASCIIDigit(b[i-1]) && isASCIIDigit(b[i+1]) {
			b[i] = '-'
		}
	}
	return string(b)
}

// stripDateSuffix drops a trailing "-YYYYMMDD" (eight digits after a hyphen).
func stripDateSuffix(s string) string {
	n := len(s)
	if n < 9 || s[n-9] != '-' {
		return s
	}
	for _, c := range []byte(s[n-8:]) {
		if !isASCIIDigit(c) {
			return s
		}
	}
	return s[:n-9]
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

// ProviderModels is one ready provider and the models it lists, given in the
// order the providers were added.
type ProviderModels struct {
	Provider string
	Models   []catalog.Model
}

// AutoMember is one provider's model inside an automatic group.
type AutoMember struct {
	Provider string
	Model    string
}

// AutoGroup is a group magpie builds by itself: the same model offered by two
// or more providers.
type AutoGroup struct {
	ID      string // "auto-" + the model key
	Name    string // a provider's formal display name, else the ID
	Members []AutoMember
}

// AutoGroups builds the automatic groups from the ready providers' lists. A
// model listed by one provider only forms no group; a model listed by two or
// more forms one, with its members in provider order. A provider that lists
// the same key twice counts once. Groups come out in the order their models
// first appear.
func AutoGroups(sources []ProviderModels) []AutoGroup {
	type entry struct {
		key     string
		members []AutoMember
		seen    map[string]bool // providers already counted
		name    string          // formal display name, if any
		first   string          // first member's model id
	}
	var order []string
	byKey := map[string]*entry{}
	for _, src := range sources {
		for _, m := range src.Models {
			key := modelKey(m.ID)
			if key == "" {
				continue
			}
			e, ok := byKey[key]
			if !ok {
				e = &entry{key: key, seen: map[string]bool{}, first: m.ID}
				byKey[key] = e
				order = append(order, key)
			}
			if e.seen[src.Provider] {
				continue
			}
			e.seen[src.Provider] = true
			e.members = append(e.members, AutoMember{Provider: src.Provider, Model: m.ID})
			if e.name == "" && m.Name != "" && m.Name != m.ID {
				e.name = m.Name
			}
		}
	}
	var out []AutoGroup
	for _, key := range order {
		e := byKey[key]
		if len(e.members) < 2 {
			continue
		}
		name := e.name
		if name == "" {
			name = e.first
		}
		out = append(out, AutoGroup{ID: "auto-" + e.key, Name: name, Members: e.members})
	}
	return out
}
