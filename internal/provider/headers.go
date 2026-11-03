package provider

import (
	"net/http"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// cleanHeaders trims names and values, drops entries with no name, and
// keeps one entry per name ignoring case (the later name in sorted order
// wins, so the result does not depend on map iteration). An empty map is
// nil, so it is not written to providers.json.
func cleanHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	var out map[string]string
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		for k := range out {
			if strings.EqualFold(k, name) {
				delete(out, k)
			}
		}
		out[name] = strings.TrimSpace(h[raw])
	}
	return out
}

// CustomHeaders are the extra headers the user gave a custom provider. Presets
// and signed-in accounts never send them, even if a file carries some.
func (p Provider) CustomHeaders() map[string]string {
	if p.Account != nil || p.Preset != "" {
		return nil
	}
	return p.Headers
}

// ApplyHeaders adds the custom headers to a request. Call it after Sign, so
// they can override the headers magpie or the auth scheme set.
func (p Provider) ApplyHeaders(req *http.Request) {
	catalog.SetHeaders(req, p.CustomHeaders())
}
