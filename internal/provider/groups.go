package provider

// Routing groups: models, from one or more providers, that an agent picks as
// one model, named group/<id>. The groups the user made or changed live in
// providers.json; the ones magpie finds itself (a model two or more ready
// providers offer under one normalized name) are worked out from the ready
// providers each time and never stored.

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/yetone/magpie/internal/catalog"
)

// groupScheme is the provider-like id a group's model id starts with.
const groupScheme = "group"

// GroupPrefix starts the model id of every routing group: group/<id>.
const GroupPrefix = groupScheme + "/"

// Affinity modes (see Provider.Affinity and Group.Affinity).
const (
	AffinityAuto    = ""
	AffinitySession = "session"
	AffinityTurn    = "turn"
	AffinityOff     = "off"
)

// Group is a routing group: the models an agent sees as one.
type Group struct {
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Members  []string `json:"members"`            // "provider/model", in order
	Routing  string   `json:"routing,omitempty"`  // as Provider.Routing: "" smart, Ordered, Rotate or LeastUsed
	Affinity string   `json:"affinity,omitempty"` // as Provider.Affinity
	// Auto marks a group magpie found itself. It is never stored.
	Auto bool `json:"-"`
}

// Member is one usable model of a group.
type Member struct {
	Provider Provider // the provider offering it, ready
	Model    string   // the model id that provider knows it by
	Efforts  []string // the reasoning efforts it takes
}

// Ref is the member as a group names it: provider/model.
func (m Member) Ref() string { return m.Provider.ID + "/" + m.Model }

var groupIDRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// validRouting reports whether r is one of the routings a provider or group
// can have besides the default "".
func validRouting(r string) bool { return r == Ordered || r == Rotate || r == LeastUsed }

// normalizeAffinity keeps a known affinity mode; anything else is automatic.
func normalizeAffinity(a string) string {
	switch a {
	case AffinitySession, AffinityTurn, AffinityOff:
		return a
	}
	return AffinityAuto
}

// SaveGroup adds or replaces one of the user's routing groups. Saving a
// group magpie found itself makes it the user's.
func SaveGroup(g Group) error {
	g.ID = strings.ToLower(strings.TrimSpace(g.ID))
	g.Name = strings.TrimSpace(g.Name)
	if g.ID == "" {
		g.ID = Slug(g.Name)
	}
	if g.ID == "" {
		return errors.New("a routing group needs a name or an id")
	}
	if !groupIDRe.MatchString(g.ID) {
		return fmt.Errorf("group id must be lowercase letters, digits and dashes, not %q", g.ID)
	}
	if g.Name == "" {
		g.Name = g.ID
	}
	g.Members = cleanGroupMembers(g.Members)
	if len(g.Members) == 0 {
		return errors.New("组里至少要有一个模型")
	}
	if !validRouting(g.Routing) {
		g.Routing = ""
	}
	g.Affinity = normalizeAffinity(g.Affinity)
	g.Auto = false

	f := load()
	if i := slices.IndexFunc(f.Groups, func(x Group) bool { return x.ID == g.ID }); i >= 0 {
		f.Groups[i] = g
	} else {
		f.Groups = append(f.Groups, g)
	}
	// saving a group brings back one the user had hidden under that id
	f.HiddenGroups = slices.DeleteFunc(f.HiddenGroups, func(x string) bool { return x == g.ID })
	return store(f)
}

// DeleteGroup removes a group. A group the user made is deleted outright and
// nothing else changes: a discovered group under the same id, if there is
// one, shows again. A discovered group with no user group of that id is
// hidden, so ShowGroup can bring it back.
func DeleteGroup(id string) error {
	id = strings.ToLower(strings.TrimSpace(id))
	f := load()
	if i := slices.IndexFunc(f.Groups, func(g Group) bool { return g.ID == id }); i >= 0 {
		f.Groups = slices.Delete(f.Groups, i, i+1)
		return store(f)
	}
	if slices.Contains(f.HiddenGroups, id) || !slices.ContainsFunc(discover(All()), func(g Group) bool { return g.ID == id }) {
		return fmt.Errorf("no group %q", id)
	}
	f.HiddenGroups = append(f.HiddenGroups, id)
	return store(f)
}

// ShowGroup brings back a group magpie found and the user had hidden.
func ShowGroup(id string) error {
	f := load()
	if !slices.Contains(f.HiddenGroups, id) {
		return nil
	}
	f.HiddenGroups = slices.DeleteFunc(f.HiddenGroups, func(x string) bool { return x == id })
	return store(f)
}

// FindGroup looks a group up by its id, written with or without the group/
// prefix, and resolves its members that can be used now, in the group's
// order. ok is false when there is no such group or none of its members can
// be used.
func FindGroup(id string) (Group, []Member, bool) {
	id = strings.TrimPrefix(strings.TrimSpace(id), GroupPrefix)
	ps := All()
	for _, g := range visibleGroups(ps) {
		if g.ID == id {
			ms := usable(g, ps)
			return g, ms, len(ms) > 0
		}
	}
	return Group{}, nil, false
}

// SetAffinity sets how a provider's conversations stick to the key or
// account that answered them. An unknown mode is automatic.
func SetAffinity(providerID, affinity string) error {
	p, err := Find(providerID)
	if err != nil {
		return err
	}
	p.Affinity = normalizeAffinity(affinity)
	return Save(*p)
}

// visibleGroups lists the groups an agent can be offered: the user's own,
// then those magpie found that the user has neither hidden nor replaced.
func visibleGroups(ps []Provider) []Group {
	f := load()
	out := slices.Clone(f.Groups)
	for _, g := range discover(ps) {
		if slices.ContainsFunc(f.Groups, func(x Group) bool { return x.ID == g.ID }) || slices.Contains(f.HiddenGroups, g.ID) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// usable resolves g's members to the ones that can be used now, in the order
// the group lists them. A member named twice, or a model its provider is
// named for twice, counts once. Nested groups, members whose provider is not
// ready, and members whose provider does not offer the model are left out.
func usable(g Group, ps []Provider) []Member {
	avail := map[string][]catalog.Model{}
	seen := map[string]bool{}
	var out []Member
	for _, ref := range g.Members {
		pid, model, ok := splitRef(ref)
		if !ok || seen[pid+"/"+model] {
			continue
		}
		seen[pid+"/"+model] = true
		p, ok := find(ps, pid)
		if !ok || !p.Ready() {
			continue
		}
		ms, ok := avail[pid]
		if !ok {
			ms = served(p)
			avail[pid] = ms
		}
		m, ok := modelNamed(ms, model)
		if !ok {
			continue
		}
		out = append(out, Member{Provider: p, Model: model, Efforts: m.Efforts})
	}
	return out
}

// splitRef reads a member "provider/model". The model may hold slashes of
// its own, as vendor prefixes do.
func splitRef(ref string) (string, string, bool) {
	pid, model, ok := strings.Cut(strings.TrimSpace(ref), "/")
	pid, model = strings.ToLower(strings.TrimSpace(pid)), strings.TrimSpace(model)
	if !ok || pid == "" || model == "" || pid == groupScheme {
		return "", "", false
	}
	return pid, model, true
}

// cleanGroupMembers drops blanks, repeats and groups named as members: a
// group is never a member of a group.
func cleanGroupMembers(xs []string) []string {
	var out []string
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" || strings.HasPrefix(strings.ToLower(x), GroupPrefix) || slices.Contains(out, x) {
			continue
		}
		out = append(out, x)
	}
	return out
}

// served lists the models a provider offers: what its catalog says, and the
// models the user picked, which magpie takes on trust.
func served(p Provider) []catalog.Model {
	seen := map[string]bool{}
	var out []catalog.Model
	for _, m := range p.Available() {
		if !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	for _, id := range p.Models {
		if !seen[id] {
			seen[id] = true
			out = append(out, catalog.Model{ID: id, Name: id})
		}
	}
	return out
}

func modelNamed(ms []catalog.Model, id string) (catalog.Model, bool) {
	for _, m := range ms {
		if m.ID == id {
			return m, true
		}
	}
	return catalog.Model{}, false
}

// discover finds the models two or more ready providers offer under one
// normalized name (see NormalizeModelKey), and makes a group of each: id
// auto-<name>, members in the order the providers were added.
func discover(ps []Provider) []Group {
	type found struct {
		provs  map[string]bool
		refs   []string
		names  []string // each member's display name from its catalog, "" if none
		models []string
	}
	var order []string
	byKey := map[string]*found{}
	for _, p := range ps {
		if !p.Ready() {
			continue
		}
		for _, m := range served(p) {
			key := NormalizeModelKey(m.ID)
			if key == "" {
				continue
			}
			f := byKey[key]
			if f == nil {
				f = &found{provs: map[string]bool{}}
				byKey[key] = f
				order = append(order, key)
			}
			f.provs[p.ID] = true
			f.refs = append(f.refs, p.ID+"/"+m.ID)
			f.names = append(f.names, m.Name)
			f.models = append(f.models, m.ID)
		}
	}
	var out []Group
	used := map[string]bool{}
	for _, key := range order {
		f := byKey[key]
		slug := Slug(key)
		if len(f.provs) < 2 || slug == "" {
			continue
		}
		id := "auto-" + slug
		for n := 2; used[id]; n++ {
			id = fmt.Sprintf("auto-%s-%d", slug, n)
		}
		used[id] = true
		out = append(out, Group{ID: id, Name: groupName(key, f.names, f.models), Members: f.refs, Auto: true})
	}
	return out
}

// groupName is the name of an discovered group: the first display name a
// member's catalog gives, or else the normalized name spelled out.
func groupName(key string, names, models []string) string {
	for i, n := range names {
		if n != "" && !strings.EqualFold(n, models[i]) {
			return n
		}
	}
	return titleKey(key)
}

// titleKey spells a normalized model name for people: "claude-opus-5-5"
// reads "Claude Opus 5.5".
func titleKey(key string) string {
	var words []string
	parts := strings.Split(key, "-")
	for i := 0; i < len(parts); i++ {
		if parts[i] == "" {
			continue
		}
		if allDigits(parts[i]) {
			nums := []string{parts[i]}
			for i+1 < len(parts) && allDigits(parts[i+1]) {
				i++
				nums = append(nums, parts[i])
			}
			words = append(words, strings.Join(nums, "."))
			continue
		}
		rs := []rune(parts[i])
		rs[0] = unicode.ToUpper(rs[0])
		words = append(words, string(rs))
	}
	return strings.Join(words, " ")
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

// commonEfforts are the reasoning efforts every member takes, in the order
// the first member lists them.
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

// groupEntries are the routing groups as the catalog lists them, after the
// providers' models. A group none of whose members can be used now is left out.
func groupEntries(ps []Provider) []Entry {
	var out []Entry
	for _, g := range visibleGroups(ps) {
		ms := usable(g, ps)
		if len(ms) == 0 {
			continue
		}
		out = append(out, Entry{ID: GroupPrefix + g.ID, Model: GroupPrefix + g.ID, Name: g.Name, Efforts: commonEfforts(ms), Group: true})
	}
	return out
}
