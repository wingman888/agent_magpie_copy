package provider

import (
	"strings"
)

// SplitRef splits a "provider/model" reference at its first slash. The
// model part may itself contain slashes ("openrouter/vendor/model").
func SplitRef(ref string) (pid, model string, ok bool) {
	pid, model, ok = strings.Cut(strings.TrimSpace(ref), "/")
	pid, model = strings.TrimSpace(pid), strings.TrimSpace(model)
	return pid, model, ok && pid != "" && model != ""
}

// Known reports whether a "provider/model" reference names a provider magpie
// has and a model that provider serves (exposed, or in its known list).
// It does not touch the network.
func Known(ref string) bool {
	pid, model, ok := SplitRef(ref)
	if !ok {
		return false
	}
	p, err := Find(pid)
	if err != nil {
		return false
	}
	for _, m := range p.Exposed() {
		if m.ID == model {
			return true
		}
	}
	for _, m := range p.Available() {
		if m.ID == model {
			return true
		}
	}
	return false
}
