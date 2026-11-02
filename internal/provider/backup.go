package provider

// A backup carries providers.json's entries as they are stored, and puts
// them back on another machine (see internal/backup).

// Stored is the providers as providers.json keeps them: keys included,
// signed-in accounts only as the model picks the user made for them.
func Stored() []Provider { return load().Providers }

// Restore puts providers from a backup in. Each replaces the one here with
// its id; one that came without keys keeps the keys already here. It
// returns how many were new and how many replaced one here.
func Restore(ps []Provider) (added, replaced int, err error) {
	f := load()
	for _, p := range ps {
		p.IconURL = ""
		if p.ID == "" || p.ID != Slug(p.ID) || p.ID == "magpie" {
			continue
		}
		i := -1
		for j := range f.Providers {
			if f.Providers[j].ID == p.ID {
				i = j
				break
			}
		}
		if i < 0 {
			f.Providers = append(f.Providers, p)
			added++
			continue
		}
		if p.Key == "" && len(p.Keys) == 0 {
			p.Key, p.KeyName, p.Keys, p.KeyProtocol = f.Providers[i].Key, f.Providers[i].KeyName, f.Providers[i].Keys, f.Providers[i].KeyProtocol
		}
		f.Providers[i] = p
		replaced++
	}
	if added+replaced == 0 {
		return 0, 0, nil
	}
	return added, replaced, store(f)
}
