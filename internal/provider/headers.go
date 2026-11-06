package provider

import (
	"sort"
	"strings"
)

// cleanHeaders trims names and values, drops entries with no name, and gives
// back nil for an empty map so nothing is written for it.
func cleanHeaders(m map[string]string) map[string]string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out map[string]string
	for _, k := range keys {
		name := strings.TrimSpace(k)
		if name == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[name] = strings.TrimSpace(m[k])
	}
	return out
}

// customHeaders are the extra headers that go with a request to the
// provider: a custom provider's own, never a preset's or an account's.
func (p Provider) customHeaders() map[string]string {
	if p.Preset != "" || p.Account != nil {
		return nil
	}
	// cleaned here too: a hand-edited providers.json or a Provider built in
	// code may still carry untrimmed or empty names.
	return cleanHeaders(p.Headers)
}
