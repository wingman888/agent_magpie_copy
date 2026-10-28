package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

func errorf(format string, a ...any) error { return fmt.Errorf(format, a...) }

// manyModels is where "expose everything" stops being helpful.
const manyModels = 24

// Available lists every model the vendor is known to serve: its own list
// when fetched (see Fetch), else the models.dev catalog for the vendor.
func (p Provider) Available() []catalog.Model {
	if p.Account != nil && p.Account.models != nil {
		return p.Account.models()
	}
	known := catalog.Provider(p.Catalog)
	if live, _, ok := catalog.Live(p.ID); ok {
		return catalog.Decorate(live, known)
	}
	var out []catalog.Model
	for _, m := range known {
		if !strings.Contains(m.ID, "-exp") && !strings.Contains(m.ID, "preview") {
			out = append(out, m)
		}
	}
	return out
}

// Fetched reports when the vendor's own list was last fetched.
func (p Provider) Fetched() (time.Time, bool) {
	_, t, ok := catalog.Live(p.ID)
	return t, ok
}

// Fetch asks the vendor which models it serves and remembers the answer.
func (p Provider) Fetch(ctx context.Context) ([]catalog.Model, error) {
	if p.Account != nil && p.Account.fetch != nil {
		return p.Account.fetch(ctx)
	}
	var lastErr error
	for _, proto := range p.Speaks() {
		ms, err := catalog.Fetch(ctx, p.Base(proto), p.Key)
		if err == nil {
			return ms, catalog.SaveLive(p.ID, p.Base(proto), ms)
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errorf("%s has no endpoint to ask", p.Name)
	}
	return nil, lastErr
}

// Exposed lists the models dial offers to agents for this provider: the
// user's picks; else the preset's; else everything, when that is few.
func (p Provider) Exposed() []catalog.Model {
	avail := p.Available()
	byID := make(map[string]catalog.Model, len(avail))
	for _, m := range avail {
		byID[m.ID] = m
	}
	pick := func(ids []string) []catalog.Model {
		out := make([]catalog.Model, 0, len(ids))
		for _, id := range ids {
			if m, ok := byID[id]; ok {
				out = append(out, m)
			} else {
				out = append(out, catalog.Model{ID: id, Name: id, Provider: p.Catalog})
			}
		}
		return out
	}
	if len(p.Models) > 0 {
		return pick(p.Models)
	}
	if pr := Preset(p.Preset); pr != nil && len(pr.Defaults) > 0 {
		if _, fetched := p.Fetched(); !fetched {
			return pick(pr.Defaults)
		}
		var have []string
		for _, id := range pr.Defaults {
			if _, ok := byID[id]; ok {
				have = append(have, id)
			}
		}
		if len(have) > 0 {
			return pick(have)
		}
	}
	if len(avail) <= manyModels {
		return avail
	}
	return nil
}

// Chosen reports whether a model is exposed.
func (p Provider) Chosen(id string) bool {
	for _, m := range p.Exposed() {
		if m.ID == id {
			return true
		}
	}
	return false
}

// ---- the dial catalog ------------------------------------------------------
//
// Agents see one flat list of models across every provider, each spelled
// "provider/model" so nothing ever clashes. The bare model id works too
// when only one provider serves it.

// Entry is one model as the agents see it.
type Entry struct {
	ID       string   `json:"id"`    // what the agent sends dial
	Model    string   `json:"model"` // what dial sends the vendor
	Name     string   `json:"name"`
	Efforts  []string `json:"efforts,omitempty"`
	Provider Provider `json:"-"`
}

// Catalog lists every exposed model of every ready provider.
func Catalog() []Entry {
	var out []Entry
	for _, p := range All() {
		if !p.Ready() {
			continue
		}
		for _, m := range p.Exposed() {
			out = append(out, Entry{ID: p.ID + "/" + m.ID, Model: m.ID, Name: m.Name, Efforts: m.Efforts, Provider: p})
		}
	}
	return out
}

// Resolve maps an id an agent sent to a provider and the vendor's model id.
// It accepts catalog ids, "provider/model" for any model (exposed or not),
// and the bare model id when exactly one provider serves it.
func Resolve(id string) (Provider, string, bool) {
	id = strings.TrimSpace(id)
	entries := Catalog()
	for _, e := range entries {
		if e.ID == id {
			return e.Provider, e.Model, true
		}
	}
	if pid, model, ok := strings.Cut(id, "/"); ok {
		if p, err := Find(pid); err == nil && p.Ready() {
			return *p, model, true
		}
	}
	var hits []Entry
	for _, e := range entries {
		if e.Model == id {
			hits = append(hits, e)
		}
	}
	if len(hits) >= 1 {
		return hits[0].Provider, hits[0].Model, true
	}
	// not exposed, but some provider lists it
	var found []Provider
	for _, p := range All() {
		if !p.Ready() {
			continue
		}
		for _, m := range p.Available() {
			if m.ID == id {
				found = append(found, p)
				break
			}
		}
	}
	if len(found) == 1 {
		return found[0], id, true
	}
	return Provider{}, "", false
}

// IDs lists the catalog ids, for error messages.
func IDs() []string {
	var out []string
	for _, e := range Catalog() {
		out = append(out, e.ID)
	}
	sort.Strings(out)
	return out
}
