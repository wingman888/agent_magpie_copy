package provider

// Routing groups: several models, from one vendor or several, that an agent
// picks as one. A group is named group/<id> and the gateway spreads its
// requests over the members' keys and accounts. Groups the user made are
// kept in providers.json; the ones magpie finds itself — a model more than
// one ready vendor serves under the same name — are worked out every time
// and written only when the user hides one.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// GroupPrefix starts the id of a routing group as agents send it.
const GroupPrefix = "group/"

// Routing values a group takes; "" is smart. Ordered and Rotate are the
// providers' own; a group spells least-used out (a provider keeps LeastUsed).
const GroupLeastUsed = "least-used"

// Affinity values: how long a conversation stays on the key or account that
// answered it last. "" is automatic.
const (
	AffinitySession = "session"
	AffinityTurn    = "turn"
	AffinityOff     = "off"
)

// autoPrefix starts the id of a group magpie discovers.
const autoPrefix = "auto-"

// groupProvider is the provider a group's catalog entry belongs to: a name
// of its own, so the lists show the groups under their heading. No vendor
// has the id "group" (Save refuses it).
var groupProvider = Provider{ID: "group", Name: "Routing groups"}

// Group is a routing group as the user keeps it, or as magpie finds it.
type Group struct {
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Members  []string `json:"members,omitempty"` // "provider/model", in order
	Routing  string   `json:"routing,omitempty"`
	Affinity string   `json:"affinity,omitempty"`
	// Hidden marks an automatic group the user removed. It holds nothing else.
	Hidden bool `json:"hidden,omitempty"`
	// Auto is set on a group magpie found; it is derived, never stored.
	Auto bool `json:"-"`
}

// Member is one model of a group, on the provider it is reached through.
type Member struct {
	Provider Provider
	Model    string
}

// ID is the member as the config spells it: "provider/model".
func (m Member) ID() string { return m.Provider.ID + "/" + m.Model }

// SaveGroup adds or replaces a routing group. Saving an automatic group's
// id takes that group over: it is the user's from then on.
func SaveGroup(g Group) error {
	g, err := g.clean()
	if err != nil {
		return err
	}
	f := load()
	for i := range f.Groups {
		if f.Groups[i].ID == g.ID {
			f.Groups[i] = g
			return store(f)
		}
	}
	f.Groups = append(f.Groups, g)
	return store(f)
}

// DeleteGroup removes a group the user made. An automatic one is hidden
// instead, so it does not come back on its own; ShowGroup brings it back.
// Deleting a group the user made under an automatic group's id hides that
// id too, so the discovered group does not reappear either.
func DeleteGroup(id string) error {
	id = groupID(id)
	f := load()
	auto := discovered(id)
	i := slices.IndexFunc(f.Groups, func(g Group) bool { return g.ID == id && !g.Hidden })
	switch {
	case i >= 0:
		f.Groups = slices.Delete(f.Groups, i, i+1)
		if auto {
			hide(&f, id)
		}
	case auto:
		hide(&f, id)
	default:
		return fmt.Errorf("no group %q", id)
	}
	return store(f)
}

// ShowGroup brings back an automatic group the user hid.
func ShowGroup(id string) error {
	id = groupID(id)
	f := load()
	for i, g := range f.Groups {
		if g.ID == id && g.Hidden {
			f.Groups = slices.Delete(f.Groups, i, i+1)
			return store(f)
		}
	}
	if slices.ContainsFunc(f.Groups, func(g Group) bool { return g.ID == id && !g.Hidden }) || discovered(id) {
		return nil
	}
	return fmt.Errorf("no group %q", id)
}

// FindGroup looks a group up by id, with or without the "group/" prefix,
// and resolves its members to the ones ready now. ok is false when the
// group is unknown, hidden, or has no member ready.
func FindGroup(id string) (Group, []Member, bool) {
	id = groupID(id)
	for _, g := range listGroups() {
		if g.ID == id {
			ms := resolveMembers(g.Members)
			return g, ms, len(ms) > 0
		}
	}
	return Group{}, nil, false
}

// SetAffinity sets how a provider's conversations keep to the key or
// account that answered them: "" automatic, session, turn or off.
func SetAffinity(providerID, affinity string) error {
	p, err := Find(providerID)
	if err != nil {
		return err
	}
	p.Affinity = cleanAffinity(affinity)
	return Save(*p)
}

// groupID is the bare id of a group, from "group/x" or "x".
func groupID(id string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(id), GroupPrefix)))
}

// clean checks a group as the user gave it and returns it as it is stored.
func (g Group) clean() (Group, error) {
	g.ID = groupID(g.ID)
	g.Name = strings.TrimSpace(g.Name)
	if g.ID == "" {
		g.ID = Slug(g.Name)
	}
	if g.ID == "" {
		return g, errors.New("a group needs a name or an id")
	}
	if g.ID != Slug(g.ID) {
		return g, fmt.Errorf("group id must be lowercase letters, digits and dashes, not %q", g.ID)
	}
	if g.Name == "" {
		g.Name = g.ID
	}
	g.Routing = cleanRouting(g.Routing)
	g.Affinity = cleanAffinity(g.Affinity)
	members, err := cleanMembers(g.Members)
	if err != nil {
		return g, err
	}
	if len(members) == 0 {
		return g, errors.New("组里至少要有一个模型")
	}
	g.Members = members
	return g, nil
}

// cleanMembers trims the members, drops a group named among them (groups do
// not nest), and drops repeats. A member is "provider/model".
func cleanMembers(ms []string) ([]string, error) {
	var out []string
	for _, m := range ms {
		m = strings.TrimSpace(m)
		if m == "" || strings.HasPrefix(m, GroupPrefix) {
			continue
		}
		pid, model, ok := strings.Cut(m, "/")
		pid, model = strings.ToLower(strings.TrimSpace(pid)), strings.TrimSpace(model)
		if !ok || pid == "" || model == "" {
			return nil, fmt.Errorf("member %q must be written provider/model", m)
		}
		if pid == "group" {
			continue
		}
		if id := pid + "/" + model; !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

func cleanRouting(s string) string {
	switch s {
	case Ordered, Rotate, GroupLeastUsed:
		return s
	}
	return ""
}

func cleanAffinity(s string) string {
	switch s {
	case AffinitySession, AffinityTurn, AffinityOff:
		return s
	}
	return ""
}

// hide records an automatic group as hidden, unless it already is.
func hide(f *file, id string) {
	if !slices.ContainsFunc(f.Groups, func(g Group) bool { return g.ID == id && g.Hidden }) {
		f.Groups = append(f.Groups, Group{ID: id, Hidden: true})
	}
}

// discovered reports whether magpie finds a group of this id right now,
// hidden or not.
func discovered(id string) bool {
	for _, g := range autoGroups() {
		if g.ID == id {
			return true
		}
	}
	return false
}

// listGroups is every group the catalog offers: the user's, then the
// automatic ones the user has not hidden or replaced.
func listGroups() []Group {
	f := load()
	var out []Group
	taken := map[string]bool{}
	for _, g := range f.Groups {
		taken[g.ID] = true
		if !g.Hidden {
			out = append(out, g)
		}
	}
	for _, g := range autoGroups() {
		if !taken[g.ID] {
			out = append(out, g)
		}
	}
	return out
}

// resolveMembers turns member ids into the ones whose provider is ready. A
// member listed twice, or a provider's model listed twice, counts once.
func resolveMembers(ids []string) []Member {
	all := All()
	var out []Member
	seen := map[string]bool{}
	for _, s := range ids {
		pid, model, ok := strings.Cut(s, "/")
		pid, model = strings.ToLower(strings.TrimSpace(pid)), strings.TrimSpace(model)
		if !ok || pid == "" || model == "" || pid == "group" || seen[pid+"/"+model] {
			continue
		}
		seen[pid+"/"+model] = true
		p, ok := find(all, pid)
		if !ok || !p.Ready() {
			continue
		}
		out = append(out, Member{Provider: p, Model: model})
	}
	return out
}

// autoGroups finds the models more than one ready provider serves under the
// same name (NormalizeModelKey). Each becomes a group "auto-<name>" holding
// every provider's copy, in the order the providers were added, and named
// after the first copy that has a proper display name.
func autoGroups() []Group {
	type bucket struct {
		members []Member
		name    string
		vendors map[string]bool
	}
	var order []string
	byKey := map[string]*bucket{}
	for _, p := range All() {
		if !p.Ready() {
			continue
		}
		for _, m := range p.Available() {
			key := NormalizeModelKey(m.ID)
			if key == "" {
				continue
			}
			b := byKey[key]
			if b == nil {
				b = &bucket{vendors: map[string]bool{}}
				byKey[key] = b
				order = append(order, key)
			}
			if !slices.ContainsFunc(b.members, func(x Member) bool { return x.Provider.ID == p.ID && x.Model == m.ID }) {
				b.members = append(b.members, Member{Provider: p, Model: m.ID})
			}
			b.vendors[p.ID] = true
			if b.name == "" && m.Name != "" && m.Name != m.ID {
				b.name = m.Name
			}
		}
	}
	var out []Group
	ids := map[string]bool{}
	for _, key := range order {
		b := byKey[key]
		id := autoPrefix + Slug(key)
		if len(b.vendors) < 2 || Slug(key) == "" || ids[id] {
			continue
		}
		ids[id] = true
		name := b.name
		if name == "" {
			name = b.members[0].Model
		}
		g := Group{ID: id, Name: name, Auto: true}
		for _, m := range b.members {
			g.Members = append(g.Members, m.ID())
		}
		out = append(out, g)
	}
	return out
}

// groupEntries is the catalog's view of the groups: the ones with a member
// ready, each offering only the reasoning levels every member supports.
func groupEntries() []Entry {
	var out []Entry
	for _, g := range listGroups() {
		ms := resolveMembers(g.Members)
		if len(ms) == 0 {
			continue
		}
		out = append(out, Entry{ID: GroupPrefix + g.ID, Model: GroupPrefix + g.ID, Name: g.Name,
			Efforts: commonEfforts(ms), Provider: groupProvider, Group: g.ID})
	}
	return out
}

// commonEfforts is the reasoning levels every member supports, in the first
// member's order.
func commonEfforts(ms []Member) []string {
	if len(ms) == 0 {
		return nil
	}
	out := slices.Clone(memberEfforts(ms[0]))
	for _, m := range ms[1:] {
		have := memberEfforts(m)
		out = slices.DeleteFunc(out, func(x string) bool { return !slices.Contains(have, x) })
	}
	return out
}

func memberEfforts(m Member) []string {
	for _, cm := range m.Provider.Available() {
		if cm.ID == m.Model {
			return cm.Efforts
		}
	}
	return nil
}
