package provider

// Routing groups: several models, from one vendor or several, that agents
// use as one model. An agent names a group group/<id>, and the gateway
// spreads its requests over the keys and accounts of every member.
//
// A group the user makes is kept in providers.json. A group magpie finds on
// its own — the same model offered by two or more ready vendors — is not
// kept: it is worked out each time from the vendors that are ready. Only
// changing one (saving it under its id) or hiding it writes it down.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// GroupPrefix starts the model id of every routing group: group/<id>.
const GroupPrefix = "group/"

// groupProvider is the provider id the groups keep to themselves. A member
// written group/… is a group nested in a group, which is ignored.
const groupProvider = "group"

// Affinity modes (Provider.Affinity, Group.Affinity): how a conversation
// keeps to the key or account that last answered it.
const (
	AffinityAuto    = ""        // kept within a turn; across turns while the vendor's cache is warm
	AffinitySession = "session" // kept for the whole conversation
	AffinityTurn    = "turn"    // kept within one user turn, so a new turn may move
	AffinityOff     = "off"     // never kept
)

// normalizeRouting keeps a routing the code knows and makes any other smart
// (""). "usage", the name least-used had before, reads as least-used.
func normalizeRouting(r string) string {
	switch r = strings.TrimSpace(r); r {
	case Ordered, Rotate, LeastUsed:
		return r
	case "usage":
		return LeastUsed
	}
	return ""
}

// normalizeAffinity keeps an affinity the code knows and makes any other
// automatic ("").
func normalizeAffinity(a string) string {
	switch a = strings.TrimSpace(a); a {
	case AffinitySession, AffinityTurn, AffinityOff:
		return a
	}
	return AffinityAuto
}

// Member is one model a group draws on: the provider and the model id that
// provider knows it by. In providers.json it is written "provider/model".
type Member struct {
	Provider string
	Model    string
}

func (m Member) String() string { return m.Provider + "/" + m.Model }

// ParseMember reads "provider/model". The model may hold slashes of its own
// (vendor/m), so the provider is what comes before the first one.
func ParseMember(s string) (Member, error) {
	p, model, ok := strings.Cut(strings.TrimSpace(s), "/")
	m := Member{Provider: strings.ToLower(strings.TrimSpace(p)), Model: strings.TrimSpace(model)}
	if !ok || m.Provider == "" || m.Model == "" {
		return Member{}, fmt.Errorf("组的成员 %q 应写作 提供方/模型", s)
	}
	return m, nil
}

// MarshalJSON writes a member as its "provider/model" string.
func (m Member) MarshalJSON() ([]byte, error) { return json.Marshal(m.String()) }

// UnmarshalJSON reads a member back. A string that is not a member is kept
// as far as it goes, so one bad entry cannot spoil the whole file; cleaning
// drops it when it is used.
func (m *Member) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	p, model, _ := strings.Cut(s, "/")
	m.Provider, m.Model = strings.ToLower(strings.TrimSpace(p)), strings.TrimSpace(model)
	return nil
}

// Group is a routing group: models an agent uses as one.
type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Members []Member `json:"members,omitempty"` // in order of use

	// Routing and Affinity mean what they do on a provider; see Provider.
	Routing  string `json:"routing,omitempty"`
	Affinity string `json:"affinity,omitempty"`

	// Hidden marks a group magpie found on its own that the user removed:
	// the record only keeps it away until ShowGroup.
	Hidden bool `json:"hidden,omitempty"`
}

// cleanMembers keeps the members a group can use, in order, each once. A
// member nested as group/… is ignored. One that is not provider/model is
// an error when strict, else dropped.
func cleanMembers(ms []Member, strict bool) ([]Member, error) {
	var out []Member
	for _, m := range ms {
		m.Provider = strings.ToLower(strings.TrimSpace(m.Provider))
		m.Model = strings.TrimSpace(m.Model)
		switch {
		case m.Provider == "" && m.Model == "":
			continue
		case m.Provider == groupProvider:
			continue
		case m.Provider == "" || m.Model == "":
			if strict {
				return nil, fmt.Errorf("组的成员 %q 应写作 提供方/模型", m.String())
			}
			continue
		}
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out, nil
}

// cleanGroup puts a group's fields in the form they are kept in.
func cleanGroup(g Group, strict bool) (Group, error) {
	g.ID = groupID(g.ID)
	g.Name = strings.TrimSpace(g.Name)
	g.Routing = normalizeRouting(g.Routing)
	g.Affinity = normalizeAffinity(g.Affinity)
	ms, err := cleanMembers(g.Members, strict)
	if err != nil {
		return Group{}, err
	}
	g.Members = ms
	return g, nil
}

// groupID reads a group's id from what the user typed: group/<id> or <id>.
func groupID(id string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), GroupPrefix)
}

// SaveGroup adds a routing group, or replaces the one with its id. The id,
// when left out, comes from the name; the name, when left out, is the id.
// Saving a group magpie found on its own under the same id makes it the
// user's.
func SaveGroup(g Group) error {
	g.ID = groupID(g.ID)
	if g.ID == "" {
		g.ID = Slug(g.Name)
	}
	if g.ID == "" || g.ID != Slug(g.ID) {
		return fmt.Errorf("group id must be lowercase letters, digits and dashes, not %q", g.ID)
	}
	g, err := cleanGroup(g, true)
	if err != nil {
		return err
	}
	if len(g.Members) == 0 {
		return errors.New("组里至少要有一个模型")
	}
	if g.Name == "" {
		g.Name = g.ID
	}
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

// DeleteGroup removes a group. One the user made is removed; one magpie
// found on its own is hidden, so it stays away until ShowGroup. A group
// that is not there is an error.
func DeleteGroup(id string) error {
	id = groupID(id)
	ps := All()
	auto := hasGroup(discover(ps, exposureOf(ps)), id)
	f := load()
	i := indexGroup(f.Groups, id)
	switch {
	case i >= 0 && f.Groups[i].Hidden:
		return fmt.Errorf("no routing group %q", id)
	case i >= 0 && auto:
		// the user's copy goes, and the one magpie finds would come back
		f.Groups[i] = Group{ID: id, Hidden: true}
	case i >= 0:
		f.Groups = slices.Delete(f.Groups, i, i+1)
	case auto:
		f.Groups = append(f.Groups, Group{ID: id, Hidden: true})
	default:
		return fmt.Errorf("no routing group %q", id)
	}
	return store(f)
}

// ShowGroup brings back a group magpie found on its own that DeleteGroup
// hid.
func ShowGroup(id string) error {
	id = groupID(id)
	f := load()
	keep := f.Groups[:0]
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

// FindGroup looks a routing group up by group/<id> or <id>. It returns the
// group with only the members that can be used now (their provider is
// ready), and whether there is such a group with at least one of them. A
// group with none is not usable: a caller answers it as not ready.
func FindGroup(id string) (Group, []Member, bool) {
	ps := All()
	x := exposureOf(ps)
	id = groupID(id)
	for _, g := range groupsNow(ps, x) {
		if g.ID != id {
			continue
		}
		g, _ = resolve(g, ps, x)
		return g, g.Members, len(g.Members) > 0
	}
	return Group{}, nil, false
}

// exposure is the models each ready vendor offers the agents, by provider id.
type exposure map[string][]catalog.Model

func exposureOf(ps []Provider) exposure {
	x := exposure{}
	for _, p := range ps {
		if p.Ready() {
			x[p.ID] = p.Exposed()
		}
	}
	return x
}

// model is the vendor's model with the id, as far as magpie knows it: from
// what the vendor exposes, else from its list. A model it knows nothing of
// is just its id.
func (x exposure) model(p Provider, id string) catalog.Model {
	for _, m := range x[p.ID] {
		if m.ID == id {
			return m
		}
	}
	for _, m := range p.Available() {
		if m.ID == id {
			return m
		}
	}
	return catalog.Model{ID: id}
}

// discover works out the groups magpie makes on its own: a model that two or
// more ready vendors offer under the same NormalizeModelKey. Members follow
// the order the vendors were added in; a vendor counts once per model.
func discover(ps []Provider, x exposure) []Group {
	type candidate struct {
		member Member
		model  catalog.Model
	}
	var keys []string
	byKey := map[string][]candidate{}
	for _, p := range ps {
		if !p.Ready() {
			continue
		}
		seen := map[string]bool{}
		for _, m := range x[p.ID] {
			key := NormalizeModelKey(m.ID)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			if _, ok := byKey[key]; !ok {
				keys = append(keys, key)
			}
			byKey[key] = append(byKey[key], candidate{Member{Provider: p.ID, Model: m.ID}, m})
		}
	}
	var out []Group
	for _, key := range keys {
		cs := byKey[key]
		if len(cs) < 2 {
			continue
		}
		g := Group{ID: autoID(key)}
		var models []catalog.Model
		for _, c := range cs {
			g.Members = append(g.Members, c.member)
			models = append(models, c.model)
		}
		g.Name = OfficialModelName(models)
		out = append(out, g)
	}
	return out
}

// autoID is the id of a group magpie finds on its own: auto-<key>. A key that
// is not a plain id already (a variant after a colon, a dot in a version)
// gets a short hash too, so two different models never share one id.
func autoID(key string) string {
	base := Slug(key)
	if base == key {
		return "auto-" + base
	}
	sum := sha256.Sum256([]byte(key))
	tag := hex.EncodeToString(sum[:4])
	if base == "" {
		return "auto-" + tag
	}
	return "auto-" + base + "-" + tag
}

// GroupAuto says whether a group id is one magpie finds on its own, rather
// than one the user keeps (saved, or replacing a found one).
func GroupAuto(id string) bool {
	id = groupID(id)
	f := load()
	if i := indexGroup(f.Groups, id); i >= 0 && !f.Groups[i].Hidden {
		return false
	}
	ps := All()
	return hasGroup(discover(ps, exposureOf(ps)), id)
}

// groupsNow is the groups as they stand: the user's first, then the ones
// magpie finds that the user has neither hidden nor replaced.
func groupsNow(ps []Provider, x exposure) []Group {
	f := load()
	var out []Group
	hidden := map[string]bool{}
	for _, g := range f.Groups {
		if g.Hidden {
			hidden[g.ID] = true
			continue
		}
		c, _ := cleanGroup(g, false)
		out = append(out, c)
	}
	for _, g := range discover(ps, x) {
		if hidden[g.ID] || hasGroup(out, g.ID) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// resolve keeps the members of g that can be used now, and the model each of
// them is.
func resolve(g Group, ps []Provider, x exposure) (Group, []catalog.Model) {
	var ms []Member
	var models []catalog.Model
	for _, m := range g.Members {
		p, ok := find(ps, m.Provider)
		if !ok || !p.Ready() {
			continue
		}
		ms = append(ms, m)
		models = append(models, x.model(p, m.Model))
	}
	g.Members = ms
	return g, models
}

// commonEfforts are the reasoning levels every one of the models takes, in
// the order the first of them lists them.
func commonEfforts(ms []catalog.Model) []string {
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

func hasGroup(gs []Group, id string) bool {
	return slices.ContainsFunc(gs, func(g Group) bool { return g.ID == id })
}

func indexGroup(gs []Group, id string) int {
	return slices.IndexFunc(gs, func(g Group) bool { return g.ID == id })
}
