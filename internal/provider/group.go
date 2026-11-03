package provider

// Routing groups: several models, from one provider or several, that an
// agent picks as one. The gateway spreads a request for a group over the
// keys and accounts of all its members. A group the user wrote is stored in
// providers.json; a group magpie found by itself (the same model offered by
// two or more ready providers, see AutoGroups) is never stored, only hidden
// when the user removes it.

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// groupWord is the provider-like word that prefixes a group's model id.
const groupWord = "group"

// GroupPrefix starts the model id of every routing group: "group/<id>".
const GroupPrefix = groupWord + "/"

// Group is a routing group as the user wrote it, or as magpie built it.
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Members are "provider/model" strings, in the order they are tried.
	Members  []string `json:"members"`
	Routing  string   `json:"routing,omitempty"`  // "", "order", "rotate" or "least-used"
	Affinity string   `json:"affinity,omitempty"` // as Provider.Affinity

	// Auto is set on a group magpie built from the ready providers. It is
	// derived, never stored.
	Auto bool `json:"-"`
}

// Member is one model of a group, resolved to the ready provider serving it.
type Member struct {
	Provider string   // provider id
	Model    string   // the model id that provider knows
	Target   Provider // the provider itself
}

var groupIDRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// groupID is the id a group reference names: "group/<id>" or the bare id.
func groupID(ref string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ref)), GroupPrefix)
}

// parseMember splits "provider/model" at the first slash, so a model may
// carry slashes of its own ("b/vendor/m").
func parseMember(s string) (pid, model string, ok bool) {
	pid, model, ok = strings.Cut(strings.TrimSpace(s), "/")
	pid, model = strings.ToLower(strings.TrimSpace(pid)), strings.TrimSpace(model)
	return pid, model, ok && pid != "" && model != ""
}

// SaveGroup adds or replaces a user's group. An empty id is made from the
// name, and an empty name is the id. Members that are groups are ignored,
// and a member listed twice counts once. Saving a group under the id of an
// automatic group takes that group over: from then on the user's version
// is the one used.
func SaveGroup(g Group) error {
	id := strings.TrimSpace(g.ID)
	if id == "" {
		id = Slug(g.Name)
	}
	if id == "" {
		return errors.New("a group needs an id or a name")
	}
	all := All()
	// An automatic group's id may carry dots or colons (auto-v1.beta,
	// auto-claude-opus-5-5:batch), which a user's own id may not. Taking one
	// over by its exact id is allowed.
	if !groupIDRe.MatchString(id) && !isAutoGroupID(id, all) {
		return fmt.Errorf("group id must be lowercase letters, digits and dashes, not %q", id)
	}
	name := strings.TrimSpace(g.Name)
	if name == "" {
		name = id
	}

	known := map[string]bool{id: true}
	for _, x := range allGroups(all) {
		known[x.ID] = true
	}
	var members []string
	seen := map[string]bool{}
	for _, raw := range g.Members {
		m := strings.TrimSpace(raw)
		if m == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(m), GroupPrefix) || (!strings.Contains(m, "/") && known[m]) {
			continue // a group inside a group
		}
		pid, model, ok := parseMember(m)
		if !ok {
			return fmt.Errorf("group member %q must be written provider/model", raw)
		}
		key := pid + "/" + model
		if seen[key] {
			continue
		}
		seen[key] = true
		members = append(members, key)
	}
	if len(members) == 0 {
		return errors.New("组里至少要有一个模型")
	}

	f := load()
	ng := Group{ID: id, Name: name, Members: members, Routing: normRouting(g.Routing), Affinity: normAffinity(g.Affinity)}
	replaced := false
	for i := range f.Groups {
		if f.Groups[i].ID == id {
			f.Groups[i] = ng
			replaced = true
		}
	}
	if !replaced {
		f.Groups = append(f.Groups, ng)
	}
	// saving it again brings a removed automatic group back, as the user's own
	f.HiddenGroups = slices.DeleteFunc(f.HiddenGroups, func(h string) bool { return h == id })
	return store(f)
}

// DeleteGroup removes a group. A group the user wrote is dropped; an
// automatic one is hidden, and ShowGroup brings it back. A user group that
// shares an automatic group's id is dropped and its automatic group hidden,
// so the removal sticks.
func DeleteGroup(id string) error {
	id = groupID(id)
	f := load()
	user := slices.IndexFunc(f.Groups, func(g Group) bool { return g.ID == id })
	auto := slices.ContainsFunc(autoGroupList(All()), func(g Group) bool { return g.ID == id })
	if user < 0 && !auto {
		return fmt.Errorf("no group %q", id)
	}
	if user >= 0 {
		f.Groups = slices.Delete(f.Groups, user, user+1)
	}
	if auto && !slices.Contains(f.HiddenGroups, id) {
		f.HiddenGroups = append(f.HiddenGroups, id)
	}
	return store(f)
}

// ShowGroup brings back an automatic group the user removed.
func ShowGroup(id string) error {
	id = groupID(id)
	f := load()
	i := slices.Index(f.HiddenGroups, id)
	if i < 0 {
		// not hidden: fine if it is there to show, an error if it is not
		if slices.ContainsFunc(f.Groups, func(g Group) bool { return g.ID == id }) ||
			slices.ContainsFunc(autoGroupList(All()), func(g Group) bool { return g.ID == id }) {
			return nil
		}
		return fmt.Errorf("no group %q", id)
	}
	f.HiddenGroups = slices.Delete(f.HiddenGroups, i, i+1)
	return store(f)
}

// FindGroup looks a group up by "group/<id>" or its bare id. The members
// come back in the group's order, only those whose provider is ready; a
// group with none gives an empty list. A hidden automatic group is not
// found.
func FindGroup(id string) (Group, []Member, bool) {
	all := All()
	g, ok := findGroup(groupID(id), all)
	if !ok {
		return Group{}, nil, false
	}
	return g, resolveMembers(g, all), true
}

// findGroup is the group with id: a user's group first, then an automatic
// one that is not hidden.
func findGroup(id string, all []Provider) (Group, bool) {
	f := load()
	for _, g := range f.Groups {
		if g.ID == id {
			return g, true
		}
	}
	if slices.Contains(f.HiddenGroups, id) {
		return Group{}, false
	}
	for _, g := range autoGroupList(all) {
		if g.ID == id {
			return g, true
		}
	}
	return Group{}, false
}

// allGroups lists the user's groups in the order they were written, then
// the automatic ones not hidden and not taken over by a user's group.
func allGroups(all []Provider) []Group {
	f := load()
	out := slices.Clone(f.Groups)
	for _, g := range autoGroupList(all) {
		if slices.Contains(f.HiddenGroups, g.ID) || slices.ContainsFunc(f.Groups, func(u Group) bool { return u.ID == g.ID }) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// isAutoGroupID is whether id is the id of an automatic group as magpie
// builds it now.
func isAutoGroupID(id string, all []Provider) bool {
	return slices.ContainsFunc(autoGroupList(all), func(g Group) bool { return g.ID == id })
}

// autoGroupList builds the automatic groups now, from the models each ready
// provider actually serves (Available, not only what is exposed or picked).
// Nothing about them is stored.
func autoGroupList(all []Provider) []Group {
	var src []ProviderModels
	for _, p := range all {
		if !p.Ready() {
			continue
		}
		src = append(src, ProviderModels{Provider: p.ID, Models: p.Available()})
	}
	var out []Group
	for _, a := range AutoGroups(src) {
		g := Group{ID: a.ID, Name: a.Name, Auto: true}
		for _, m := range a.Members {
			g.Members = append(g.Members, m.Provider+"/"+m.Model)
		}
		out = append(out, g)
	}
	return out
}

// resolveMembers turns a group's members into the ready ones, in order. A
// member met twice counts once, and a group inside a group is passed over.
func resolveMembers(g Group, all []Provider) []Member {
	var out []Member
	seen := map[string]bool{}
	for _, ref := range g.Members {
		if strings.HasPrefix(strings.ToLower(ref), GroupPrefix) {
			continue
		}
		pid, model, ok := parseMember(ref)
		if !ok {
			continue
		}
		key := pid + "/" + model
		if seen[key] {
			continue
		}
		seen[key] = true
		i := slices.IndexFunc(all, func(p Provider) bool { return p.ID == pid })
		if i < 0 || !all[i].Ready() {
			continue
		}
		out = append(out, Member{Provider: pid, Model: model, Target: all[i]})
	}
	return out
}

// groupEntries are the catalog entries of the groups that have at least one
// ready member, in allGroups order. A group shows the reasoning efforts all
// its members support.
func groupEntries(all []Provider) []Entry {
	var out []Entry
	for _, g := range allGroups(all) {
		ms := resolveMembers(g, all)
		if len(ms) == 0 {
			continue
		}
		name := g.Name
		if name == "" {
			name = g.ID
		}
		out = append(out, Entry{ID: GroupPrefix + g.ID, Name: name, Efforts: sharedEfforts(ms), Group: g.ID})
	}
	return out
}

// sharedEfforts are the reasoning efforts every member supports, in the
// first member's order.
func sharedEfforts(ms []Member) []string {
	var shared []string
	for i, m := range ms {
		es := modelEfforts(m.Target, m.Model)
		if i == 0 {
			shared = es
			continue
		}
		var keep []string
		for _, e := range shared {
			if slices.Contains(es, e) {
				keep = append(keep, e)
			}
		}
		shared = keep
	}
	return shared
}

// modelEfforts are the reasoning efforts a provider's model takes, from its
// catalog or the user's own list.
func modelEfforts(p Provider, model string) []string {
	for _, m := range p.Available() {
		if m.ID == model {
			return m.Efforts
		}
	}
	for _, m := range p.Exposed() {
		if m.ID == model {
			return m.Efforts
		}
	}
	return nil
}
