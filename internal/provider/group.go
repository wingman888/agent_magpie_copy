package provider

// A routing group is one or more models, from one or several providers,
// that an agent picks as if it were a single model: requests for group/<id>
// are spread over the keys and accounts of all its members (see gateway).
//
// Groups the user makes are stored in providers.json. magpie also finds
// groups by itself: a model that two or more ready providers serve under
// the same name becomes an auto group. Those are not stored; they are
// worked out from the providers each time. Deleting one only hides it
// (a stored entry with Hidden set), and ShowGroup brings it back.

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// groupPrefix starts a routing group's model id: group/<id>.
const groupPrefix = "group"

// Group is a routing group.
type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Members []string `json:"members,omitempty"` // provider/model, in order

	// Routing and Affinity mean what they do on a provider: "" smart, or
	// order, rotate, least-used; affinity "", session, turn or off.
	Routing  string `json:"routing,omitempty"`
	Affinity string `json:"affinity,omitempty"`

	// Auto marks a group magpie found on its own. Stored, it marks an auto
	// group the user hid (with Hidden).
	Auto   bool `json:"auto,omitempty"`
	Hidden bool `json:"hidden,omitempty"`
}

// Member is one of a group's members, resolved to a ready provider.
type Member struct {
	Provider Provider
	Model    string
	Keys     []KeyAccount // the keys in use; none for a signed-in account
}

// normalizeAffinity keeps a known affinity and turns anything else into
// the default, automatic ("").
func normalizeAffinity(a string) string {
	switch a {
	case "session", "turn", "off":
		return a
	}
	return ""
}

// groupID is the id of a group named with or without its group/ prefix.
func groupID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	return strings.TrimPrefix(id, groupPrefix+"/")
}

// SetAffinity sets how a provider keeps a conversation on the key or
// account that answered it last; "" is automatic.
func SetAffinity(providerID, affinity string) error {
	p, err := Find(providerID)
	if err != nil {
		return err
	}
	p.Affinity = normalizeAffinity(affinity)
	return Save(*p)
}

// SaveGroup adds a routing group, or replaces the one with its id. A group
// the magpie found on its own becomes the user's by being saved.
func SaveGroup(g Group) error {
	g.ID = groupID(g.ID)
	g.Name = strings.TrimSpace(g.Name)
	if g.ID == "" {
		g.ID = Slug(g.Name)
	}
	if g.ID == "" || g.ID != Slug(g.ID) {
		return fmt.Errorf("group id must be lowercase letters, digits and dashes, not %q", g.ID)
	}
	if g.Name == "" {
		g.Name = g.ID
	}
	var members []string
	for _, m := range g.Members {
		m = strings.TrimSpace(m)
		if strings.HasPrefix(strings.ToLower(m), groupPrefix+"/") {
			continue // a group is not a member of a group
		}
		pid, model, ok := strings.Cut(m, "/")
		pid, model = strings.ToLower(strings.TrimSpace(pid)), strings.TrimSpace(model)
		if !ok || pid == "" || model == "" {
			continue // members are provider/model
		}
		if s := pid + "/" + model; !slices.Contains(members, s) {
			members = append(members, s)
		}
	}
	if len(members) == 0 {
		return errors.New("组里至少要有一个模型")
	}
	ng := Group{ID: g.ID, Name: g.Name, Members: members, Routing: normalizeRouting(g.Routing), Affinity: normalizeAffinity(g.Affinity)}
	f := load()
	for i := range f.Groups {
		if f.Groups[i].ID == g.ID {
			f.Groups[i] = ng
			return store(f)
		}
	}
	f.Groups = append(f.Groups, ng)
	return store(f)
}

// DeleteGroup removes a group the user made. An auto group is hidden
// instead, so it stays away until ShowGroup.
func DeleteGroup(id string) error {
	id = groupID(id)
	ps := All()
	f := load()
	i := slices.IndexFunc(f.Groups, func(g Group) bool { return g.ID == id })
	if i >= 0 && f.Groups[i].Hidden {
		return fmt.Errorf("no routing group %q", id)
	}
	if i >= 0 {
		f.Groups = slices.Delete(f.Groups, i, i+1)
	}
	if slices.ContainsFunc(autoGroups(ps), func(g Group) bool { return g.ID == id }) {
		// hidden, not removed: the providers would find it again
		f.Groups = append(f.Groups, Group{ID: id, Auto: true, Hidden: true})
	} else if i < 0 {
		return fmt.Errorf("no routing group %q", id)
	}
	return store(f)
}

// ShowGroup brings back an auto group the user had hidden.
func ShowGroup(id string) error {
	id = groupID(id)
	f := load()
	keep := f.Groups[:0]
	for _, g := range f.Groups {
		if g.ID == id && g.Hidden {
			continue
		}
		keep = append(keep, g)
	}
	if len(keep) == len(f.Groups) {
		return nil
	}
	f.Groups = keep
	return store(f)
}

// Groups lists the routing groups in use: the user's, then the auto ones
// not hidden and not replaced by one of the user's.
func Groups() []Group { return visibleGroups(All()) }

// FindGroup looks a routing group up by id, with or without its group/
// prefix. Its members are the ones that are ready now; the group is found
// even when none of them is, and the caller answers that no model is ready.
func FindGroup(id string) (Group, []Member, bool) {
	ps := All()
	id = groupID(id)
	for _, g := range visibleGroups(ps) {
		if g.ID == id {
			return g, resolveMembers(ps, g.Members), true
		}
	}
	return Group{}, nil, false
}

// visibleGroups is the groups in use, given the providers in use.
func visibleGroups(ps []Provider) []Group {
	stored := load().Groups
	var out []Group
	taken := map[string]bool{}
	for _, g := range stored {
		taken[g.ID] = true
		if !g.Hidden && g.ID != "" {
			out = append(out, g)
		}
	}
	for _, a := range autoGroups(ps) {
		if !taken[a.ID] {
			out = append(out, a)
		}
	}
	return out
}

// resolveMembers resolves members to the ones that are ready, in order.
// A model that appears twice on one provider, under the same name, counts
// once.
func resolveMembers(ps []Provider, members []string) []Member {
	var out []Member
	seen := map[string]bool{}
	for _, m := range members {
		pid, model, ok := strings.Cut(m, "/")
		if !ok {
			continue
		}
		k := pid + "\x00" + modelKey(model)
		if seen[k] {
			continue
		}
		seen[k] = true
		p, found := find(ps, pid)
		if !found || !p.Ready() {
			continue
		}
		out = append(out, Member{Provider: p, Model: model, Keys: p.KeysOn()})
	}
	return out
}

// groupEntries are the catalog entries of the groups with a ready member.
// A group offers only the reasoning levels every member has.
func groupEntries(ps []Provider) []Entry {
	var out []Entry
	for _, g := range visibleGroups(ps) {
		ms := resolveMembers(ps, g.Members)
		if len(ms) == 0 {
			continue
		}
		var efforts []string
		for i, m := range ms {
			es := modelEfforts(m.Provider, m.Model)
			if i == 0 {
				efforts = slices.Clone(es)
				continue
			}
			efforts = slices.DeleteFunc(efforts, func(e string) bool { return !slices.Contains(es, e) })
		}
		name := g.Name
		if name == "" {
			name = g.ID
		}
		gg := g
		out = append(out, Entry{ID: groupPrefix + "/" + g.ID, Model: groupPrefix + "/" + g.ID, Name: name, Efforts: efforts, Group: &gg})
	}
	return out
}

// modelEfforts are the reasoning levels a provider's model offers.
func modelEfforts(p Provider, model string) []string {
	for _, m := range p.Available() {
		if m.ID == model {
			return m.Efforts
		}
	}
	return nil
}

// ---- the same model, under any spelling ---------------------------------------

var (
	versionDot = regexp.MustCompile(`(\d)\.(\d)`)
	dateSuffix = regexp.MustCompile(`-\d{8}$`)
)

// modelKey is the form under which two spellings of one model are the same:
// lowercase; the vendor prefix dropped ("anthropic/…"); dots between version
// numbers read as hyphens ("5.5" is "5-5"); an 8-digit date suffix dropped.
// A variant after a colon (":batch") is a model of its own and kept as it
// is; so is any other dot, as in "v1.beta".
func modelKey(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	base, variant, hasVariant := strings.Cut(m, ":")
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
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

// autoAcc gathers the providers that serve one model.
type autoAcc struct {
	provs   map[string]bool
	members []string
	name    string // the first display name that is not just the id
	first   string // the first id, for a name when none is a display name
}

// autoGroups are the groups magpie finds on its own: a model two or more
// ready providers serve, under the same key (see modelKey). Members follow
// the providers' order; the name is the first real display name, or else
// the first id.
func autoGroups(ps []Provider) []Group {
	var keys []string
	by := map[string]*autoAcc{}
	for _, p := range ps {
		if !p.Ready() {
			continue
		}
		for _, m := range p.Exposed() {
			k := modelKey(m.ID)
			if k == "" {
				continue
			}
			a := by[k]
			if a == nil {
				a = &autoAcc{provs: map[string]bool{}}
				by[k] = a
				keys = append(keys, k)
			}
			if a.provs[p.ID] {
				continue
			}
			a.provs[p.ID] = true
			a.members = append(a.members, p.ID+"/"+m.ID)
			if a.name == "" && m.Name != "" && m.Name != m.ID {
				a.name = m.Name
			}
			if a.first == "" {
				a.first = m.ID
			}
		}
	}
	var out []Group
	ids := map[string]bool{}
	for _, k := range keys {
		a := by[k]
		slug := Slug(k)
		if len(a.provs) < 2 || slug == "" {
			continue
		}
		id := "auto-" + slug
		if ids[id] {
			continue
		}
		ids[id] = true
		name := a.name
		if name == "" {
			name = a.first
		}
		out = append(out, Group{ID: id, Name: name, Members: a.members, Auto: true})
	}
	return out
}
