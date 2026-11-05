package provider

import (
	"errors"
	"fmt"
	"strings"
)

// Raw access to providers.json for backup and restore. Save and All apply
// rules a restore must not: Save wants a key, All hides the entries behind
// signed-in accounts. These two keep an entry exactly as it is stored.

// StoredProviders returns the entries in providers.json as they are saved:
// keys included, signed-in accounts' model picks included, nothing derived.
func StoredProviders() []Provider {
	return load().Providers
}

// PutStored writes p into providers.json as it is, replacing the saved entry
// with the same id or adding one. Unlike Save it does not require a key, so a
// provider without one can be stored and completed later. It reports whether
// an entry was replaced.
func PutStored(p Provider) (replaced bool, err error) {
	p = normalize(p)
	p.IconURL = ""
	p.Account = nil
	if p.ID == "" || p.ID != Slug(p.ID) {
		return false, fmt.Errorf("provider id must be lowercase letters, digits and dashes, not %q", p.ID)
	}
	if p.ID == "magpie" {
		return false, errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == p.ID {
			f.Providers[i] = p
			return true, store(f)
		}
	}
	f.Providers = append(f.Providers, p)
	return false, store(f)
}

// HasURL reports whether a provider entry has at least one base URL, that is,
// whether it is a provider rather than the model picks of a signed-in account.
func (p Provider) HasURL() bool {
	return strings.TrimSpace(p.Chat) != "" || strings.TrimSpace(p.Responses) != "" || strings.TrimSpace(p.Anthropic) != ""
}
