package provider

import (
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// cleanHeaders trims names and values, drops entries with no name, and
// returns nil for an empty map. Presets (custom false) never carry headers.
func cleanHeaders(hs map[string]string, custom bool) map[string]string {
	if !custom || len(hs) == 0 {
		return nil
	}
	var out map[string]string
	for k, v := range hs {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = strings.TrimSpace(v)
	}
	return out
}

// customHeaders are the extra headers a request to this provider carries.
// Only custom providers have them; signed-in accounts and presets send none.
func (p Provider) customHeaders() map[string]string {
	if p.Preset != "" || p.Account != nil {
		return nil
	}
	return cleanHeaders(p.Headers, true)
}

// applyHeaders sets the provider's headers on req. They are written with
// the names exactly as the user typed them, so they go out with that case.
func applyHeaders(req *http.Request, p Provider) {
	for k, v := range p.customHeaders() {
		req.Header[k] = []string{v}
	}
}

// cleanCatalogs turns " openai, deepseek," into "openai,deepseek".
func cleanCatalogs(s string) string {
	return strings.Join(catalogIDs(s), ",")
}

// catalogIDs lists the models.dev catalogs a provider reads, in order.
func catalogIDs(s string) []string {
	var out []string
	for _, c := range strings.Split(s, ",") {
		if c = strings.TrimSpace(c); c != "" && !contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// validCatalogs is true when every name in a catalog list is a plain slug.
func validCatalogs(s string) bool {
	ids := catalogIDs(s)
	if len(ids) == 0 {
		return false
	}
	for _, c := range ids {
		if c != Slug(c) {
			return false
		}
	}
	return true
}

// catalogModels is the models of every catalog the provider names, in the
// order they are listed. A model id listed by an earlier catalog wins.
func (p Provider) catalogModels() []catalog.Model {
	var out []catalog.Model
	seen := map[string]bool{}
	for _, c := range catalogIDs(p.Catalog) {
		for _, m := range catalog.Provider(c) {
			if !seen[m.ID] {
				seen[m.ID] = true
				out = append(out, m)
			}
		}
	}
	return out
}

// matchModel finds the entry for a model id in a list. An id spelled
// "vendor/model", as relays do, matches the entry for the part after the
// last slash when nothing matches it exactly.
func matchModel(models []catalog.Model, id string) (catalog.Model, bool) {
	for _, m := range models {
		if m.ID == id {
			return m, true
		}
	}
	if i := strings.LastIndex(id, "/"); i >= 0 {
		rest := id[i+1:]
		for _, m := range models {
			if m.ID == rest {
				return m, true
			}
		}
	}
	return catalog.Model{}, false
}
