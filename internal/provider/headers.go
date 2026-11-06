package provider

import (
	"net/http"
	"sort"
	"strings"
)

// cleanHeaders trims names and values and drops entries with an empty name.
// Names keep their case: "X-ORG-id" and "X-Org-Id" are two entries.
func cleanHeaders(hs map[string]string) map[string]string {
	var out map[string]string
	for k, v := range hs {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

// CustomHeaders are the extra headers magpie sends to this provider. Only a
// custom provider (one the user entered by URL and key) has them; a preset
// or a signed-in account ignores them.
func (p Provider) CustomHeaders() map[string]string {
	if p.Preset != "" || p.Account != nil {
		return nil
	}
	return p.Headers
}

// ApplyCustomHeaders adds the custom headers to a request already signed.
// It runs after the auth headers, so a custom header of the same name wins.
// Header names keep the user's spelling: Header.Set would canonicalize them,
// so the map is written directly.
func (p Provider) ApplyCustomHeaders(req *http.Request) {
	hs := p.CustomHeaders()
	if len(hs) == 0 {
		return
	}
	names := make([]string, 0, len(hs))
	for k := range hs {
		names = append(names, k)
	}
	sort.Strings(names)
	// drop whatever the request already carries under the same name,
	// whatever its spelling (Authorization set by Sign, say)
	for existing := range req.Header {
		for _, n := range names {
			if strings.EqualFold(existing, n) {
				delete(req.Header, existing)
				break
			}
		}
	}
	for _, n := range names {
		req.Header[n] = []string{hs[n]}
	}
}
