package provider

// Import writes providers from a configuration backup (see internal/backup).
// It is Save for many at once and without the sign-in checks: a restored
// provider may come without a key, which Save refuses, and the user adds
// that key afterwards with `magpie provider key`.

import "fmt"

// Imported is what Import wrote: the providers as stored, and the counts.
type Imported struct {
	Added    int
	Replaced int
	Stored   []Provider
}

// Import merges providers into the stored file, matched by id: one with the
// same id as a stored provider replaces it, any other is added. A replacement
// that carries no key keeps the stored one, with its other keys. Entries with
// an invalid id, the reserved id "magpie", or no base URL are skipped.
func Import(incoming []Provider) (Imported, error) {
	var out Imported
	f := load()
	for _, p := range incoming {
		p = normalize(p)
		p.IconURL = ""
		if p.ID == "" || p.ID != Slug(p.ID) || p.ID == "magpie" {
			continue
		}
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			continue
		}
		if p.Name == "" {
			p.Name = p.ID
		}
		at := -1
		for i := range f.Providers {
			if f.Providers[i].ID == p.ID {
				at = i
				break
			}
		}
		if at >= 0 {
			old := f.Providers[at]
			if p.Key == "" && len(p.Keys) == 0 {
				p.Key, p.KeyName, p.KeyProtocol, p.Keys = old.Key, old.KeyName, old.KeyProtocol, old.Keys
			}
			f.Providers[at] = p
			out.Replaced++
		} else {
			f.Providers = append(f.Providers, p)
			out.Added++
		}
		out.Stored = append(out.Stored, p)
	}
	if out.Added == 0 && out.Replaced == 0 {
		return out, nil
	}
	if err := store(f); err != nil {
		return Imported{}, fmt.Errorf("write providers: %w", err)
	}
	return out, nil
}
