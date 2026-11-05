package provider

// Stored and WriteStored give the backup code the providers file as it is
// on disk. Nothing is normalized or filled in from presets, and signed-in
// accounts are not in it, so reading and writing it back changes nothing
// the user did not change.

// Stored is the providers as providers.json holds them.
func Stored() []Provider { return load().Providers }

// WriteStored replaces providers.json with ps, as given.
func WriteStored(ps []Provider) error {
	if ps == nil {
		ps = []Provider{}
	}
	return store(file{Providers: ps})
}
