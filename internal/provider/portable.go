package provider

// Raw access to providers.json for backup and restore. Unlike All, nothing
// here adds preset defaults or the signed-in accounts: the entries come back
// exactly as the file keeps them, so a backup restores what was on disk.

// Stored returns the entries of providers.json as they are on disk. An entry
// with no base URL is the model picks of a signed-in account (see All).
func Stored() []Provider {
	return load().Providers
}

// PutStored writes ps as the whole of providers.json, after the icons they
// refer to are in place. Callers merge with Stored first.
func PutStored(ps []Provider) error {
	return store(file{Providers: ps})
}
