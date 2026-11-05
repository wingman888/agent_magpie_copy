package provider

// Routing groups: several models, from one vendor or several, that an agent
// picks as one model ("group/<id>"). The gateway sends a request for a group
// to whichever of its members is ready.
//
// A group the user made or hid is kept in providers.json. A group magpie
// discovers — the same model offered by two or more ready providers — is not
// kept: it is worked out from the ready providers each time. Only a change
// the user makes to it (saving or hiding it) is written down.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// groupPrefix is the provider-id-like prefix a routing group's model id
// carries: "group/<id>".
const groupPrefix = "group"

// groupProvider stands in for a provider on the catalog entries of groups,
// so code that reads an entry's provider (its owner, its name) still works.
var groupProvider = Provider{ID: groupPrefix, Name: "Routing groups"}

// Group is a routing group.
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Members are "provider/model" references, in the order they are tried.
	Members []string `json:"members,omitempty"`
	// Routing is how requests spread over the members: "" smart, "order",
	// "rotate" or "least-used" (see Provider.Routing).
	Routing string `json:"routing,omitempty"`
	// Affinity is as on Provider.Affinity.
	Affinity string `json:"affinity,omitempty"`

	// Auto marks a group magpie discovered. It is never written down.
	Auto bool `json:"-"`
	// Hidden is set on a record that hides a group (usually a discovered
	// one) from the catalog; such a record has no other fields.
	Hidden bool `json:"hidden,omitempty"`
}

// Member is one ready candidate of a group: a member reference resolved to
// the provider it names, with the model it names there.
type Member struct {
	Ref      string   // "provider/model", as the group lists it
	Provider Provider // the ready provider
	Model    string   // the model id the vendor knows it by
}

// normalizeRouting keeps a known routing and turns anything else into smart.
// "usage" is what least-used was called before; it still means least-used.
func normalizeRouting(s string) string {
	switch s {
	case Ordered, Rotate, LeastUsed:
		return s
	case "usage":
		return LeastUsed
	}
	return ""
}

// normalizeAffinity keeps a known affinity and turns anything else into the
// automatic one.
func normalizeAffinity(s string) string {
	switch s {
	case "session", "turn", "off":
		return s
	}
	return ""
}

// SetAffinity sets how a provider's conversations stick to the key or
// account that answered them last; "" is automatic.
func SetAffinity(providerID, affinity string) error {
	p, err := Find(providerID)
	if err != nil {
		return err
	}
	p.Affinity = normalizeAffinity(affinity)
	return Save(*p)
}

// SaveGroup adds a group, or replaces the user's group with its id. Saving a
// group magpie discovered makes it the user's.
func SaveGroup(g Group) error {
	g.ID = strings.ToLower(strings.TrimSpace(g.ID))
	g.Name = strings.TrimSpace(g.Name)
	if g.ID == "" {
		g.ID = Slug(g.Name)
	}
	if g.Name == "" {
		g.Name = g.ID
	}
	if g.ID == "" || g.ID != Slug(g.ID) {
		return fmt.Errorf("group id must be lowercase letters, digits and dashes, not %q", g.ID)
	}
	var members []string
	for _, ref := range g.Members {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		pid, model, ok := strings.Cut(ref, "/")
		if pid == groupPrefix {
			// a group is not a member of a group: such a member is ignored
			continue
		}
		if !ok || strings.TrimSpace(model) == "" {
			return fmt.Errorf("group member %q is not provider/model", ref)
		}
		members = append(members, ref)
	}
	members = cleanList(members)
	if len(members) == 0 {
		return errors.New("组里至少要有一个模型")
	}
	g.Members = members
	g.Routing = normalizeRouting(g.Routing)
	g.Affinity = normalizeAffinity(g.Affinity)
	g.Auto, g.Hidden = false, false

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

// DeleteGroup removes a group the user made. A group magpie discovered is
// hidden instead, and comes back with ShowGroup.
func DeleteGroup(id string) error {
	id = groupID(id)
	discovered := false
	for _, g := range autoGroups(All()) {
		if g.ID == id {
			discovered = true
			break
		}
	}
	f := load()
	if discovered {
		for i := range f.Groups {
			if f.Groups[i].ID == id {
				f.Groups[i] = Group{ID: id, Hidden: true}
				return store(f)
			}
		}
		f.Groups = append(f.Groups, Group{ID: id, Hidden: true})
		return store(f)
	}
	for i := range f.Groups {
		if f.Groups[i].ID == id && !f.Groups[i].Hidden {
			f.Groups = append(f.Groups[:i], f.Groups[i+1:]...)
			return store(f)
		}
	}
	return fmt.Errorf("no group %q", id)
}

// ShowGroup brings back a group DeleteGroup hid.
func ShowGroup(id string) error {
	id = groupID(id)
	f := load()
	for i := range f.Groups {
		if f.Groups[i].ID == id && f.Groups[i].Hidden {
			f.Groups = append(f.Groups[:i], f.Groups[i+1:]...)
			return store(f)
		}
	}
	return nil
}

// groupID is a group's id, from its id or its "group/<id>" model id.
func groupID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	return strings.TrimPrefix(id, groupPrefix+"/")
}

// FindGroup looks a group up by its id or its "group/<id>" model id. It
// returns the group and its members that are ready now, in the group's
// order; ok is false when the group is unknown or none of its members is
// ready.
func FindGroup(id string) (Group, []Member, bool) {
	id = groupID(id)
	ps := All()
	for _, g := range groupDefs(ps) {
		if g.ID == id {
			ms := expand(g, ps)
			return g, ms, len(ms) > 0
		}
	}
	return Group{}, nil, false
}

// expand resolves a group's member references to the ready providers they
// name. A reference repeated, or naming a group, counts once or not at all.
func expand(g Group, ps []Provider) []Member {
	var out []Member
	seen := map[string]bool{}
	for _, ref := range g.Members {
		pid, model, ok := strings.Cut(ref, "/")
		if !ok || model == "" || pid == groupPrefix || seen[ref] {
			continue
		}
		seen[ref] = true
		for _, p := range ps {
			if p.ID == pid && p.Ready() {
				out = append(out, Member{Ref: ref, Provider: p, Model: model})
				break
			}
		}
	}
	return out
}

// groupDefs is every group that applies now: the user's, then the ones
// discovered that the user has neither replaced nor hidden.
func groupDefs(ps []Provider) []Group {
	f := load()
	hidden := map[string]bool{}
	taken := map[string]bool{}
	var out []Group
	for _, g := range f.Groups {
		if g.Hidden {
			hidden[g.ID] = true
			continue
		}
		taken[g.ID] = true
		out = append(out, g)
	}
	for _, g := range autoGroups(ps) {
		if hidden[g.ID] || taken[g.ID] {
			continue
		}
		out = append(out, g)
	}
	return out
}

// autoGroups makes a group for every model that two or more ready providers
// offer under the same canonical name (see CanonicalModel). Members follow
// the providers' order; the name is the first display name a member has.
func autoGroups(ps []Provider) []Group {
	type bucket struct {
		g    Group
		seen map[string]bool
	}
	var order []string
	byKey := map[string]*bucket{}
	for _, p := range ps {
		if !p.Ready() {
			continue
		}
		for _, m := range p.Exposed() {
			key := CanonicalModel(m.ID)
			if key == "" {
				continue
			}
			b := byKey[key]
			if b == nil {
				b = &bucket{seen: map[string]bool{}}
				byKey[key] = b
				order = append(order, key)
			}
			if b.seen[p.ID] {
				continue
			}
			b.seen[p.ID] = true
			b.g.Members = append(b.g.Members, p.ID+"/"+m.ID)
			if b.g.Name == "" && displayName(m) {
				b.g.Name = m.Name
			}
		}
	}
	var out []Group
	for _, key := range order {
		b := byKey[key]
		if len(b.seen) < 2 {
			continue
		}
		g := b.g
		g.ID = autoID(key)
		if g.Name == "" {
			g.Name = key
		}
		g.Auto = true
		out = append(out, g)
	}
	return out
}

// displayName is whether a model's name is more than its id.
func displayName(m catalog.Model) bool {
	return strings.TrimSpace(m.Name) != "" && m.Name != m.ID
}

// autoID is the id of the group discovered for a canonical model name:
// "auto-" and the name with its dots and other symbols made dashes. A name
// that needs that rewriting gets a fingerprint too, so two names that come
// out alike stay two groups.
func autoID(key string) string {
	slug := Slug(key)
	if slug == key {
		return "auto-" + key
	}
	return "auto-" + slug + "-" + keyID(key)
}

// commonEfforts are the reasoning levels every member supports, in the
// first member's order.
func commonEfforts(ms []Member) []string {
	var out []string
	for i, m := range ms {
		var es []string
		for _, x := range m.Provider.Available() {
			if x.ID == m.Model {
				es = x.Efforts
				break
			}
		}
		if i == 0 {
			out = append(out, es...)
			continue
		}
		var keep []string
		for _, e := range out {
			if contains(es, e) {
				keep = append(keep, e)
			}
		}
		out = keep
	}
	return out
}

// groupEntries are the catalog entries of the groups that have a ready
// member, after the providers' own.
func groupEntries(ps []Provider) []Entry {
	var out []Entry
	for _, g := range groupDefs(ps) {
		ms := expand(g, ps)
		if len(ms) == 0 {
			continue
		}
		out = append(out, Entry{ID: groupPrefix + "/" + g.ID, Model: groupPrefix + "/" + g.ID, Name: g.Name,
			Efforts: commonEfforts(ms), Provider: groupProvider, Group: g.ID})
	}
	return out
}
