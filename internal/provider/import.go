package provider

// Import merges providers from a backup into the stored file. It works on
// the file as it is stored, not on All(), so what the backup does not cover
// (signed-in accounts' model picks, hidden accounts, anything else) is kept
// exactly as it was.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Imported says what Import did.
type Imported struct {
	Added    int      // providers that were not configured before
	Replaced int      // providers that replaced one with the same id
	NoKey    []string // names of restored providers with no key to use
}

// loadStrict reads the stored file, and unlike load it refuses a file it
// cannot parse: writing back over it would lose the user's providers.
func loadStrict() (file, error) {
	var f file
	b, err := os.ReadFile(Path())
	if errors.Is(err, fs.ErrNotExist) || (err == nil && len(bytes.TrimSpace(b)) == 0) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("%s: %w", Path(), err)
	}
	return f, nil
}

// Import merges ps into the stored providers, by id. A provider that replaces
// one with the same id keeps that one's key material when ps carries none
// (no primary key and no saved keys). Entries with an invalid id, the
// reserved id "magpie", or no base URL are skipped. Nothing is written when
// an error is returned.
func Import(ps []Provider) (Imported, error) {
	var res Imported
	f, err := loadStrict()
	if err != nil {
		return res, err
	}
	var restored []string // ids written, in order, deduplicated
	seen := map[string]bool{}
	for _, p := range ps {
		p = normalize(p)
		p.IconURL = ""
		if p.ID == "" || p.ID != Slug(p.ID) || p.ID == "magpie" {
			continue
		}
		if p.Name == "" {
			p.Name = p.ID
		}
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			continue // not a provider: only the picks of a signed-in account
		}
		idx := -1
		for i := range f.Providers {
			if f.Providers[i].ID == p.ID {
				idx = i
				break
			}
		}
		if idx >= 0 {
			old := f.Providers[idx]
			if p.Key == "" && len(p.Keys) == 0 {
				p.Key, p.KeyName, p.KeyProtocol, p.Keys = old.Key, old.KeyName, old.KeyProtocol, old.Keys
			}
			f.Providers[idx] = p
			res.Replaced++
		} else {
			f.Providers = append(f.Providers, p)
			res.Added++
		}
		if !seen[p.ID] {
			seen[p.ID] = true
			restored = append(restored, p.ID)
		}
	}
	if res.Added == 0 && res.Replaced == 0 {
		return res, nil
	}
	for _, id := range restored {
		for _, p := range f.Providers {
			if p.ID == id && p.Key == "" && len(p.Keys) == 0 {
				res.NoKey = append(res.NoKey, p.Name)
			}
		}
	}
	if err := store(f); err != nil {
		return Imported{}, err
	}
	return res, nil
}
