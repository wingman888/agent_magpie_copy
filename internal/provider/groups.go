package provider

// Routing groups: one or more models, from one provider or several, that an
// agent picks as if they were a single model ("group/<id>"). The gateway
// routes the request over every key or account of every member together.
//
// A model offered under the same name by two or more ready providers gets a
// group of its own without anyone asking. Those auto groups are never
// stored: they are worked out from the providers that are ready each time.
// Only what the user changes is written to the config — a group the user
// saved (an edited auto group becomes theirs), and the ids of auto groups
// the user removed.

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// groupPrefix is what an agent puts before a group's id to ask for it.
const groupPrefix = "group/"

// Group is a routing group.
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Members are "provider/model" references, in the order they are listed.
	Members []string `json:"members"`
	// Routing is how requests spread over the pooled keys and accounts:
	// "" smart, "order" member by member, "rotate", or "least-used".
	Routing string `json:"routing,omitempty"`
	// Affinity is as Provider.Affinity.
	Affinity string `json:"affinity,omitempty"`
	// Auto is set on a group magpie found by itself and the user has not
	// changed; it is derived and never stored.
	Auto bool `json:"auto,omitempty"`
}

// Member is a group member whose provider is ready: the provider, and the
// model as that vendor calls it.
type Member struct {
	Provider Provider
	Model    string
}

// Ref is the member as written in a group: provider/model.
func (m Member) Ref() string { return m.Provider.ID + "/" + m.Model }

// GroupID reads a model id an agent sent as a group's: "group/<id>".
func GroupID(model string) (string, bool) {
	id, ok := strings.CutPrefix(strings.TrimSpace(model), groupPrefix)
	return id, ok
}

func cleanGroupID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	return strings.TrimPrefix(id, groupPrefix)
}

var autoIDRe = regexp.MustCompile(`^auto-[a-z0-9._:+-]+$`)

func validGroupID(id string) bool {
	return id != "" && (id == Slug(id) || autoIDRe.MatchString(id))
}

// ---- the same model under different names ----------------------------------------

var dateSuffix = regexp.MustCompile(`-\d{8}$`)

// NormalModel is a model's name with the differences that don't make it
// another model taken out: case, a vendor prefix ("anthropic/…"), dots in
// version numbers ("5.5" is "5-5"), an 8-digit date suffix. A variant after
// a colon (":batch") is another model and stays; so does a dot that isn't
// between digits ("v1.beta").
func NormalModel(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	base, variant, hasVariant := strings.Cut(s, ":")
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	b := []byte(base)
	for i := 1; i+1 < len(b); i++ {
		if b[i] == '.' && b[i-1] >= '0' && b[i-1] <= '9' && b[i+1] >= '0' && b[i+1] <= '9' {
			b[i] = '-'
		}
	}
	base = dateSuffix.ReplaceAllString(string(b), "")
	if hasVariant {
		return base + ":" + variant
	}
	return base
}

// ---- finding groups ---------------------------------------------------------------

// autoGroups are the groups of models two or more of the entries' providers
// offer under one name, members in the order the providers were added.
func autoGroups(entries []Entry) []Group {
	type found struct {
		g      Group
		prov   map[string]bool
		formal bool
	}
	var order []string
	byKey := map[string]*found{}
	for _, e := range entries {
		if e.Group != nil {
			continue
		}
		key := NormalModel(e.Model)
		if key == "" {
			continue
		}
		f := byKey[key]
		if f == nil {
			id := "auto-" + strings.Join(strings.Fields(key), "-")
			f = &found{g: Group{ID: id, Name: e.Name, Auto: true}, prov: map[string]bool{}}
			byKey[key] = f
			order = append(order, key)
		}
		if f.prov[e.Provider.ID] {
			continue
		}
		f.prov[e.Provider.ID] = true
		f.g.Members = append(f.g.Members, e.Provider.ID+"/"+e.Model)
		if formal := e.Name != "" && !strings.EqualFold(e.Name, e.Model); formal && !f.formal {
			f.g.Name, f.formal = e.Name, true
		}
	}
	var out []Group
	for _, k := range order {
		if f := byKey[k]; len(f.g.Members) >= 2 {
			if f.g.Name == "" {
				f.g.Name = f.g.Members[0]
			}
			out = append(out, f.g)
		}
	}
	return out
}

// groupsOf lists the groups in force: the user's, then the auto ones they
// haven't hidden or taken over.
func groupsOf(entries []Entry) []Group {
	f := load()
	var out []Group
	taken := map[string]bool{}
	for _, g := range f.Groups {
		g.Auto = false
		out = append(out, g)
		taken[g.ID] = true
	}
	for _, g := range autoGroups(entries) {
		if !taken[g.ID] && !slices.Contains(f.HiddenGroups, g.ID) {
			out = append(out, g)
		}
	}
	return out
}

// Groups lists the routing groups: the user's, then those found by magpie.
func Groups() []Group { return groupsOf(modelEntries()) }

// resolveMembers is the members of a group that can be used now: the
// provider is there and ready. One listed twice, or the same model of the
// same provider under two spellings, counts once; a group in a group is
// ignored.
func (g Group) resolveMembers() []Member {
	var out []Member
	seen := map[string]bool{}
	for _, ref := range g.Members {
		pid, model, ok := strings.Cut(strings.TrimSpace(ref), "/")
		if !ok || pid == "" || model == "" || strings.HasPrefix(ref, groupPrefix) {
			continue
		}
		p, err := Find(pid)
		if err != nil || !p.Ready() || seen[p.ID+"\x00"+model] {
			continue
		}
		seen[p.ID+"\x00"+model] = true
		out = append(out, Member{Provider: *p, Model: model})
	}
	return out
}

// FindGroup looks a group up by its id, with or without "group/", and
// resolves the members that are ready. A group with none is found all the
// same: the caller says it has no model ready.
func FindGroup(id string) (Group, []Member, bool) {
	id = cleanGroupID(id)
	for _, g := range groupsOf(modelEntries()) {
		if g.ID == id {
			return g, g.resolveMembers(), true
		}
	}
	return Group{}, nil, false
}

// ---- keeping them -------------------------------------------------------------------

// SaveGroup adds or replaces a group of the user's. Saving an auto group
// makes it the user's, and brings it back if it was hidden.
func SaveGroup(g Group) error {
	g.ID, g.Name = cleanGroupID(g.ID), strings.TrimSpace(g.Name)
	if g.ID == "" {
		g.ID = Slug(g.Name)
	}
	if !validGroupID(g.ID) {
		return fmt.Errorf("group id must be lowercase letters, digits and dashes, not %q", g.ID)
	}
	if g.Name == "" {
		g.Name = g.ID
	}
	var members []string
	for _, m := range g.Members {
		m = strings.TrimSpace(m)
		if m == "" || strings.HasPrefix(m, groupPrefix) || slices.Contains(members, m) {
			continue
		}
		members = append(members, m)
	}
	if len(members) == 0 {
		return errors.New("组里至少要有一个模型")
	}
	g.Members, g.Auto = members, false
	g.Routing, g.Affinity = cleanRouting(g.Routing), cleanAffinity(g.Affinity)
	f := load()
	f.HiddenGroups = slices.DeleteFunc(f.HiddenGroups, func(h string) bool { return h == g.ID })
	for i := range f.Groups {
		if f.Groups[i].ID == g.ID {
			f.Groups[i] = g
			return store(f)
		}
	}
	f.Groups = append(f.Groups, g)
	return store(f)
}

// DeleteGroup removes a group of the user's. An auto group is only hidden;
// ShowGroup brings it back.
func DeleteGroup(id string) error {
	id = cleanGroupID(id)
	f := load()
	hidden := slices.Contains(f.HiddenGroups, id)
	auto := false
	for _, g := range autoGroups(modelEntries()) {
		auto = auto || g.ID == id
	}
	i := slices.IndexFunc(f.Groups, func(g Group) bool { return g.ID == id })
	if i < 0 && !auto {
		return fmt.Errorf("no group %q", id)
	}
	if i >= 0 {
		f.Groups = slices.Delete(f.Groups, i, i+1)
	}
	if auto && !hidden {
		f.HiddenGroups = append(f.HiddenGroups, id)
	}
	return store(f)
}

// ShowGroup brings back an auto group the user had removed.
func ShowGroup(id string) error {
	id = cleanGroupID(id)
	f := load()
	if !slices.Contains(f.HiddenGroups, id) {
		return nil
	}
	f.HiddenGroups = slices.DeleteFunc(f.HiddenGroups, func(h string) bool { return h == id })
	return store(f)
}

// HiddenGroups lists the auto groups the user removed that still exist.
func HiddenGroups() []Group {
	hidden := load().HiddenGroups
	var out []Group
	for _, g := range autoGroups(modelEntries()) {
		if slices.Contains(hidden, g.ID) {
			out = append(out, g)
		}
	}
	return out
}

// ---- in the catalog -----------------------------------------------------------------

// efforts are the reasoning levels every member supports.
func commonEfforts(ms []Member) []string {
	var common []string
	for i, m := range ms {
		var have []string
		for _, am := range m.Provider.Available() {
			if am.ID == m.Model {
				have = am.Efforts
				break
			}
		}
		if i == 0 {
			common = slices.Clone(have)
		} else {
			common = slices.DeleteFunc(common, func(e string) bool { return !slices.Contains(have, e) })
		}
	}
	return common
}

func groupEntries(entries []Entry) []Entry {
	var out []Entry
	for _, g := range groupsOf(entries) {
		ms := g.resolveMembers()
		if len(ms) == 0 {
			continue
		}
		g := g
		out = append(out, Entry{ID: groupPrefix + g.ID, Model: groupPrefix + g.ID, Name: g.Name, Efforts: commonEfforts(ms),
			Provider: Provider{ID: "group", Name: "Routing groups"}, Group: &g})
	}
	return out
}
