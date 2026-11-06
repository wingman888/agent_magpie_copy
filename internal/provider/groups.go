package provider

// Routing groups: models the agents pick as one. A group is a list of
// "provider/model" members, from one provider or several, and the gateway
// spreads a request to group/<id> over the keys and accounts of all of them
// (see FindGroup). magpie makes an auto group itself for a model that two or
// more ready providers offer under one name. Those are worked out from what
// is ready each time; only what the user changes or hides is written to
// providers.json.

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Affinity values: how long a conversation keeps going to the key or account
// that answered it last (see Provider.Affinity).
const (
	AffinitySession = "session" // the whole conversation
	AffinityTurn    = "turn"    // one user turn, while tool results come back
	AffinityOff     = "off"     // not kept
)

// groupPrefix starts the model id of a routing group: group/<id>.
const groupPrefix = "group/"

// Group is a routing group the user made, or an auto group the user changed
// or hid.
type Group struct {
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Members  []string `json:"members,omitempty"` // "provider/model", in order
	Routing  string   `json:"routing,omitempty"` // "", "order", "rotate" or "least-used"
	Affinity string   `json:"affinity,omitempty"`

	// Auto is set on a group magpie made. Once the user changes it, it is
	// theirs and Auto is cleared. Hidden is set on an auto group the user
	// removed; ShowGroup brings it back.
	Auto   bool `json:"auto,omitempty"`
	Hidden bool `json:"hidden,omitempty"`
}

// Member is one key or account a group sends requests to.
type Member struct {
	// Provider is the provider as this key sees it (see WithKey); for a
	// signed-in account, the account itself.
	Provider Provider
	// Model is the vendor's model id.
	Model string
	// Key is the key used; zero for an account or a provider that needs none.
	Key KeyAccount
}

// routingValue is a routing as it is kept: one of the routings, or "" for
// smart. The older spelling "usage" is least-used.
func routingValue(s string) string {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case Ordered, Rotate, LeastUsed:
		return s
	case "usage":
		return LeastUsed
	}
	return ""
}

// affinityValue is an affinity as it is kept; anything unknown is "" (automatic).
func affinityValue(s string) string {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case AffinitySession, AffinityTurn, AffinityOff:
		return s
	}
	return ""
}

// SetAffinity says how a provider's conversations keep going to the key or
// account that answered them last: "" automatic, "session", "turn" or "off".
func SetAffinity(providerID, affinity string) error {
	p, err := Find(providerID)
	if err != nil {
		return err
	}
	p.Affinity = affinityValue(affinity)
	return Save(*p)
}

// groupID is the id a group is named by, with or without its group/ prefix.
func groupID(id string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(id), groupPrefix)))
}

// cleanMembers drops blanks, repeats and groups: a group holds models, not
// other groups.
func cleanMembers(ms []string) []string {
	var out []string
	for _, m := range ms {
		m = strings.TrimSpace(m)
		if m == "" || strings.HasPrefix(m, groupPrefix) || contains(out, m) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// SaveGroup adds a routing group, or replaces the one with its id. Saving an
// auto group makes it the user's. An empty id is made from the name, and an
// empty name is the id.
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
	g.Routing = routingValue(g.Routing)
	g.Affinity = affinityValue(g.Affinity)
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

// DeleteGroup removes a routing group the user made. An auto group is hidden
// instead, so that it doesn't come back the next time magpie works the groups
// out; ShowGroup brings it back.
func DeleteGroup(id string) error {
	id = groupID(id)
	f := load()
	at := -1
	for i, g := range f.Groups {
		if g.ID == id {
			at = i
			break
		}
	}
	isAuto := slices.ContainsFunc(autoGroups(All()), func(g Group) bool { return g.ID == id })
	hidden := Group{ID: id, Auto: true, Hidden: true}
	switch {
	case isAuto && at >= 0:
		f.Groups[at] = hidden
	case isAuto:
		f.Groups = append(f.Groups, hidden)
	case at >= 0:
		f.Groups = slices.Delete(f.Groups, at, at+1)
	default:
		return fmt.Errorf("no group %q", id)
	}
	return store(f)
}

// ShowGroup brings back an auto group the user hid.
func ShowGroup(id string) error {
	id = groupID(id)
	f := load()
	for i, g := range f.Groups {
		if g.ID == id && g.Hidden {
			f.Groups = slices.Delete(f.Groups, i, i+1)
			return store(f)
		}
	}
	return nil
}

// FindGroup looks a routing group up by its id, "group/<id>" or "<id>", and
// resolves its members to the keys and accounts that can answer now, in the
// group's order. ok is false when there is no such group, or the user hid it.
func FindGroup(id string) (Group, []Member, bool) {
	ps := All()
	g, ok := groupByID(ps, groupID(id))
	if !ok {
		return Group{}, nil, false
	}
	return g, members(ps, g), true
}

// storedGroup is the group saved under the id, if the user saved one.
func storedGroup(id string) (Group, bool) {
	for _, g := range load().Groups {
		if g.ID == id {
			return g, true
		}
	}
	return Group{}, false
}

// groupByID is the group as it stands now: the user's, else the auto one.
func groupByID(ps []Provider, id string) (Group, bool) {
	if id == "" {
		return Group{}, false
	}
	stored, has := storedGroup(id)
	switch {
	case has && stored.Hidden:
		return Group{}, false
	case has && !stored.Auto:
		return stored, true
	}
	for _, g := range autoGroups(ps) {
		if g.ID == id {
			return g, true
		}
	}
	return Group{}, false
}

// members resolves a group's members, in order, to every key or account that
// can answer for each model. A model listed twice counts once.
func members(ps []Provider, g Group) []Member {
	var out []Member
	seen := map[string]bool{}
	for _, path := range g.Members {
		pid, model, ok := strings.Cut(path, "/")
		if !ok || seen[path] {
			continue
		}
		seen[path] = true
		p, ok := find(ps, pid)
		if !ok || !p.Ready() {
			continue
		}
		out = append(out, memberKeys(p, model)...)
	}
	return out
}

// memberKeys is each key of p that can ask for model, in the order p keeps
// them. An account, or a provider that needs no key, is a single member.
func memberKeys(p Provider, model string) []Member {
	keys := p.KeysOn()
	if p.Account != nil || len(keys) == 0 {
		return []Member{{Provider: p, Model: model}}
	}
	var out []Member
	for _, k := range keys {
		if p.Serves(k, model) {
			out = append(out, Member{Provider: p.WithKey(k), Model: model, Key: k})
		}
	}
	return out
}

// groupEfforts are the reasoning levels every member's model supports, in
// the first member's order.
func groupEfforts(ms []Member) []string {
	var out []string
	for i, m := range ms {
		var es []string
		for _, am := range m.Provider.Available() {
			if am.ID == m.Model {
				es = am.Efforts
				break
			}
		}
		if i == 0 {
			out = slices.Clone(es)
			continue
		}
		out = slices.DeleteFunc(out, func(e string) bool { return !slices.Contains(es, e) })
	}
	return out
}

// groupEntries lists the routing groups that have a member to answer, after
// the providers' models in the catalog.
func groupEntries(ps []Provider) []Entry {
	var out []Entry
	for _, g := range allGroups(ps) {
		ms := members(ps, g)
		if len(ms) == 0 {
			continue
		}
		out = append(out, Entry{ID: groupPrefix + g.ID, Name: g.Name, Efforts: groupEfforts(ms), Members: g.Members})
	}
	return out
}

// allGroups is every group in effect: the user's, then the auto ones the user
// has neither hidden nor changed.
func allGroups(ps []Provider) []Group {
	var out []Group
	taken := map[string]bool{}
	for _, g := range load().Groups {
		taken[g.ID] = true
		if !g.Auto && !g.Hidden {
			out = append(out, g)
		}
	}
	for _, g := range autoGroups(ps) {
		if !taken[g.ID] {
			out = append(out, g)
		}
	}
	return out
}

// autoGroups works out the groups magpie makes itself: a model that two or
// more ready providers offer under the same name (see canonicalModel), as a
// group of them in the order the providers were added. The group is named for
// the first display name any of them gives the model, else the model's id.
func autoGroups(ps []Provider) []Group {
	type offer struct {
		provider, model, name string // name is a display name, when the model has one
	}
	var order []string
	offers := map[string][]offer{}
	for _, p := range ps {
		if !p.Ready() {
			continue
		}
		for _, m := range p.Exposed() {
			c := canonicalModel(m.ID)
			if c == "" {
				continue
			}
			list := offers[c]
			if len(list) > 0 && list[len(list)-1].provider == p.ID {
				continue // one member per provider and model
			}
			if len(list) == 0 {
				order = append(order, c)
			}
			name := ""
			if m.Name != "" && m.Name != m.ID {
				name = m.Name
			}
			offers[c] = append(list, offer{p.ID, m.ID, name})
		}
	}
	var out []Group
	seen := map[string]bool{}
	for _, c := range order {
		list := offers[c]
		slug := Slug(c)
		if len(list) < 2 || slug == "" || seen["auto-"+slug] {
			continue
		}
		g := Group{ID: "auto-" + slug, Auto: true}
		for _, o := range list {
			g.Members = append(g.Members, o.provider+"/"+o.model)
			if g.Name == "" {
				g.Name = o.name
			}
		}
		if g.Name == "" {
			g.Name = list[0].model
		}
		seen[g.ID] = true
		out = append(out, g)
	}
	return out
}

var (
	versionDot = regexp.MustCompile(`(\d)\.(\d)`)
	dateSuffix = regexp.MustCompile(`-\d{8}$`)
)

// canonicalModel is the name a model goes by when two vendors' names for it are
// compared, so that they group together: lowercase, without the vendor's
// prefix ("anthropic/…"), a version's dots as dashes (5.5 → 5-5), and without
// an 8-digit date ("-20260801"). A variant after a colon (":batch") is a model
// of its own, and a dot that is not between two digits (v1.beta) stays.
func canonicalModel(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	base, variant, hasVariant := strings.Cut(id, ":")
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
	if base == "" {
		return ""
	}
	if hasVariant {
		return base + ":" + variant
	}
	return base
}

// keyFit is how well a key suits a request in protocol req: 0 when the key is
// made for req, 1 when it names no protocol, 2 when it is made for another.
func keyFit(k KeyAccount, req Protocol) int {
	switch k.Protocol {
	case req:
		return 0
	case "":
		return 1
	}
	return 2
}

// PoolKeys splits keys for a request in protocol req. The main pool is the
// keys that suit it best: the ones made for req, else the ones that name no
// protocol, else the rest. Routing, rotation and affinity work inside it. The
// others come after it as asides, the unspecified before the keys made for
// another protocol. Each keeps the order it had.
func PoolKeys(keys []KeyAccount, req Protocol) (main, aside []KeyAccount) {
	best := 2
	for _, k := range keys {
		best = min(best, keyFit(k, req))
	}
	for fit := 0; fit <= 2; fit++ {
		for _, k := range keys {
			if keyFit(k, req) != fit {
				continue
			}
			if fit == best {
				main = append(main, k)
			} else {
				aside = append(aside, k)
			}
		}
	}
	return main, aside
}

// KeysFor is the keys of p in use for a request in protocol req, split as
// PoolKeys splits them. The first key's protocol is the provider's KeyProtocol.
func (p Provider) KeysFor(req Protocol) (main, aside []KeyAccount) {
	return PoolKeys(p.KeysOn(), req)
}
