package provider

// Routing groups: one or more models, from one provider or several, that an
// agent picks as one ("group/<id>") and the gateway routes over all their
// keys and accounts together.
//
// Groups come from two places. The user makes them (kept in providers.json),
// and magpie discovers them: a model that two or more ready providers serve
// under the same name (see CanonicalModel) becomes "auto-<model>". A
// discovered group is worked out afresh from the providers each time and
// never stored; once the user changes it, it is theirs and stored like any
// other, and one the user removes is only hidden, to be shown again.

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// groupPrefix is the provider part of a group's model id: "group/<id>".
const groupPrefix = "group"

// GroupPrefix is what a routing group's model id starts with.
const GroupPrefix = groupPrefix + "/"

// Group is a routing group.
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Members are the models in it, as agents pick them ("provider/model"),
	// in order.
	Members []string `json:"members"`
	// Routing is how requests spread over the members' keys and accounts,
	// with the same values as Provider.Routing.
	Routing string `json:"routing,omitempty"`
	// Affinity is how a conversation sticks to the key or account that
	// answered it last, with the same values as Provider.Affinity.
	Affinity string `json:"affinity,omitempty"`
	// Auto is set on a group magpie discovered; derived, never stored.
	Auto bool `json:"auto,omitempty"`
}

// Model is the id agents send for the group: "group/<id>".
func (g Group) Model() string { return GroupPrefix + g.ID }

// Member is one model of a group as it can be reached now.
type Member struct {
	ID       string   // "provider/model", as agents pick it
	Provider Provider // the provider serving it
	Model    string   // the vendor's model id
}

// IsGroup reports whether a model id names a routing group.
func IsGroup(model string) bool {
	return strings.HasPrefix(strings.TrimSpace(model), GroupPrefix)
}

// groupID is the id in "group/<id>" (or a bare id).
func groupID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	return strings.TrimPrefix(id, GroupPrefix)
}

// ---- the same model under other spellings -----------------------------------

var (
	dateSuffix = regexp.MustCompile(`-\d{8}$`)
	notIDChars = regexp.MustCompile(`[^a-z0-9-]+`)
)

// CanonicalModel is the spelling a model id is compared by, so the same
// model from different vendors is known as one: lowercase, without the
// vendor's prefix ("anthropic/…"), dots in version numbers as dashes ("5.5"
// is "5-5"), and without a date suffix ("-20260801"). A variant after a
// colon (":batch") is a model of its own and stays; a dot that isn't
// between digits ("v1.beta") stays too.
func CanonicalModel(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	base, variant, hasVariant := strings.Cut(id, ":")
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	b := []byte(base)
	for i := 1; i+1 < len(b); i++ {
		if b[i] == '.' && isDigit(b[i-1]) && isDigit(b[i+1]) {
			b[i] = '-'
		}
	}
	base = dateSuffix.ReplaceAllString(string(b), "")
	if hasVariant {
		return base + ":" + variant
	}
	return base
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// autoID is the id of the group discovered for a canonical model.
func autoID(canonical string) string {
	return "auto-" + strings.Trim(notIDChars.ReplaceAllString(canonical, "-"), "-")
}

// ---- the groups ---------------------------------------------------------------

// Groups lists the routing groups: the user's, in the order they were made,
// then the discovered ones the user hasn't removed or changed.
func Groups() []Group { return groupsOf(load(), readyProviders()) }

// HiddenGroups lists the discovered groups the user removed.
func HiddenGroups() []Group {
	f := load()
	var out []Group
	for _, g := range discover(readyProviders()) {
		if slices.Contains(f.HiddenGroups, g.ID) && !hasGroup(f.Groups, g.ID) {
			out = append(out, g)
		}
	}
	return out
}

func readyProviders() []Provider {
	var out []Provider
	for _, p := range All() {
		if p.Ready() {
			out = append(out, p)
		}
	}
	return out
}

func hasGroup(gs []Group, id string) bool {
	for _, g := range gs {
		if g.ID == id {
			return true
		}
	}
	return false
}

func groupsOf(f file, ready []Provider) []Group {
	var out []Group
	for _, g := range f.Groups {
		g = cleanGroup(g)
		g.Auto = false
		if g.ID != "" && !hasGroup(out, g.ID) {
			out = append(out, g)
		}
	}
	for _, g := range discover(ready) {
		if !hasGroup(out, g.ID) && !slices.Contains(f.HiddenGroups, g.ID) {
			out = append(out, g)
		}
	}
	return out
}

// discover finds the models two or more of the ready providers serve under
// one name, each a group with a member per provider, in the order the
// providers were added.
func discover(ready []Provider) []Group {
	type found struct {
		g         Group
		providers []string
		named     bool
	}
	var order []string
	byCanon := map[string]*found{}
	for _, p := range ready {
		for _, m := range p.Exposed() {
			c := CanonicalModel(m.ID)
			if c == "" {
				continue
			}
			f := byCanon[c]
			if f == nil {
				f = &found{g: Group{ID: autoID(c), Auto: true}}
				byCanon[c] = f
				order = append(order, c)
			}
			if slices.Contains(f.providers, p.ID) {
				continue // one member per provider
			}
			f.providers = append(f.providers, p.ID)
			f.g.Members = append(f.g.Members, p.ID+"/"+m.ID)
			named := m.Name != "" && m.Name != m.ID
			if f.g.Name == "" || (named && !f.named) {
				f.g.Name, f.named = m.ID, false
				if named {
					f.g.Name, f.named = m.Name, true
				}
			}
		}
	}
	var out []Group
	for _, c := range order {
		if f := byCanon[c]; len(f.providers) >= 2 {
			out = append(out, f.g)
		}
	}
	return out
}

// cleanGroup tidies a group as given: id from the name when missing, the
// name from the id, members trimmed and without repeats or other groups,
// routing and affinity magpie doesn't know as their defaults.
func cleanGroup(g Group) Group {
	g.Name = strings.TrimSpace(g.Name)
	g.ID = Slug(groupID(g.ID))
	if g.ID == "" {
		g.ID = Slug(g.Name)
	}
	if g.Name == "" {
		g.Name = g.ID
	}
	var ms []string
	for _, m := range g.Members {
		m = strings.TrimSpace(m)
		if m == "" || IsGroup(m) || strings.EqualFold(m, groupPrefix) || slices.Contains(ms, m) {
			continue // a group in a group is ignored
		}
		ms = append(ms, m)
	}
	g.Members = ms
	g.Routing = cleanRouting(g.Routing)
	g.Affinity = cleanAffinity(g.Affinity)
	return g
}

// SaveGroup adds a routing group of the user's, or replaces the one with its
// id. Changing a discovered group makes it the user's.
func SaveGroup(g Group) error {
	g = cleanGroup(g)
	g.Auto = false
	if g.ID == "" {
		return errors.New("a routing group needs a name")
	}
	if len(g.Members) == 0 {
		return errors.New("组里至少要有一个模型 (a routing group needs at least one model)")
	}
	f := load()
	f.HiddenGroups = slices.DeleteFunc(f.HiddenGroups, func(id string) bool { return id == g.ID })
	for i := range f.Groups {
		if f.Groups[i].ID == g.ID {
			f.Groups[i] = g
			return store(f)
		}
	}
	f.Groups = append(f.Groups, g)
	return store(f)
}

// DeleteGroup removes a routing group: one the user made is deleted, one
// magpie discovered is hidden, to be shown again with ShowGroup.
func DeleteGroup(id string) error {
	id = groupID(id)
	f := load()
	found := false
	if hasGroup(f.Groups, id) {
		found = true
		f.Groups = slices.DeleteFunc(f.Groups, func(g Group) bool { return g.ID == id })
	}
	// a discovered group would come back on its own: hide it
	if hasGroup(discover(readyProviders()), id) {
		found = true
		if !slices.Contains(f.HiddenGroups, id) {
			f.HiddenGroups = append(f.HiddenGroups, id)
		}
	}
	if !found {
		return fmt.Errorf("no routing group %q", id)
	}
	return store(f)
}

// ShowGroup brings back a discovered group the user had removed.
func ShowGroup(id string) error {
	id = groupID(id)
	f := load()
	if slices.Contains(f.HiddenGroups, id) {
		f.HiddenGroups = slices.DeleteFunc(f.HiddenGroups, func(h string) bool { return h == id })
		return store(f)
	}
	if hasGroup(groupsOf(f, readyProviders()), id) {
		return nil // shown already
	}
	return fmt.Errorf("no hidden routing group %q", id)
}

// FindGroup looks a routing group up by its model id ("group/<id>") or bare
// id, with the members that can be reached now: their provider is there,
// ready, and serves the model. A member listed twice counts once, and a
// member's own fallback is not the group's.
func FindGroup(id string) (Group, []Member, bool) {
	ready := readyProviders()
	return findGroupIn(groupsOf(load(), ready), ready, id)
}

func findGroupIn(gs []Group, ready []Provider, id string) (Group, []Member, bool) {
	id = groupID(id)
	for _, g := range gs {
		if g.ID == id {
			return g, membersOf(g, ready), true
		}
	}
	return Group{}, nil, false
}

// membersOf resolves a group's members among the ready providers.
func membersOf(g Group, ready []Provider) []Member {
	var out []Member
	seen := map[string]bool{}
	for _, ref := range g.Members {
		pid, model, ok := strings.Cut(strings.TrimSpace(ref), "/")
		if !ok || model == "" || strings.EqualFold(pid, groupPrefix) {
			continue
		}
		p, ok := readyByRef(ready, pid)
		if !ok || !servesModel(p, model) {
			continue
		}
		key := p.ID + "/" + model
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Member{ID: key, Provider: p, Model: model})
	}
	return out
}

// readyByRef finds a ready provider by id, or by name case-insensitively.
func readyByRef(ready []Provider, ref string) (Provider, bool) {
	q := strings.ToLower(strings.TrimSpace(ref))
	for _, p := range ready {
		if p.ID == q {
			return p, true
		}
	}
	for _, p := range ready {
		if strings.ToLower(p.Name) == q {
			return p, true
		}
	}
	return Provider{}, false
}

// servesModel reports whether p serves model: it is exposed or listed, or
// nothing is known of what p serves.
func servesModel(p Provider, model string) bool {
	avail := p.Available()
	if len(avail) == 0 {
		return true
	}
	return modelIn(avail, model) || modelIn(p.Exposed(), model)
}

func modelIn(ms []catalog.Model, id string) bool {
	for _, m := range ms {
		if m.ID == id {
			return true
		}
	}
	return false
}

// modelInfo is what p knows of model, its id as the name when nothing.
func modelInfo(p Provider, model string) catalog.Model {
	for _, list := range [][]catalog.Model{p.Exposed(), p.Available()} {
		for _, m := range list {
			if m.ID == model {
				return m
			}
		}
	}
	return catalog.Model{ID: model, Name: model}
}

// groupEntry is a group as the catalog lists it: the reasoning levels every
// member has. A group with no member to reach is left out.
func groupEntry(g Group, ms []Member) (Entry, bool) {
	if len(ms) == 0 {
		return Entry{}, false
	}
	var efforts []string
	for i, m := range ms {
		es := modelInfo(m.Provider, m.Model).Efforts
		if i == 0 {
			efforts = slices.Clone(es)
			continue
		}
		efforts = slices.DeleteFunc(efforts, func(e string) bool { return !slices.Contains(es, e) })
	}
	if len(efforts) == 0 {
		efforts = nil
	}
	gc := g
	return Entry{ID: g.Model(), Model: g.Model(), Name: g.Name, Efforts: efforts,
		Provider: Provider{ID: groupPrefix, Name: "Routing groups"}, Group: &gc, Members: ms}, true
}
