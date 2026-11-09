package provider

// Routing groups: several models, from one vendor or several, that the
// agents pick as one. A group is "group/<id>" to them; the gateway spreads
// its requests over the keys and accounts of all its members.
//
// The user's groups and the record of each discovered group the user hid
// are kept in providers.json. The discovered groups themselves are never
// stored: they are worked out from the providers that are ready now.

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// Group is a routing group, as the user wrote it or as magpie found it.
type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Members []string `json:"members,omitempty"` // "provider/model", in order
	// Routing and Affinity mean what they mean on a provider (see
	// Provider.Routing and Provider.Affinity); "" is the default.
	Routing  string `json:"routing,omitempty"`
	Affinity string `json:"affinity,omitempty"`
	// Hidden is set on a discovered group the user removed. It only ever
	// names the group; it is what ShowGroup takes back.
	Hidden bool `json:"hidden,omitempty"`

	// Auto is set on a group magpie discovered, never stored.
	Auto bool `json:"-"`
}

// Member is one model a routing group can send a request to: a model of a
// provider that is ready.
type Member struct {
	Provider Provider
	Model    string // the vendor's model id
}

// Affinity values: how a conversation sticks to the key or account that
// answered it last (see Provider.Affinity).
const (
	AffinityAuto    = ""        // stick within a turn, and across turns while the cache holds
	AffinitySession = "session" // the whole conversation
	AffinityTurn    = "turn"    // one user turn, tool results included
	AffinityOff     = "off"     // never
)

// groupPrefix starts a routing group's model id.
const groupPrefix = "group/"

// reservedProviderID is the one provider id a group's prefix takes.
const reservedProviderID = "group"

var groupIDRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// cleanRouting keeps a routing value magpie knows, and "" for anything else.
// "usage" is what least-used was called before; it still means that.
func cleanRouting(s string) string {
	switch s {
	case Ordered, Rotate, LeastUsed:
		return s
	case "usage":
		return LeastUsed
	}
	return ""
}

// cleanAffinity keeps an affinity value magpie knows, and "" for anything else.
func cleanAffinity(s string) string {
	switch s {
	case AffinitySession, AffinityTurn, AffinityOff:
		return s
	}
	return ""
}

// groupRef is a group's id from what the caller wrote: "group/x" or "x".
func groupRef(id string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), groupPrefix)
}

// offer is one model a ready provider serves, as discovery sees it.
type offer struct {
	provider Provider
	model    catalog.Model
}

// offersOf lists the exposed models of the ready providers, in order.
func offersOf(ps []Provider) []offer {
	var out []offer
	for _, p := range ps {
		if !p.Ready() {
			continue
		}
		for _, m := range p.Exposed() {
			out = append(out, offer{provider: p, model: m})
		}
	}
	return out
}

// discoverGroups makes an auto group for each model that two or more
// providers serve under the same model key (see NormalizeModelKey). Members
// keep the providers' order; the name is the first formal display name
// found, else the model id.
func discoverGroups(offers []offer) []Group {
	type bucket struct {
		key    string
		offers []offer
	}
	var buckets []*bucket
	at := map[string]*bucket{}
	for _, o := range offers {
		key := NormalizeModelKey(o.model.ID)
		if key == "" {
			continue
		}
		b := at[key]
		if b == nil {
			b = &bucket{key: key}
			at[key] = b
			buckets = append(buckets, b)
		}
		b.offers = append(b.offers, o)
	}
	var out []Group
	used := map[string]bool{}
	for _, b := range buckets {
		providers := map[string]bool{}
		seen := map[string]bool{}
		var members []string
		name := ""
		for _, o := range b.offers {
			providers[o.provider.ID] = true
			ref := o.provider.ID + "/" + o.model.ID
			if !seen[ref] {
				seen[ref] = true
				members = append(members, ref)
			}
			if name == "" && o.model.Name != "" && !strings.EqualFold(o.model.Name, o.model.ID) {
				name = o.model.Name
			}
		}
		if len(providers) < 2 {
			continue
		}
		slug := Slug(b.key)
		if slug == "" {
			continue
		}
		// two keys that slug alike are two groups; the later one gets a suffix
		id := "auto-" + slug
		for n := 2; used[id]; n++ {
			id = fmt.Sprintf("auto-%s-%d", slug, n)
		}
		used[id] = true
		if name == "" {
			name = b.offers[0].model.ID
		}
		out = append(out, Group{ID: id, Name: name, Members: members, Auto: true})
	}
	return out
}

// listGroups is the routing groups as the agents see them: the user's in
// the order they were saved, then the discovered ones that neither the
// user's groups replace nor a hidden record removes.
func listGroups(ps []Provider, f file) []Group {
	hidden := map[string]bool{}
	owned := map[string]bool{}
	var out []Group
	for _, g := range f.Groups {
		if g.Hidden {
			hidden[g.ID] = true
			continue
		}
		if owned[g.ID] {
			continue
		}
		owned[g.ID] = true
		out = append(out, g)
	}
	for _, g := range discoverGroups(offersOf(ps)) {
		if hidden[g.ID] || owned[g.ID] {
			continue
		}
		out = append(out, g)
	}
	return out
}

// resolveMembers turns a group's member list into the models it can send
// to now: members of providers that are not ready, of unknown providers,
// and nested groups are left out; a model listed twice counts once.
func resolveMembers(refs []string, ps []Provider) []Member {
	var out []Member
	seen := map[string]bool{}
	for _, ref := range refs {
		pid, model, ok := strings.Cut(strings.TrimSpace(ref), "/")
		model = strings.TrimSpace(model)
		if !ok || pid == "" || model == "" || pid == reservedProviderID {
			continue
		}
		p, found := find(ps, pid)
		if !found || !p.Ready() {
			continue
		}
		key := pid + "/" + model
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Member{Provider: p, Model: model})
	}
	return out
}

// efforts are the reasoning levels the member's model takes.
func (m Member) efforts() []string {
	for _, x := range m.Provider.Available() {
		if x.ID == m.Model {
			return x.Efforts
		}
	}
	return nil
}

// groupEfforts are the reasoning levels every member takes, in the first
// member's order.
func groupEfforts(ms []Member) []string {
	var out []string
	for i, m := range ms {
		es := m.efforts()
		if i == 0 {
			out = es
			continue
		}
		var keep []string
		for _, e := range out {
			if slices.Contains(es, e) {
				keep = append(keep, e)
			}
		}
		out = keep
	}
	return out
}

// putGroup stores g in place of the record with its id, or after the others.
func putGroup(f *file, g Group) {
	for i := range f.Groups {
		if f.Groups[i].ID == g.ID {
			f.Groups[i] = g
			return
		}
	}
	f.Groups = append(f.Groups, g)
}

// SaveGroup adds or replaces a user's routing group. Saving over a
// discovered group makes it the user's. Members that are groups are
// ignored; a group with no member left is an error.
func SaveGroup(g Group) error {
	ps := All()
	f := load()
	g.ID = groupRef(g.ID)
	if g.ID == "" {
		g.ID = Slug(g.Name)
	}
	if g.ID == "" {
		return errors.New("a routing group needs a name or an id")
	}
	if !groupIDRe.MatchString(g.ID) {
		return fmt.Errorf("group id must be lowercase letters, digits and dashes, not %q", g.ID)
	}
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" {
		g.Name = g.ID
	}
	known := map[string]bool{}
	for _, x := range listGroups(ps, f) {
		known[x.ID] = true
	}
	for _, x := range f.Groups {
		known[x.ID] = true
	}
	var members []string
	for _, m := range g.Members {
		m = strings.TrimSpace(m)
		if m == "" || strings.HasPrefix(m, groupPrefix) {
			continue
		}
		pid, model, ok := strings.Cut(m, "/")
		if !ok {
			if known[m] {
				continue // a group named bare
			}
			return fmt.Errorf("member %q should be written provider/model", m)
		}
		if pid == "" || model == "" {
			return fmt.Errorf("member %q should be written provider/model", m)
		}
		members = append(members, m)
	}
	if len(members) == 0 {
		return errors.New("组里至少要有一个模型")
	}
	g.Members = members
	g.Routing = cleanRouting(g.Routing)
	g.Affinity = cleanAffinity(g.Affinity)
	g.Hidden, g.Auto = false, false
	putGroup(&f, g)
	return store(f)
}

// DeleteGroup removes a user's group. A discovered group is hidden instead
// (ShowGroup brings it back), and so is a user's group that replaces one.
func DeleteGroup(id string) error {
	id = groupRef(id)
	ps := All()
	f := load()
	auto := false
	for _, g := range discoverGroups(offersOf(ps)) {
		if g.ID == id {
			auto = true
		}
	}
	var kept []Group
	found := false
	for _, g := range f.Groups {
		if g.ID == id {
			found = true
			continue
		}
		kept = append(kept, g)
	}
	switch {
	case auto:
		kept = append(kept, Group{ID: id, Hidden: true})
	case !found:
		return fmt.Errorf("no routing group %q", id)
	}
	f.Groups = kept
	return store(f)
}

// ShowGroup brings back a discovered group the user hid.
func ShowGroup(id string) error {
	id = groupRef(id)
	f := load()
	var kept []Group
	hid := false
	for _, g := range f.Groups {
		if g.ID == id && g.Hidden {
			hid = true
			continue
		}
		kept = append(kept, g)
	}
	if hid {
		f.Groups = kept
		return store(f)
	}
	for _, g := range listGroups(All(), f) {
		if g.ID == id {
			return nil
		}
	}
	return fmt.Errorf("no routing group %q", id)
}

// FindGroup looks a routing group up by id ("x" or "group/x"), with the
// members that can take a request now. It is false when there is no such
// group; a group with no member ready is found, with none in the list.
func FindGroup(id string) (Group, []Member, bool) {
	id = groupRef(id)
	ps := All()
	for _, g := range listGroups(ps, load()) {
		if g.ID == id {
			return g, resolveMembers(g.Members, ps), true
		}
	}
	return Group{}, nil, false
}

// SetAffinity sets how a provider's conversations stick to the key or
// account that answered them: "" automatic, "session", "turn" or "off".
// A value magpie doesn't know is taken as "".
func SetAffinity(providerID, affinity string) error {
	p, err := Find(providerID)
	if err != nil {
		return err
	}
	p.Affinity = affinity
	return Save(*p)
}
