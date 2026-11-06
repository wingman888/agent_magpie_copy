package provider

// Routing groups: several models, from one provider or several, that an
// agent picks as one. An agent sends "group/<id>" and the gateway spreads
// the request over the keys and accounts of the members (see the gateway's
// routing). The user's groups are kept in providers.json; the automatic
// ones are worked out from the ready providers each time, and only a
// change the user makes to one (or hiding it) is stored.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Group is a set of models an agent uses as one.
type Group struct {
	ID       string   `json:"id"`                 // lowercase letters, digits and dashes
	Name     string   `json:"name"`               // what the pickers show
	Members  []string `json:"members"`            // "provider/model", in order
	Routing  string   `json:"routing,omitempty"`  // "", "order", "rotate" or "least-used"
	Affinity string   `json:"affinity,omitempty"` // "", "session", "turn" or "off"

	// Auto is set on a group worked out from the providers; never stored.
	Auto bool `json:"-"`
}

// Member is one usable member of a group: a model of a ready provider.
type Member struct {
	ProviderID string   // the provider's id
	Model      string   // the model's id at the vendor
	Name       string   // its display name, when the provider has one
	Efforts    []string // the reasoning levels it takes, when known
	Provider   Provider // the provider it belongs to
}

// groupPrefix is how an agent names a group: group/<id>.
const groupPrefix = "group/"

func validAffinity(a string) bool {
	return a == "" || a == "session" || a == "turn" || a == "off"
}

// cleanGroupRouting keeps a routing a group can have; anything else is smart.
func cleanGroupRouting(r string) string {
	switch r {
	case "order", "rotate", "least-used":
		return r
	}
	return ""
}

// groupID is a group's id, given as group/<id> or as <id>.
func groupID(id string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), groupPrefix)
}

// splitMember cuts "provider/model" at its first slash: a provider id has
// none, a model's may ("b/vendor/m").
func splitMember(ref string) (string, string, bool) {
	pid, model, ok := strings.Cut(strings.TrimSpace(ref), "/")
	pid = strings.ToLower(strings.TrimSpace(pid))
	model = strings.TrimSpace(model)
	return pid, model, ok && pid != "" && model != ""
}

// cleanMembers keeps the entries that name a provider's model, each once. A
// group is not a member of a group: an entry pointing at one is dropped.
func cleanMembers(refs []string) []string {
	var out []string
	for _, ref := range refs {
		pid, model, ok := splitMember(ref)
		if !ok || pid == "group" {
			continue
		}
		if m := pid + "/" + model; !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

// SaveGroup adds or replaces a routing group. Saving the id of an automatic
// group makes that group the user's, and un-hides it.
func SaveGroup(g Group) error {
	g.ID = groupID(g.ID)
	if g.ID == "" {
		g.ID = Slug(g.Name)
	}
	if g.ID == "" || g.ID != Slug(g.ID) {
		return fmt.Errorf("group id must be lowercase letters, digits and dashes, not %q", g.ID)
	}
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" {
		g.Name = g.ID
	}
	g.Members = cleanMembers(g.Members)
	if len(g.Members) == 0 {
		return errors.New("组里至少要有一个模型")
	}
	g.Routing = cleanGroupRouting(g.Routing)
	if !validAffinity(g.Affinity) {
		g.Affinity = ""
	}
	g.Auto = false

	f := load()
	f.HiddenGroups = slices.DeleteFunc(f.HiddenGroups, func(h string) bool { return h == g.ID })
	if i := slices.IndexFunc(f.Groups, func(x Group) bool { return x.ID == g.ID }); i >= 0 {
		f.Groups[i] = g
	} else {
		f.Groups = append(f.Groups, g)
	}
	return store(f)
}

// DeleteGroup removes a group. The user's own group is deleted; an automatic
// one is hidden, so ShowGroup brings it back. When both exist, both go.
func DeleteGroup(id string) error {
	id = groupID(id)
	f := load()
	n := len(f.Groups)
	f.Groups = slices.DeleteFunc(f.Groups, func(g Group) bool { return g.ID == id })
	found := len(f.Groups) != n
	if slices.ContainsFunc(autoGroups(All()), func(g Group) bool { return g.ID == id }) {
		found = true
		if !slices.Contains(f.HiddenGroups, id) {
			f.HiddenGroups = append(f.HiddenGroups, id)
		}
	}
	if !found {
		return fmt.Errorf("no group %q", id)
	}
	return store(f)
}

// ShowGroup brings back an automatic group that DeleteGroup hid. A group
// that is not hidden is left as it is.
func ShowGroup(id string) error {
	id = groupID(id)
	f := load()
	if !slices.Contains(f.HiddenGroups, id) {
		return nil
	}
	f.HiddenGroups = slices.DeleteFunc(f.HiddenGroups, func(h string) bool { return h == id })
	return store(f)
}

// FindGroup looks a group up by id, given as group/<id> or <id>, and resolves
// its members that can be used now. A group none of whose models can be used
// is found with no members; the gateway answers that with a 404.
func FindGroup(id string) (Group, []Member, bool) {
	id = groupID(id)
	ps := All()
	for _, g := range allGroups(ps) {
		if g.ID == id {
			return g, resolveMembers(g, ps), true
		}
	}
	return Group{}, nil, false
}

// SetAffinity sets how a provider keeps a conversation on the key or account
// that answered it last: "" automatic, "session", "turn" or "off".
func SetAffinity(providerID, affinity string) error {
	affinity = strings.ToLower(strings.TrimSpace(affinity))
	if !validAffinity(affinity) {
		return fmt.Errorf("affinity must be session, turn, off or empty, not %q", affinity)
	}
	p, err := Find(providerID)
	if err != nil {
		return err
	}
	p.Affinity = affinity
	return Save(*p)
}

// allGroups are the groups in effect, given the providers: the user's first,
// then the automatic ones that are not hidden and not replaced by the user's.
func allGroups(ps []Provider) []Group {
	f := load()
	out := slices.Clone(f.Groups)
	for _, g := range autoGroups(ps) {
		if slices.ContainsFunc(f.Groups, func(u Group) bool { return u.ID == g.ID }) || slices.Contains(f.HiddenGroups, g.ID) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// autoGroups makes a group of every model that two or more ready providers
// serve under the same name (see normalizeModelKey). Its name is the display
// name of the first provider that has one, else the first id seen. Members
// are in the order the providers were added.
func autoGroups(ps []Provider) []Group {
	type bucket struct {
		name    string
		named   bool
		members []string
		provs   []string
	}
	var order []string
	byKey := map[string]*bucket{}
	for _, p := range ps {
		if !p.Ready() {
			continue
		}
		for _, m := range p.Available() {
			key := normalizeModelKey(m.ID)
			if key == "" {
				continue
			}
			b := byKey[key]
			if b == nil {
				b = &bucket{}
				byKey[key] = b
				order = append(order, key)
			}
			if b.name == "" {
				b.name = m.ID
			}
			if !b.named && m.Name != "" && m.Name != m.ID {
				b.name, b.named = m.Name, true
			}
			if ref := p.ID + "/" + m.ID; !slices.Contains(b.members, ref) {
				b.members = append(b.members, ref)
			}
			if !slices.Contains(b.provs, p.ID) {
				b.provs = append(b.provs, p.ID)
			}
		}
	}
	var out []Group
	ids := map[string]bool{}
	for _, key := range order {
		b := byKey[key]
		slug := Slug(key)
		if len(b.provs) < 2 || slug == "" || ids["auto-"+slug] {
			continue
		}
		ids["auto-"+slug] = true
		out = append(out, Group{ID: "auto-" + slug, Name: b.name, Members: b.members, Auto: true})
	}
	return out
}

// resolveMembers is the members of g that can be used now, given the
// providers: those of ready providers, each model once.
func resolveMembers(g Group, ps []Provider) []Member {
	var out []Member
	seen := map[string]bool{}
	for _, ref := range g.Members {
		if strings.HasPrefix(ref, groupPrefix) {
			continue
		}
		pid, model, ok := splitMember(ref)
		if !ok || seen[pid+"/"+model] {
			continue
		}
		p, found := find(ps, pid)
		if !found || !p.Ready() {
			continue
		}
		seen[pid+"/"+model] = true
		m := Member{ProviderID: pid, Model: model, Provider: p}
		for _, av := range p.Available() {
			if av.ID == model {
				m.Name, m.Efforts = av.Name, av.Efforts
				break
			}
		}
		out = append(out, m)
	}
	return out
}

// groupEntries lists the groups for the catalog, after the providers' models.
func groupEntries() []Entry {
	ps := All()
	var out []Entry
	for _, g := range allGroups(ps) {
		out = append(out, groupEntry(g, resolveMembers(g, ps)))
	}
	return out
}

// groupEntry is a group as a catalog entry. It offers only the reasoning
// levels all of its members take.
func groupEntry(g Group, ms []Member) Entry {
	var efforts []string
	for i, m := range ms {
		if i == 0 {
			efforts = slices.Clone(m.Efforts)
			continue
		}
		efforts = slices.DeleteFunc(efforts, func(e string) bool { return !slices.Contains(m.Efforts, e) })
	}
	return Entry{ID: groupPrefix + g.ID, Name: g.Name, Efforts: efforts, Group: g.ID}
}
