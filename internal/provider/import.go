package provider

import (
	"fmt"
	"strings"
)

// IconFileName is the picture file a provider's Icon points at, if it points
// at one this machine stores ("file:<name>"); ok is false for built-in icons.
func IconFileName(icon string) (name string, ok bool) {
	return strings.CutPrefix(icon, iconPrefix)
}

// ValidID reports whether id can name a provider: lowercase letters, digits
// and dashes, and not the gateway's own name "magpie".
func ValidID(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	return id != "" && id != "magpie" && id == Slug(id)
}

// Import writes a provider taken from a backup: it replaces the provider with
// the same id, or adds it. A backup that carries no key at all leaves this
// machine's key (and its other keys) in place for that id. It returns the
// provider as stored and whether an existing one was replaced.
func Import(p Provider) (stored Provider, replaced bool, err error) {
	p = normalize(p)
	p.IconURL = ""
	if !ValidID(p.ID) {
		return Provider{}, false, fmt.Errorf("provider id %q is not usable", p.ID)
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
		return Provider{}, false, fmt.Errorf("%s has no base URL", p.Name)
	}
	if a, ok := find(Accounts(), p.ID); ok {
		// as Save: a signed-in account keeps only the user's model picks
		p = Provider{ID: a.ID, Models: p.Models, Fallback: p.Fallback, Routing: p.Routing}
	}

	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID != p.ID {
			continue
		}
		old := f.Providers[i]
		if p.Key == "" {
			p.Key, p.KeyName, p.KeyProtocol = old.Key, old.KeyName, old.KeyProtocol
		}
		if len(p.Keys) == 0 {
			p.Keys = old.Keys
		}
		f.Providers[i] = p
		return p, true, store(f)
	}
	f.Providers = append(f.Providers, p)
	return p, false, store(f)
}
