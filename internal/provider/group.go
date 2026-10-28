package provider

// Routing groups: models from one or more providers handed to an agent as
// one, "group/<id>". The gateway routes a request over every key and account
// of every member. Models that several providers serve are grouped on their
// own; those groups are worked out each time and only written down once the
// user changes or removes one.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// GroupPrefix starts the catalog id of a routing group.
const GroupPrefix = "group/"

// The affinities: how long a conversation stays on the key or account that
// last answered it. "" is automatic.
const (
	AffinitySession = "session"
	AffinityTurn    = "turn"
	AffinityOff     = "off"
)

// Affinities are the values Provider.Affinity and Group.Affinity take.
var Affinities = []string{"", AffinitySession, AffinityTurn, AffinityOff}

// Group is a set of models routed as one.
type Group struct {
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Members  []string `json:"members,omitempty"` // "provider/model", in order
	Routing  string   `json:"routing,omitempty"`
	Affinity string   `json:"affinity,omitempty"`
	Auto     bool     `json:"auto,omitempty"`   // found by magpie, not saved by the user
	Hidden   bool     `json:"hidden,omitempty"` // an automatic group the user removed
}

// Member is a group member that can be used now.
type Member struct {
	ID       string // provider/model
	Provider Provider
	Model    string // the vendor's model id
}

// Groups lists the user's groups, then the ones found automatically. A
// removed automatic group is listed too, marked Hidden; one the user changed
// appears once, as theirs.
func Groups() []Group {
	stored := load().Groups
	var out []Group
	taken := map[string]bool{}
	for _, g := range stored {
		taken[g.ID] = true
		if !g.Hidden {
			g.Auto = false
			out = append(out, g)
		}
	}
	for _, g := range autoGroups() {
		if !taken[g.ID] {
			out = append(out, g)
			continue
		}
		for _, s := range stored {
			if s.ID == g.ID && s.Hidden {
				g.Hidden = true
				out = append(out, g)
			}
		}
	}
	return out
}

// FindGroup looks a group up by its catalog id (group/<id>) with the members
// that can be used now.
func FindGroup(id string) (Group, []Member, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(id), GroupPrefix)
	if !ok {
		return Group{}, nil, false
	}
	for _, g := range Groups() {
		if g.ID != rest {
			continue
		}
		if g.Hidden {
			return Group{}, nil, false
		}
		var ms []Member
		seen := map[string]bool{}
		for _, ref := range g.Members {
			m, ok := member(ref)
			if !ok || seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			ms = append(ms, m)
		}
		return g, ms, true
	}
	return Group{}, nil, false
}

// member resolves "provider/model" when that provider is ready.
func member(ref string) (Member, bool) {
	if strings.HasPrefix(ref, GroupPrefix) {
		return Member{}, false
	}
	pid, model, ok := strings.Cut(strings.TrimSpace(ref), "/")
	if !ok || model == "" {
		return Member{}, false
	}
	p, err := Find(pid)
	if err != nil || p.ID != pid || !p.Ready() {
		return Member{}, false
	}
	return Member{ID: p.ID + "/" + model, Provider: *p, Model: model}, true
}

// groupEntry is the catalog entry of a group with a member ready.
func groupEntry(g Group) (Entry, bool) {
	if g.Hidden {
		return Entry{}, false
	}
	_, ms, ok := FindGroup(GroupPrefix + g.ID)
	if !ok || len(ms) == 0 {
		return Entry{}, false
	}
	e := Entry{ID: GroupPrefix + g.ID, Model: ms[0].Model, Name: g.Name, Provider: ms[0].Provider, Group: g.ID}
	var efforts []string
	for i, m := range ms {
		have := m.Provider.efforts(m.Model)
		if i == 0 {
			efforts = have
			continue
		}
		var both []string
		for _, x := range efforts {
			if contains(have, x) {
				both = append(both, x)
			}
		}
		efforts = both
	}
	e.Efforts = efforts
	seen := map[string]bool{}
	for _, m := range ms {
		if !seen[m.Provider.ID] {
			seen[m.Provider.ID] = true
			e.Icons = append(e.Icons, m.Provider.Icon)
		}
	}
	return e, true
}

// efforts are the reasoning levels the provider lists for a model.
func (p Provider) efforts(model string) []string {
	for _, m := range p.Exposed() {
		if m.ID == model {
			return m.Efforts
		}
	}
	for _, m := range p.Available() {
		if m.ID == model {
			return m.Efforts
		}
	}
	return nil
}

// autoGroups finds the models that more than one ready provider serves.
func autoGroups() []Group {
	type found struct {
		entries   []Entry
		providers map[string]bool
	}
	var order []string
	by := map[string]*found{}
	for _, e := range providerEntries() {
		k := normalizeModel(e.Model)
		if k == "" {
			continue
		}
		f := by[k]
		if f == nil {
			f = &found{providers: map[string]bool{}}
			by[k] = f
			order = append(order, k)
		}
		f.entries = append(f.entries, e)
		f.providers[e.Provider.ID] = true
	}
	var out []Group
	for _, k := range order {
		f := by[k]
		if len(f.providers) < 2 {
			continue
		}
		g := Group{ID: "auto-" + Slug(k), Name: f.entries[0].Name, Auto: true}
		for _, e := range f.entries {
			g.Members = append(g.Members, e.ID)
		}
		if f.entries[0].Name == f.entries[0].Model {
			// the vendor named it by id only; borrow a real name
			for _, e := range f.entries {
				if e.Name != "" && e.Name != e.Model {
					g.Name = e.Name
					break
				}
			}
		}
		out = append(out, g)
	}
	return out
}

var dateRe = regexp.MustCompile(`-20\d{6}$`)

// normalizeModel spells a model the same way whichever vendor lists it:
// lowercase, no vendor prefix, dots between digits as dashes, no trailing
// date snapshot. A ":variant" stays part of the name.
func normalizeModel(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	base, variant, hasVariant := strings.Cut(id, ":")
	base = dateRe.ReplaceAllString(base, "")
	b := []byte(base)
	for i := 1; i+1 < len(b); i++ {
		if b[i] == '.' && b[i-1] >= '0' && b[i-1] <= '9' && b[i+1] >= '0' && b[i+1] <= '9' {
			b[i] = '-'
		}
	}
	base = string(b)
	if hasVariant {
		return base + ":" + variant
	}
	return base
}

// SaveGroup adds a group or replaces the user's group with the same id.
// Saving an automatic group makes it the user's.
func SaveGroup(g Group) error {
	g.ID = strings.ToLower(strings.TrimSpace(g.ID))
	g.Name = strings.TrimSpace(g.Name)
	if g.ID == "" {
		g.ID = Slug(g.Name)
	}
	if g.ID == "" || g.ID != Slug(g.ID) {
		return fmt.Errorf("a group's id must be lowercase letters, digits and dashes, not %q", g.ID)
	}
	if g.Name == "" {
		g.Name = g.ID
	}
	var members []string
	for _, m := range cleanList(g.Members) {
		if !strings.HasPrefix(m, GroupPrefix) {
			members = append(members, m)
		}
	}
	if len(members) == 0 {
		return errors.New("a group needs a model in it")
	}
	g.Members = members
	if g.Routing != Ordered && g.Routing != Rotate && g.Routing != LeastUsed {
		g.Routing = ""
	}
	if !contains(Affinities, g.Affinity) {
		g.Affinity = ""
	}
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

// DeleteGroup removes the user's group. An automatic group is only hidden,
// and ShowGroup brings it back.
func DeleteGroup(id string) error {
	id = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(id), GroupPrefix))
	f := load()
	var keep []Group
	found, hidden := false, false
	for _, g := range f.Groups {
		if g.ID == id {
			if g.Hidden {
				hidden = true
				keep = append(keep, g)
			} else {
				found = true
			}
			continue
		}
		keep = append(keep, g)
	}
	auto := false
	for _, g := range autoGroups() {
		if g.ID == id {
			auto = true
		}
	}
	switch {
	case auto && !hidden:
		keep = append(keep, Group{ID: id, Hidden: true})
	case !found && !(auto && hidden):
		return fmt.Errorf("no group %q", id)
	}
	f.Groups = keep
	return store(f)
}

// ShowGroup brings back an automatic group the user had removed.
func ShowGroup(id string) error {
	id = strings.TrimPrefix(strings.TrimSpace(id), GroupPrefix)
	f := load()
	var keep []Group
	changed := false
	for _, g := range f.Groups {
		if g.ID == id && g.Hidden {
			changed = true
			continue
		}
		keep = append(keep, g)
	}
	if !changed {
		return nil
	}
	f.Groups = keep
	return store(f)
}

// SetAffinity changes how long a provider's conversations stay on the key
// or account that last answered them.
func SetAffinity(id, affinity string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.Affinity = affinity
	return Save(*p)
}
