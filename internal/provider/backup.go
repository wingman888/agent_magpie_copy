package provider

import (
	"errors"
	"fmt"
)

// Stored lists the providers the user entered themselves, as kept in
// providers.json: never a signed-in agent, nor the model picks kept for
// one. It is what a backup carries; subscriptions stay with each machine.
func Stored() []Provider {
	var out []Provider
	for _, p := range load().Providers {
		p = normalize(p)
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			continue
		}
		p.Account = nil
		out = append(out, p)
	}
	return out
}

// ValidID reports whether id can name a stored provider: a slug, and not
// "magpie", which is the gateway itself.
func ValidID(id string) bool {
	return id != "" && id == Slug(id) && id != "magpie"
}

// Put writes a provider restored from a backup, adding it or replacing the
// stored one with the same id. Unlike Save it takes a provider without its
// key — a backup made without keys — so the user can add one afterwards.
func Put(p Provider) (replaced bool, err error) {
	p = normalize(p)
	p.IconURL = ""
	p.Account = nil
	if !ValidID(p.ID) {
		return false, fmt.Errorf("provider id must be lowercase letters, digits and dashes (and not \"magpie\"), not %q", p.ID)
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return false, errors.New("a provider needs a base URL")
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
