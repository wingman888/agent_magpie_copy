package provider

import (
	"fmt"
	"strings"
)

// SetFallback replaces a provider's fallback list, in priority order. An
// empty list clears it. A signed-in account keeps it beside its model picks,
// as a stored entry with no URL, the same way its picks are kept.
func SetFallback(id string, list []string) error {
	list = cleanList(list)
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == id {
			f.Providers[i].Fallback = list
			return store(f)
		}
	}
	if _, ok := find(Accounts(), id); ok {
		f.Providers = append(f.Providers, Provider{ID: id, Fallback: list})
		return store(f)
	}
	return fmt.Errorf("no provider %q — magpie providers lists them", id)
}

// ResolveFallback reads one fallback entry, "provider/model", into the
// provider that serves it and the model's own name there. The provider part
// is an id or a name, as in Find; the model is everything after its first
// slash, so "gw/deepseek/deepseek-chat" names model "deepseek/deepseek-chat"
// of provider gw. ok is false for an entry it cannot read: no slash, no
// model, an unknown provider, or one that has no key or account yet.
func ResolveFallback(ref string) (Provider, string, bool) {
	pid, model, found := strings.Cut(strings.TrimSpace(ref), "/")
	pid, model = strings.TrimSpace(pid), strings.TrimSpace(model)
	if !found || pid == "" || model == "" {
		return Provider{}, "", false
	}
	p, err := Find(pid)
	if err != nil || !p.Ready() {
		return Provider{}, "", false
	}
	return *p, model, true
}

// Catalogs lists the models.dev catalogs the provider reads, in the order
// they were given. Catalog holds them comma-separated, as "openai,deepseek".
func (p Provider) Catalogs() []string {
	var out []string
	for _, c := range strings.Split(p.Catalog, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// cleanCatalogs normalizes a catalog value: names lowercased, blanks and
// repeats dropped, joined with commas. A single name comes back unchanged.
func cleanCatalogs(s string) string {
	var out []string
	for _, c := range strings.Split(s, ",") {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" && !contains(out, c) {
			out = append(out, c)
		}
	}
	return strings.Join(out, ",")
}
