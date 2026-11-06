package provider

// Routing groups: several models, from one provider or several, that an
// agent picks as one. A group's members are "provider/model" references in
// order; the gateway routes a request for "group/<id>" over the keys and
// accounts of all of them (see gateway). The user's groups are kept in
// providers.json; the ones magpie finds on its own — a model two or more
// ready providers serve under the same normalized name — are computed each
// time and only the user's changes (a hidden one, an edited one, a deleted
// one) are written down.

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// KindGroup is the Entry.Kind of a routing group in the catalog.
const KindGroup = "group"

// autoPrefix starts the id of a group magpie discovers.
const autoPrefix = "auto-"

// Group is a routing group as the user saved it, or as discovery found it.
type Group struct {
	ID       string   `json:"id"`                 // lowercase letters, digits, dashes
	Name     string   `json:"name,omitempty"`     // what agents see; defaults to the id
	Members  []string `json:"members"`            // "provider/model", in order
	Routing  string   `json:"routing,omitempty"`  // "", "order", "rotate" or "least-used"
	Affinity string   `json:"affinity,omitempty"` // as Provider.Affinity
	// Auto is set on a group discovered from the providers; never stored.
	Auto bool `json:"auto,omitempty"`
}

// Member is one of a group's members that can be used now.
type Member struct {
	ID       string       // "provider/model", as the group lists it
	Provider Provider     // the member's provider, ready
	Model    string       // the model's id at the vendor
	Keys     []KeyAccount // the provider's keys in use, first to last; none for a signed-in account, which is its own
	Efforts  []string     // reasoning levels the model supports, if known
}

var groupIDRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// validGroupID: letters, digits and dashes. An auto group's id carries its
// model's normalized name, which may also hold ":" and "."; those are only
// accepted behind the "auto-" prefix.
func validGroupID(id string) bool {
	if groupIDRe.MatchString(id) {
		return true
	}
	return strings.HasPrefix(id, autoPrefix) && len(id) > len(autoPrefix) && !strings.ContainsAny(id, "/ \t")
}

// groupKey is the id of "group/<id>" or of a bare <id>, as stored.
func groupKey(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	return strings.TrimPrefix(id, GroupPrefix+"/")
}

// memberKey is what makes two members the same: one provider's one model,
// spelled the way the normalized name spells it.
func memberKey(pid, model string) string {
	return pid + "/" + NormalizeModelName(model)
}

// cleanMembers trims the references, drops empty ones, nested groups and
// repeats, and rejects a reference that is not provider/model.
func cleanMembers(xs []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		pid, model, ok := strings.Cut(x, "/")
		pid, model = strings.ToLower(strings.TrimSpace(pid)), strings.TrimSpace(model)
		if !ok || pid == "" || model == "" {
			return nil, fmt.Errorf("group member %q should be written provider/model", x)
		}
		if pid == GroupPrefix {
			continue // a group does not nest in a group
		}
		k := memberKey(pid, model)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, pid+"/"+model)
	}
	return out, nil
}

// readyProviders are the providers that can be used now, in All's order.
func readyProviders() []Provider {
	var out []Provider
	for _, p := range All() {
		if p.Ready() {
			out = append(out, p)
		}
	}
	return out
}

// memberOf resolves one "provider/model" reference against the ready
// providers; false when its provider is not one of them.
func memberOf(ps []Provider, ref string) (Member, bool) {
	pid, model, ok := strings.Cut(strings.TrimSpace(ref), "/")
	pid = strings.ToLower(strings.TrimSpace(pid))
	if !ok || pid == "" || model == "" || pid == GroupPrefix {
		return Member{}, false
	}
	i := slices.IndexFunc(ps, func(p Provider) bool { return p.ID == pid })
	if i < 0 {
		return Member{}, false
	}
	p := ps[i]
	m := Member{ID: p.ID + "/" + model, Provider: p, Model: model, Keys: p.KeysOn()}
	for _, x := range p.Available() {
		if x.ID == model {
			m.Efforts = x.Efforts
			break
		}
	}
	return m, true
}

// membersOf resolves a group's members in order. A member whose provider is
// gone or not ready is left out, and so is one repeating an earlier one.
func membersOf(ps []Provider, g Group) []Member {
	var out []Member
	seen := map[string]bool{}
	for _, ref := range g.Members {
		m, ok := memberOf(ps, ref)
		if !ok {
			continue
		}
		k := memberKey(m.Provider.ID, m.Model)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, m)
	}
	return out
}

// discovered finds the models two or more of the ready providers serve
// under one normalized name, one auto group each. Members follow the order
// the providers were added; the name is the first display name one of them
// has, else the first member's model id.
func discovered(ps []Provider) []Group {
	type acc struct {
		g     Group
		n     int // providers serving it
		named bool
	}
	by := map[string]*acc{}
	var order []string
	for _, p := range ps {
		seen := map[string]bool{}
		for _, m := range p.Available() {
			key := NormalizeModelName(m.ID)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			a := by[key]
			if a == nil {
				a = &acc{g: Group{ID: autoPrefix + key, Name: m.ID, Auto: true}}
				by[key] = a
				order = append(order, key)
			}
			a.g.Members = append(a.g.Members, p.ID+"/"+m.ID)
			a.n++
			if !a.named && m.Name != "" && m.Name != m.ID {
				a.g.Name, a.named = m.Name, true
			}
		}
	}
	var out []Group
	for _, key := range order {
		if a := by[key]; a.n >= 2 {
			out = append(out, a.g)
		}
	}
	return out
}

// groupsNow are the groups as they stand: the user's first, then the
// discovered ones the user has neither replaced nor hidden.
func groupsNow(ps []Provider, f file) []Group {
	out := slices.Clone(f.Groups)
	for _, g := range discovered(ps) {
		if slices.ContainsFunc(f.Groups, func(x Group) bool { return x.ID == g.ID }) || slices.Contains(f.HiddenGroups, g.ID) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// findGroup looks an id up among the groups as they stand.
func findGroup(ps []Provider, f file, id string) (Group, bool) {
	for _, g := range f.Groups {
		if g.ID == id {
			return g, true
		}
	}
	if slices.Contains(f.HiddenGroups, id) {
		return Group{}, false
	}
	for _, g := range discovered(ps) {
		if g.ID == id {
			return g, true
		}
	}
	return Group{}, false
}

// commonEfforts are the reasoning levels every member supports, in the
// first member's order.
func commonEfforts(ms []Member) []string {
	if len(ms) == 0 {
		return nil
	}
	var out []string
	for _, e := range ms[0].Efforts {
		all := true
		for _, m := range ms[1:] {
			if !slices.Contains(m.Efforts, e) {
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

// groupEntries are the catalog entries of the groups with a member that can
// be used now; a group with none is left out.
func groupEntries(ps []Provider) []Entry {
	var out []Entry
	f := load()
	for _, g := range groupsNow(ps, f) {
		ms := membersOf(ps, g)
		if len(ms) == 0 {
			continue
		}
		e := Entry{ID: GroupPrefix + "/" + g.ID, Name: g.Name, Kind: KindGroup, Efforts: commonEfforts(ms), Auto: g.Auto}
		for _, m := range ms {
			e.Members = append(e.Members, m.ID)
		}
		out = append(out, e)
	}
	return out
}

// SaveGroup adds a group, or replaces the user's group with its id. An id
// left out comes from the name; a name left out is the id. Members that are
// groups are ignored, repeats count once. A discovered group saved here
// belongs to the user from then on, and a hidden one is shown again.
func SaveGroup(g Group) error {
	g.ID = strings.ToLower(strings.TrimSpace(g.ID))
	if g.ID == "" {
		g.ID = Slug(g.Name)
	}
	if g.ID == "" {
		return errors.New("a group needs a name or an id")
	}
	if !validGroupID(g.ID) {
		return fmt.Errorf("group id must be lowercase letters, digits and dashes, not %q", g.ID)
	}
	if g.Name = strings.TrimSpace(g.Name); g.Name == "" {
		g.Name = g.ID
	}
	members, err := cleanMembers(g.Members)
	if err != nil {
		return err
	}
	if len(members) == 0 {
		return errors.New("组里至少要有一个模型")
	}
	g.Members = members
	g.Routing = NormalizeRouting(g.Routing)
	g.Affinity = NormalizeAffinity(g.Affinity)
	g.Auto = false

	f := load()
	if i := slices.IndexFunc(f.Groups, func(x Group) bool { return x.ID == g.ID }); i >= 0 {
		f.Groups[i] = g
	} else {
		f.Groups = append(f.Groups, g)
	}
	f.HiddenGroups = slices.DeleteFunc(f.HiddenGroups, func(h string) bool { return h == g.ID })
	return store(f)
}

// DeleteGroup removes a group the user made. A discovered group is hidden
// instead, and ShowGroup brings it back. An id that is neither is an error.
func DeleteGroup(id string) error {
	id = groupKey(id)
	ps := readyProviders()
	f := load()
	i := slices.IndexFunc(f.Groups, func(g Group) bool { return g.ID == id })
	visibleAuto := !slices.Contains(f.HiddenGroups, id) &&
		slices.ContainsFunc(discovered(ps), func(g Group) bool { return g.ID == id })
	if i < 0 && !visibleAuto {
		return fmt.Errorf("no group %q", id)
	}
	if i >= 0 {
		f.Groups = slices.Delete(f.Groups, i, i+1)
	}
	if visibleAuto {
		f.HiddenGroups = append(f.HiddenGroups, id)
	}
	return store(f)
}

// ShowGroup brings back a discovered group DeleteGroup hid. A group that is
// already shown is left as it is; an unknown one is an error.
func ShowGroup(id string) error {
	id = groupKey(id)
	f := load()
	if i := slices.Index(f.HiddenGroups, id); i >= 0 {
		f.HiddenGroups = slices.Delete(f.HiddenGroups, i, i+1)
		return store(f)
	}
	if _, ok := findGroup(readyProviders(), f, id); ok {
		return nil
	}
	return fmt.Errorf("no group %q", id)
}

// FindGroup looks a group up by "group/<id>" or its bare id, and resolves its
// members that can be used now, in order. ok is false when there is no such
// group, or when none of its members can be used: the caller answers 404.
func FindGroup(id string) (Group, []Member, bool) {
	ps := readyProviders()
	g, ok := findGroup(ps, load(), groupKey(id))
	if !ok {
		return Group{}, nil, false
	}
	ms := membersOf(ps, g)
	return g, ms, len(ms) > 0
}

// SetAffinity sets how a provider keeps a conversation on the key or account
// that last answered it. An unknown value is stored as automatic; an unknown
// provider is an error.
func SetAffinity(providerID, affinity string) error {
	p, err := Find(providerID)
	if err != nil {
		return err
	}
	p.Affinity = NormalizeAffinity(affinity)
	return Save(*p)
}
