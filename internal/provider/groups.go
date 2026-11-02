package provider

// Routing groups: several models — from one provider or many — offered to
// the agents as one, "group/<id>". The gateway routes a request for a group
// over the keys and accounts of all its members together.
//
// A group is the user's, kept in providers.json, or one magpie finds on its
// own: a model two or more ready providers serve under the same name. A
// found group is worked out afresh every time from the providers ready now
// and never stored; changing it makes it the user's, removing it hides it
// until it is shown again.

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// groupPrefix is the provider part of a group's model id: group/<id>.
const groupPrefix = "group"

// autoPrefix starts the id of a group magpie found on its own.
const autoPrefix = "auto-"

// Group is a routing group.
type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members"` // provider/model, in order
	// Routing is how requests spread over the members' keys and accounts:
	// "" smart, "order", "rotate" or "least-used", as for a provider.
	Routing string `json:"routing,omitempty"`
	// Affinity is "" auto, "session", "turn" or "off", as for a provider.
	Affinity string `json:"affinity,omitempty"`
	// Auto is set on a group magpie found on its own; never stored.
	Auto bool `json:"auto,omitempty"`
}

// Ref is the model id agents send for the group.
func (g Group) Ref() string { return groupPrefix + "/" + g.ID }

// Member is one of a group's models resolved to a ready provider.
type Member struct {
	Provider Provider
	Model    string // what magpie sends the vendor
	Ref      string // provider/model
}

// IsGroupRef reports whether a model id names a routing group.
func IsGroupRef(id string) bool {
	return strings.HasPrefix(strings.TrimSpace(id), groupPrefix+"/")
}

// groupID is the group id in "group/<id>" or a bare id.
func groupID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	return strings.TrimPrefix(id, groupPrefix+"/")
}

// cleanGroup is g as stored, or why it can't be.
func cleanGroup(g Group) (Group, error) {
	g.ID = groupID(g.ID)
	g.Name = strings.TrimSpace(g.Name)
	if g.ID == "" {
		g.ID = Slug(g.Name)
	}
	if g.ID == "" || g.ID != Slug(g.ID) {
		return g, fmt.Errorf("group id must be lowercase letters, digits and dashes, not %q", g.ID)
	}
	if g.Name == "" {
		g.Name = g.ID
	}
	var ms []string
	for _, m := range cleanList(g.Members) {
		if IsGroupRef(m) {
			continue // no groups in groups
		}
		ms = append(ms, m)
	}
	if len(ms) == 0 {
		return g, errors.New("组里至少要有一个模型")
	}
	g.Members = ms
	g.Routing = cleanRouting(g.Routing)
	g.Affinity = cleanAffinity(g.Affinity)
	g.Auto = false
	return g, nil
}

// SaveGroup adds or replaces one of the user's groups. Saving a group magpie
// found makes it the user's.
func SaveGroup(g Group) error {
	g, err := cleanGroup(g)
	if err != nil {
		return err
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

// DeleteGroup removes one of the user's groups; one magpie found is hidden,
// to be shown again with ShowGroup.
func DeleteGroup(id string) error {
	id = groupID(id)
	f := load()
	found := false
	keep := f.Groups[:0]
	for _, g := range f.Groups {
		if g.ID == id {
			found = true
			continue
		}
		keep = append(keep, g)
	}
	f.Groups = keep
	hidden := slices.Contains(f.HiddenGroups, id)
	auto := false
	for _, g := range autoGroups(providerEntries()) {
		if g.ID == id {
			auto = true
			break
		}
	}
	if auto && !hidden {
		f.HiddenGroups = append(f.HiddenGroups, id)
		found = true
	}
	if !found {
		return fmt.Errorf("no routing group %q", id)
	}
	return store(f)
}

// ShowGroup brings back a group magpie found that the user had removed.
func ShowGroup(id string) error {
	id = groupID(id)
	f := load()
	if !slices.Contains(f.HiddenGroups, id) {
		return nil
	}
	f.HiddenGroups = slices.DeleteFunc(f.HiddenGroups, func(x string) bool { return x == id })
	return store(f)
}

// Groups lists the routing groups: the user's in the order they were made,
// then the ones magpie found and the user left alone.
func Groups() []Group { return groupsOf(load(), providerEntries()) }

// HiddenGroups lists the found groups the user removed.
func HiddenGroups() []Group {
	f := load()
	var out []Group
	for _, g := range autoGroups(providerEntries()) {
		if slices.Contains(f.HiddenGroups, g.ID) && !slices.ContainsFunc(f.Groups, func(u Group) bool { return u.ID == g.ID }) {
			out = append(out, g)
		}
	}
	return out
}

func groupsOf(f file, entries []Entry) []Group {
	var out []Group
	for _, g := range f.Groups {
		if g, err := cleanGroup(g); err == nil && !slices.ContainsFunc(out, func(u Group) bool { return u.ID == g.ID }) {
			out = append(out, g)
		}
	}
	for _, g := range autoGroups(entries) {
		if slices.Contains(f.HiddenGroups, g.ID) || slices.ContainsFunc(out, func(u Group) bool { return u.ID == g.ID }) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// FindGroup looks a group up by "group/<id>" (or its bare id) and resolves
// the members that can be used now: their providers ready, each provider
// and model once. A group found with none of them ready is still found.
func FindGroup(id string) (Group, []Member, bool) {
	id = groupID(id)
	for _, g := range Groups() {
		if g.ID == id {
			return g, members(g), true
		}
	}
	return Group{}, nil, false
}

// members resolves a group's members to ready providers.
func members(g Group) []Member {
	all := All()
	var out []Member
	seen := map[string]bool{}
	for _, ref := range g.Members {
		ref = strings.TrimSpace(ref)
		if ref == "" || IsGroupRef(ref) {
			continue
		}
		var m Member
		if pid, model, ok := strings.Cut(ref, "/"); ok && model != "" {
			p, found := find(all, strings.ToLower(pid))
			if !found {
				for _, q := range all {
					if strings.EqualFold(q.Name, pid) {
						p, found = q, true
						break
					}
				}
			}
			if !found || !p.Ready() {
				continue
			}
			m = Member{Provider: p, Model: model}
		} else {
			p, model, ok := resolveModel(ref)
			if !ok {
				continue
			}
			m = Member{Provider: p, Model: model}
		}
		m.Ref = m.Provider.ID + "/" + m.Model
		if seen[m.Ref] {
			continue
		}
		seen[m.Ref] = true
		out = append(out, m)
	}
	return out
}

// groupEntry is a group as the catalog lists it: named the user's way, with
// the reasoning levels every member has; nil when no member is ready.
func groupEntry(g Group, ms []Member, entries []Entry) *Entry {
	if len(ms) == 0 {
		return nil
	}
	var efforts []string
	for i, m := range ms {
		es := memberEfforts(m, entries)
		if i == 0 {
			efforts = slices.Clone(es)
			continue
		}
		efforts = slices.DeleteFunc(efforts, func(e string) bool { return !slices.Contains(es, e) })
	}
	gg := g
	return &Entry{ID: g.Ref(), Model: ms[0].Model, Name: g.Name, Efforts: efforts,
		Provider: Provider{ID: groupPrefix, Name: "Routing group"}, Group: &gg, Members: ms}
}

// memberEfforts are the reasoning levels a member's model has.
func memberEfforts(m Member, entries []Entry) []string {
	for _, e := range entries {
		if e.Group == nil && e.Provider.ID == m.Provider.ID && e.Model == m.Model {
			return e.Efforts
		}
	}
	for _, am := range m.Provider.Available() {
		if am.ID == m.Model {
			return am.Efforts
		}
	}
	return nil
}

// ---- found groups --------------------------------------------------------------

// autoGroups are the models two or more ready providers serve under the same
// name, each a group of those providers in the order they were added.
func autoGroups(entries []Entry) []Group {
	type found struct {
		g      Group
		named  bool
		owners []string
	}
	var order []string
	byKey := map[string]*found{}
	for _, e := range entries {
		if e.Group != nil {
			continue
		}
		key := CanonicalModel(e.Model)
		if key == "" {
			continue
		}
		f := byKey[key]
		if f == nil {
			f = &found{g: Group{ID: autoPrefix + Slug(key), Auto: true}}
			byKey[key] = f
			order = append(order, key)
		}
		if slices.Contains(f.owners, e.Provider.ID) {
			continue // one model per provider
		}
		f.owners = append(f.owners, e.Provider.ID)
		f.g.Members = append(f.g.Members, e.Provider.ID+"/"+e.Model)
		// the name a vendor gives it, over one that only repeats the id
		if !f.named {
			if proper(e) {
				f.g.Name, f.named = e.Name, true
			} else if f.g.Name == "" {
				f.g.Name = e.Model
				if e.Name != "" {
					f.g.Name = e.Name
				}
			}
		}
	}
	var out []Group
	for _, k := range order {
		if f := byKey[k]; len(f.owners) >= 2 && f.g.ID != autoPrefix {
			out = append(out, f.g)
		}
	}
	return out
}

// proper reports whether a catalog entry has a display name of its own,
// not just its id over again.
func proper(e Entry) bool {
	n := strings.TrimSpace(e.Name)
	if n == "" {
		return false
	}
	return CanonicalModel(n) != CanonicalModel(e.Model) && !strings.EqualFold(n, e.Model)
}

var (
	versionDot = regexp.MustCompile(`(\d)\.(\d)`)
	dateSuffix = regexp.MustCompile(`-\d{8}$`)
)

// CanonicalModel is the form two vendors' ids for the same model share:
// lowercase, without the vendor in front ("anthropic/…"), the dots of a
// version as dashes ("5.5" is "5-5") and without a date at the end
// ("-20260801"). A variant after a colon (":batch") stays, so it is another
// model; dots that aren't between digits ("v1.beta") stay too.
func CanonicalModel(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	base, variant, hasVariant := strings.Cut(id, ":")
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	for {
		next := versionDot.ReplaceAllString(base, "$1-$2")
		if next == base {
			break
		}
		base = next
	}
	base = dateSuffix.ReplaceAllString(base, "")
	if hasVariant {
		return base + ":" + variant
	}
	return base
}

// SameModel reports whether two vendors' model ids name the same model.
func SameModel(a, b string) bool { return CanonicalModel(a) == CanonicalModel(b) }
