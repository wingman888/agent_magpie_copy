package provider

// PutAll writes providers as they are, replacing by id and appending new ones
// in the order given. Unlike Save it asks for neither a key nor a base URL,
// so a backup that carries a provider without its key can be restored; the
// caller decides what to keep. Pictures are not touched here.
func PutAll(ps []Provider) error {
	if len(ps) == 0 {
		return nil
	}
	f := load()
	for _, p := range ps {
		p = normalize(p)
		p.IconURL = "" // import-only: never stored
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
