package provider

// Fallback targets and multi-catalog lookups: where a provider falls back to
// when its candidates fail, and how one provider may draw model names and
// reasoning levels from several models.dev catalogs at once.

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// Target is one resolved fallback: a ready provider and the model to ask it for.
type Target struct {
	Provider Provider
	Model    string
}

// SetFallback replaces a provider's fallback list. Entries are cleaned (blank
// and repeated ones dropped); an entry that names nothing usable is skipped
// when it is used, not refused here.
func SetFallback(id string, list []string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	list = cleanList(list)
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == p.ID {
			f.Providers[i].Fallback = list
			return store(f)
		}
	}
	// a signed-in account has no entry of its own until it is given one
	f.Providers = append(f.Providers, Provider{ID: p.ID, Fallback: list})
	return store(f)
}

// FallbackTargets resolves the fallback list in order. "provider/model" names
// a provider magpie has and a model after its first "/"; anything else, or a
// provider that is not ready, is passed over silently.
func (p Provider) FallbackTargets() []Target {
	var out []Target
	for _, entry := range p.Fallback {
		pid, model, ok := strings.Cut(strings.TrimSpace(entry), "/")
		model = strings.TrimSpace(model)
		if !ok || model == "" {
			continue
		}
		q, err := Find(pid)
		if err != nil || !q.Ready() {
			continue
		}
		out = append(out, Target{Provider: *q, Model: model})
	}
	return out
}

// joinCatalogs cleans a comma-separated list of catalog ids: blanks and
// repeats go, the order stays.
func joinCatalogs(s string) string {
	var out []string
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" && !contains(out, n) {
			out = append(out, n)
		}
	}
	return strings.Join(out, ",")
}

// catalogModels lists the models of every catalog named, in the order named.
func catalogModels(names string) []catalog.Model {
	var out []catalog.Model
	seen := map[string]bool{}
	for _, n := range strings.Split(names, ",") {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		for _, m := range catalog.Provider(n) {
			if !seen[m.ID] {
				seen[m.ID] = true
				out = append(out, m)
			}
		}
	}
	return out
}

// matchModel finds a model in a list by id. When no id matches exactly, a
// "vendor/model" id matches the model after its last "/".
func matchModel(ms []catalog.Model, id string) (catalog.Model, bool) {
	for _, m := range ms {
		if m.ID == id {
			return m, true
		}
	}
	short := catalog.ShortID(id)
	if short == id {
		return catalog.Model{}, false
	}
	for _, m := range ms {
		if m.ID == short {
			return m, true
		}
	}
	return catalog.Model{}, false
}

// checkCatalogs reports a catalog list with a name that is not a plain id.
func checkCatalogs(s string) error {
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" && n != Slug(n) {
			return fmt.Errorf("catalog %q is not a models.dev id", n)
		}
	}
	return nil
}
