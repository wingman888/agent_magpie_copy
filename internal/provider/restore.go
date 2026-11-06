package provider

// Put writes providers as they are, replacing the stored entry with the same
// id and appending the rest. Unlike Save it does not insist on an API key or
// a base URL: a restored backup may hold providers whose key is still to be
// typed in. Icons are pruned as on every store, so write them first.
func Put(ps []Provider) error {
	if len(ps) == 0 {
		return nil
	}
	f := load()
	for _, p := range ps {
		p = normalize(p)
		p.IconURL = ""
		replaced := false
		for i := range f.Providers {
			if f.Providers[i].ID == p.ID {
				f.Providers[i] = p
				replaced = true
				break
			}
		}
		if !replaced {
			f.Providers = append(f.Providers, p)
		}
	}
	return store(f)
}
