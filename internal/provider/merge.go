package provider

// Merge writes providers that came from somewhere else (a backup) into the
// providers file, matched by id. Unlike Save it does not insist on a key: a
// provider restored without one is still written, and the caller reports it.
// An incoming provider that carries no key of its own keeps the key, key
// name, key protocol and extra accounts already on this machine.

// Merge returns the providers as they now stand for each incoming entry, plus
// how many were added and how many replaced an existing id. Ids are taken as
// given: the caller validates them.
func Merge(incoming []Provider) (merged []Provider, added, replaced int, err error) {
	f := load()
	for _, p := range incoming {
		p = normalize(p)
		p.IconURL = ""
		idx := -1
		for i := range f.Providers {
			if f.Providers[i].ID == p.ID {
				idx = i
				break
			}
		}
		if idx < 0 {
			f.Providers = append(f.Providers, p)
			added++
		} else {
			old := f.Providers[idx]
			if p.Key == "" && len(p.Keys) == 0 {
				p.Key, p.KeyName, p.KeyProtocol, p.Keys = old.Key, old.KeyName, old.KeyProtocol, old.Keys
			}
			f.Providers[idx] = p
			replaced++
		}
		merged = append(merged, p)
	}
	if len(incoming) == 0 {
		return nil, 0, 0, nil
	}
	return merged, added, replaced, store(f)
}
