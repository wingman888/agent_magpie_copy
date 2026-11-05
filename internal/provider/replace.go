package provider

// KeyOnReplace decides whether an import that replaces the provider old, and
// carries no key of its own, keeps old's key. A key belongs to the vendor it
// was given to, so it is kept only when every host next talks to is a host
// old already talked to. kept is false when there is nothing to keep.
func KeyOnReplace(old, next Provider) (key string, kept bool) {
	if next.Key != "" {
		return next.Key, false
	}
	if old.Key == "" {
		return "", false
	}
	oldHosts := providerHosts(old)
	nextHosts := providerHosts(next)
	if len(nextHosts) == 0 {
		return "", false
	}
	for _, h := range nextHosts {
		if !containsHost(oldHosts, h) {
			return "", false
		}
	}
	return old.Key, true
}

// providerHosts lists the distinct hosts of a provider's base URLs.
func providerHosts(p Provider) []string {
	var out []string
	for _, proto := range Protocols {
		if u := p.Base(proto); u != "" {
			if h := HostOf(u); h != "" && !containsHost(out, h) {
				out = append(out, h)
			}
		}
	}
	return out
}

func containsHost(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
