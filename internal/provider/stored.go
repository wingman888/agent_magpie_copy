package provider

// Reading and writing providers.json as a whole, for magpie backup and
// magpie restore: a backup carries exactly what the user kept, and a
// restore puts it back without Save's demand for a key, so a backup made
// without keys still brings everything else over.

// Stored is providers.json as it is: the providers the user added, and the
// model picks kept for signed-in accounts (entries with no base URL).
// Nothing derived from an agent's sign-in is in it.
func Stored() []Provider { return load().Providers }

// ValidID reports whether id can name a provider the user keeps: lowercase
// letters, digits and dashes, and not "magpie", the gateway's own name.
func ValidID(id string) bool { return id != "" && id == Slug(id) && id != "magpie" }

// Put writes providers to providers.json as given: one with the id of a
// stored provider replaces it, any other is added. Entries without a valid
// id or a base URL are skipped. It reports how many were added and replaced.
func Put(ps []Provider) (added, replaced int, err error) {
	f := load()
	for _, p := range ps {
		p = normalize(p)
		p.IconURL = ""
		p.Account = nil
		if !ValidID(p.ID) || (p.Chat == "" && p.Responses == "" && p.Anthropic == "") {
			continue
		}
		if p.Name == "" {
			p.Name = p.ID
		}
		found := false
		for i := range f.Providers {
			if f.Providers[i].ID == p.ID {
				f.Providers[i] = p
				found = true
				break
			}
		}
		if found {
			replaced++
		} else {
			f.Providers = append(f.Providers, p)
			added++
		}
	}
	if added+replaced == 0 {
		return 0, 0, nil
	}
	if err := store(f); err != nil {
		return 0, 0, err
	}
	return added, replaced, nil
}
