package provider

// Routing groups: several models, from one vendor or several, that the agents
// pick as one. A request for group/<id> is spread by the gateway over the
// keys and accounts of all its members (see gateway/routing.go).
//
// A group the user made is stored in providers.json. A group discovered
// from vendors offering the same model under the same name is worked out
// each time from the providers that are ready, and stored only once the user
// changes it or hides it (see visibleGroups).

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// GroupPrefix starts the model id of a routing group: group/<id>.
const GroupPrefix = "group/"

// Group is a routing group as the user sees it.
type Group struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Members  []string `json:"members"` // "provider/model", in order
	Routing  string   `json:"routing,omitempty"`
	Affinity string   `json:"affinity,omitempty"`

	// Auto is set on a group discovered from the providers, not stored.
	Auto bool `json:"-"`
}

// Member is one of a group's models that can be used: its provider is ready.
type Member struct {
	Name       string       // "provider/model"
	ProviderID string       // the provider's id
	Model      string       // the vendor's model id
	Provider   Provider     // the provider, with the keys or accounts it has on
	Keys       []KeyAccount // the keys on, for a provider with keys; nil for an account
}

// GroupID reads the group id out of a model id: "group/mine" is "mine".
func GroupID(model string) (string, bool) {
	id, ok := strings.CutPrefix(strings.TrimSpace(model), GroupPrefix)
	return id, ok && id != ""
}

// groupKey is a group id as typed: trimmed, lower-cased, without the prefix.
func groupKey(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	return strings.TrimPrefix(id, GroupPrefix)
}

// normalizeRouting keeps the routing values a provider or group may have;
// anything else is smart routing, the empty string.
func normalizeRouting(r string) string {
	switch r {
	case Ordered, Rotate, LeastUsed:
		return r
	case "usage": // the name the first versions gave least-used
		return LeastUsed
	}
	return ""
}

// The affinity values: how long a conversation stays on the key or account
// that answered it last. Empty is automatic.
const (
	AffinitySession = "session"
	AffinityTurn    = "turn"
	AffinityOff     = "off"
)

func normalizeAffinity(a string) string {
	switch a {
	case AffinitySession, AffinityTurn, AffinityOff:
		return a
	}
	return ""
}

// SetAffinity says how a provider's conversations stick to the key or
// account that answered them: "" automatic, session, turn, or off.
func SetAffinity(providerID, affinity string) error {
	if a := strings.ToLower(strings.TrimSpace(affinity)); a != normalizeAffinity(a) {
		return fmt.Errorf("affinity must be %q, %q, %q or empty, not %q", AffinitySession, AffinityTurn, AffinityOff, affinity)
	}
	p, err := Find(providerID)
	if err != nil {
		return err
	}
	p.Affinity = normalizeAffinity(strings.ToLower(strings.TrimSpace(affinity)))
	return Save(*p)
}

// SaveGroup adds or replaces a routing group the user made. Saving a
// discovered group makes it the user's.
func SaveGroup(g Group) error {
	g.ID = groupKey(g.ID)
	if g.ID == "" {
		g.ID = Slug(g.Name)
	}
	if g.ID == "" {
		return errors.New("a routing group needs a name or an id")
	}
	if g.ID != Slug(g.ID) {
		return fmt.Errorf("group id must be lowercase letters, digits and dashes, not %q", g.ID)
	}
	if g.Name = strings.TrimSpace(g.Name); g.Name == "" {
		g.Name = g.ID
	}
	var members []string
	for _, m := range g.Members {
		m = strings.TrimSpace(m)
		if m == "" || strings.HasPrefix(m, GroupPrefix) {
			continue // a group is not a member of a group
		}
		pid, model, ok := strings.Cut(m, "/")
		if !ok || strings.TrimSpace(pid) == "" || strings.TrimSpace(model) == "" {
			return fmt.Errorf("member %q is not provider/model", m)
		}
		members = append(members, m)
	}
	g.Members = cleanList(members)
	if len(g.Members) == 0 {
		return errors.New("组里至少要有一个模型")
	}
	g.Routing = normalizeRouting(g.Routing)
	g.Affinity = normalizeAffinity(strings.ToLower(strings.TrimSpace(g.Affinity)))
	g.Auto = false

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

// DeleteGroup removes a group the user made, and hides a discovered one: it
// comes back with ShowGroup.
func DeleteGroup(id string) error {
	id = groupKey(id)
	f := load()
	found := false
	var keep []Group
	for _, g := range f.Groups {
		if g.ID == id {
			found = true
			continue
		}
		keep = append(keep, g)
	}
	f.Groups = keep
	for _, g := range discovered(All()) {
		if g.ID != id || slices.Contains(f.HiddenGroups, id) {
			continue // a hidden group is not there to delete
		}
		found = true
		f.HiddenGroups = append(f.HiddenGroups, id)
	}
	if !found {
		return fmt.Errorf("no routing group %q", id)
	}
	return store(f)
}

// ShowGroup brings back a discovered group the user had hidden.
func ShowGroup(id string) error {
	id = groupKey(id)
	f := load()
	if !slices.Contains(f.HiddenGroups, id) {
		return nil
	}
	f.HiddenGroups = slices.DeleteFunc(f.HiddenGroups, func(x string) bool { return x == id })
	return store(f)
}

// FindGroup looks a group up by its id, with or without "group/", and works
// out which of its members can be used now. The bool says the group exists;
// the members are empty when none of them is ready.
func FindGroup(id string) (Group, []Member, bool) {
	ps := All()
	key := groupKey(id)
	for _, g := range visibleGroups(ps) {
		if g.ID == key {
			return g, resolveMembers(g.Members, ps), true
		}
	}
	return Group{}, nil, false
}

// visibleGroups are the groups in use: the user's, then the discovered ones
// the user has not hidden or replaced.
func visibleGroups(ps []Provider) []Group {
	f := load()
	out := append([]Group(nil), f.Groups...)
	for _, g := range discovered(ps) {
		if slices.Contains(f.HiddenGroups, g.ID) || slices.ContainsFunc(f.Groups, func(u Group) bool { return u.ID == g.ID }) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// resolveMembers turns "provider/model" names into the members that are
// ready, each once.
func resolveMembers(names []string, ps []Provider) []Member {
	var out []Member
	seen := map[string]bool{}
	for _, name := range names {
		pid, model, ok := strings.Cut(name, "/")
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		p, found := find(ps, pid)
		if !found || !p.Ready() {
			continue
		}
		m := Member{Name: name, ProviderID: pid, Model: model, Provider: p}
		if p.Account == nil {
			m.Keys = p.KeysOn()
		}
		out = append(out, m)
	}
	return out
}

// discovered are the groups made of the models that two or more ready
// providers offer under the same normalized name (see NormalizeModelKey),
// in the order the providers were added.
func discovered(ps []Provider) []Group {
	type acc struct {
		name      string // the formal display name, once one is seen
		members   []string
		providers []string
	}
	byKey := map[string]*acc{}
	var keys []string
	for _, p := range ps {
		if !p.Ready() {
			continue
		}
		for _, m := range p.Available() {
			k := NormalizeModelKey(m.ID)
			if k == "" {
				continue
			}
			a := byKey[k]
			if a == nil {
				a = &acc{}
				byKey[k] = a
				keys = append(keys, k)
			}
			if a.name == "" && m.Name != "" && m.Name != m.ID {
				a.name = m.Name
			}
			if name := p.ID + "/" + m.ID; !slices.Contains(a.members, name) {
				a.members = append(a.members, name)
			}
			if !slices.Contains(a.providers, p.ID) {
				a.providers = append(a.providers, p.ID)
			}
		}
	}
	var out []Group
	used := map[string]bool{}
	for _, k := range keys {
		a := byKey[k]
		if len(a.providers) < 2 {
			continue
		}
		id := "auto-" + Slug(k)
		if used[id] {
			id += "-" + keyID(k)[:6]
		}
		used[id] = true
		name := a.name
		if name == "" {
			name = a.members[0][strings.Index(a.members[0], "/")+1:]
		}
		out = append(out, Group{ID: id, Name: name, Members: a.members, Auto: true})
	}
	return out
}

// groupEfforts are the reasoning levels every member supports, in the first
// member's order.
func groupEfforts(ms []Member, avail map[string][]catalog.Model) []string {
	levels := func(m Member) []string {
		for _, x := range avail[m.ProviderID] {
			if x.ID == m.Model {
				return x.Efforts
			}
		}
		return nil
	}
	if len(ms) == 0 {
		return nil
	}
	var out []string
	for _, e := range levels(ms[0]) {
		all := true
		for _, m := range ms[1:] {
			if !slices.Contains(levels(m), e) {
				all = false
				break
			}
		}
		if all {
			out = append(out, e)
		}
	}
	return out
}
