package provider

// Routing groups: several models, from one provider or several, that an
// agent picks as one model, "group/<id>". The gateway spreads a request for
// a group over the keys and accounts of all its members (see the gateway).
//
// A group the user made is kept in providers.json. Groups magpie finds on
// its own — one model that two or more ready providers serve under the same
// name — are never stored: they are worked out from the providers each time,
// and only a change the user makes (replacing one, hiding one) is written.

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// groupPrefix is what a group's model id starts with: "group/<id>".
const groupPrefix = "group/"

// Affinity is whether a conversation keeps to the key or account that
// answered it last, so the vendor's prompt cache can hit (see
// Provider.Affinity). The empty one is automatic.
const (
	AffinitySession = "session" // the whole conversation
	AffinityTurn    = "turn"    // within one user turn only
	AffinityOff     = "off"     // never
)

// NormalizeAffinity is the affinity a written value stands for; "" (automatic)
// for anything else.
func NormalizeAffinity(s string) string {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case AffinitySession, AffinityTurn, AffinityOff:
		return s
	}
	return ""
}

// Group is a set of models an agent picks as one.
type Group struct {
	ID       string   `json:"id"`                 // lowercase letters, digits, dashes
	Name     string   `json:"name,omitempty"`     // what the picker shows; the id when empty
	Members  []string `json:"members"`            // "provider/model", in order
	Routing  string   `json:"routing,omitempty"`  // as Provider.Routing: "" smart, order, rotate, least-used
	Affinity string   `json:"affinity,omitempty"` // as Provider.Affinity
	// Auto is set on a group magpie found itself. Groups in providers.json
	// are the user's, so Auto is false there.
	Auto bool `json:"auto,omitempty"`
}

// Member is one usable model of a group: its provider, and the model there.
type Member struct {
	Provider string   `json:"provider"` // the provider's id
	Model    string   `json:"model"`    // the model's id at that provider
	Name     string   `json:"name,omitempty"`
	Efforts  []string `json:"efforts,omitempty"`
	// Info is the provider itself, as All lists it, with its keys and accounts.
	Info Provider `json:"-"`
}

// groupID is the id a group id or "group/<id>" stands for.
func groupID(s string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), groupPrefix)
}

// isGroupID reports whether a model id names a group.
func isGroupID(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), groupPrefix)
}

// cleanMembers keeps the usable member references, in order and once each:
// "provider/model" with both parts; a group inside a group is left out.
func cleanMembers(xs []string) []string {
	var out []string
	for _, x := range xs {
		pid, model, ok := splitMember(x)
		if !ok {
			continue
		}
		ref := pid + "/" + model
		if !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	return out
}

// splitMember cuts "provider/model" at its first slash, so a model may have
// slashes of its own ("openrouter/anthropic/claude-opus-5.5").
func splitMember(ref string) (provider, model string, ok bool) {
	ref = strings.TrimSpace(ref)
	if isGroupID(ref) {
		return "", "", false
	}
	pid, model, found := strings.Cut(ref, "/")
	pid, model = strings.ToLower(strings.TrimSpace(pid)), strings.TrimSpace(model)
	if !found || pid == "" || model == "" {
		return "", "", false
	}
	return pid, model, true
}

// SaveGroup adds a group, or replaces the one with its id. A group found
// automatically is made the user's by saving it.
func SaveGroup(g Group) error {
	g.ID = strings.ToLower(strings.TrimSpace(g.ID))
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
	g.Routing = NormalizeRouting(migrateRouting(g.Routing))
	g.Affinity = NormalizeAffinity(g.Affinity)
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

// DeleteGroup removes a group the user made, or hides one magpie found
// (ShowGroup brings it back).
func DeleteGroup(id string) error {
	id = groupID(id)
	f := load()
	for i := range f.Groups {
		if f.Groups[i].ID == id {
			f.Groups = slices.Delete(f.Groups, i, i+1)
			return store(f)
		}
	}
	if slices.ContainsFunc(autoGroups(All()), func(g Group) bool { return g.ID == id }) {
		if !slices.Contains(f.HiddenGroups, id) {
			f.HiddenGroups = append(f.HiddenGroups, id)
		}
		return store(f)
	}
	return fmt.Errorf("no group %q", id)
}

// ShowGroup brings back a group magpie found and the user hid.
func ShowGroup(id string) error {
	id = groupID(id)
	f := load()
	if i := slices.Index(f.HiddenGroups, id); i >= 0 {
		f.HiddenGroups = slices.Delete(f.HiddenGroups, i, i+1)
		return store(f)
	}
	if slices.ContainsFunc(groupsIn(f, All()), func(g Group) bool { return g.ID == id }) {
		return nil
	}
	return fmt.Errorf("no hidden group %q", id)
}

// SetAffinity sets how a provider's conversations keep to one key or
// account: "" automatic, session, turn or off.
func SetAffinity(providerID, affinity string) error {
	p, err := Find(providerID)
	if err != nil {
		return err
	}
	p.Affinity = NormalizeAffinity(affinity)
	return Save(*p)
}

// FindGroup looks a group up by its id, or by "group/<id>", among the ones
// the user made and the ones found automatically. The members are the usable
// ones: a ready provider serving the model. ok is true for a group that exists
// even when none of its members is usable now.
func FindGroup(id string) (Group, []Member, bool) {
	all := All()
	want := groupID(id)
	for _, g := range groupsIn(load(), all) {
		if g.ID == want {
			return g, membersOf(g, all), true
		}
	}
	return Group{}, nil, false
}

// ResolveGroup is the catalog entry of a group, "group/<id>", with its usable
// members. False when there is no such group or none of its members is usable.
func ResolveGroup(id string) (Entry, bool) {
	if !isGroupID(id) {
		return Entry{}, false
	}
	all := All()
	want := groupID(id)
	for _, g := range groupsIn(load(), all) {
		if g.ID == want {
			ms := membersOf(g, all)
			if len(ms) == 0 {
				return Entry{}, false
			}
			return groupEntry(g, ms), true
		}
	}
	return Entry{}, false
}

// groupsIn is every group in force: the user's, then the automatic ones that
// are not hidden and not taken by a group of the user's with the same id.
func groupsIn(f file, all []Provider) []Group {
	var out []Group
	taken := map[string]bool{}
	for _, g := range f.Groups {
		g.Auto = false
		out = append(out, g)
		taken[g.ID] = true
	}
	for _, g := range autoGroups(all) {
		if taken[g.ID] || slices.Contains(f.HiddenGroups, g.ID) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// autoGroups works out the groups magpie finds: a model that two or more
// ready providers offer, under names that are the same model once written
// alike (see canonicalModel). Groups are in the order their models first
// appear, members in the order the providers were added.
func autoGroups(all []Provider) []Group {
	refs := map[string][]modelRef{}
	seenProv := map[string][]string{}
	var order []string
	for _, p := range all {
		if !p.Ready() {
			continue
		}
		for _, m := range p.Exposed() {
			c := canonicalModel(m.ID)
			if c == "" {
				continue
			}
			if _, ok := refs[c]; !ok {
				order = append(order, c)
			}
			refs[c] = append(refs[c], modelRef{Provider: p.ID, ID: m.ID, Name: m.Name})
			if !slices.Contains(seenProv[c], p.ID) {
				seenProv[c] = append(seenProv[c], p.ID)
			}
		}
	}
	var out []Group
	used := map[string]bool{}
	for _, c := range order {
		if len(seenProv[c]) < 2 {
			continue
		}
		id := autoGroupID(c)
		// Two models can name the same group id ("m:batch", "m-batch"): the
		// second one gets a number, so both stay reachable.
		base := id
		for n := 2; used[id]; n++ {
			id = fmt.Sprintf("%s-%d", base, n)
		}
		used[id] = true
		g := Group{ID: id, Name: groupName(c, refs[c]), Auto: true}
		for _, r := range refs[c] {
			g.Members = append(g.Members, r.Provider+"/"+r.ID)
		}
		out = append(out, g)
	}
	return out
}

// membersOf resolves a group's member references against the providers:
// the ones whose provider is ready and serves the model, each once.
func membersOf(g Group, all []Provider) []Member {
	byID := make(map[string]Provider, len(all))
	for _, p := range all {
		byID[p.ID] = p
	}
	served := map[string]map[string]catalog.Model{}
	var out []Member
	seen := map[string]bool{}
	for _, ref := range g.Members {
		pid, model, ok := splitMember(ref)
		if !ok {
			continue
		}
		key := pid + "/" + model
		if seen[key] {
			continue
		}
		p, ok := byID[pid]
		if !ok || !p.Ready() {
			continue
		}
		models, ok := served[pid]
		if !ok {
			models = servedBy(p)
			served[pid] = models
		}
		m, ok := servedModel(models, model)
		if !ok {
			continue
		}
		seen[key] = true
		out = append(out, Member{Provider: pid, Model: model, Name: m.Name, Efforts: m.Efforts, Info: p})
	}
	return out
}

// servedBy is every model a provider is known to serve: the ones it exposes
// and the ones its catalog lists, by id.
func servedBy(p Provider) map[string]catalog.Model {
	out := map[string]catalog.Model{}
	for _, m := range p.Available() {
		out[m.ID] = m
	}
	for _, m := range p.Exposed() {
		if _, ok := out[m.ID]; !ok {
			out[m.ID] = m
		}
	}
	return out
}

// servedModel reports whether a provider serves model. A provider that lists
// no models at all is trusted to serve what a group names.
func servedModel(models map[string]catalog.Model, model string) (catalog.Model, bool) {
	if m, ok := models[model]; ok {
		return m, true
	}
	if len(models) == 0 {
		return catalog.Model{ID: model}, true
	}
	return catalog.Model{}, false
}

// groupEntry is a group as the catalog lists it. It offers only the
// reasoning levels every member supports.
func groupEntry(g Group, ms []Member) Entry {
	name := g.Name
	if name == "" {
		name = g.ID
	}
	gg := g
	return Entry{
		ID:      groupPrefix + g.ID,
		Name:    name,
		Efforts: sharedEfforts(ms),
		Group:   &gg,
		Members: ms,
	}
}

// sharedEfforts are the reasoning levels every member supports, in the
// order the first member lists them.
func sharedEfforts(ms []Member) []string {
	if len(ms) == 0 {
		return nil
	}
	var out []string
	for _, e := range ms[0].Efforts {
		everyone := true
		for _, m := range ms[1:] {
			if !slices.Contains(m.Efforts, e) {
				everyone = false
				break
			}
		}
		if everyone {
			out = append(out, e)
		}
	}
	return out
}

// groupEntries lists the groups with at least one usable member.
func groupEntries(all []Provider) []Entry {
	var out []Entry
	for _, g := range groupsIn(load(), all) {
		if ms := membersOf(g, all); len(ms) > 0 {
			out = append(out, groupEntry(g, ms))
		}
	}
	return out
}
