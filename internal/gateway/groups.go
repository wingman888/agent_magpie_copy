package gateway

// Routing groups in the gateway: a group/<id> model is several models, each
// a provider's, that an agent uses as one. Its requests are spread over the
// keys and accounts of every member. A group that is smart, rotating or
// least used weighs them all together, as one pool; an ordered group takes
// its members in turn, each by its own provider's routing. A member's own
// fallbacks are not the group's: a group has only its members.

import (
	"github.com/yetone/magpie/internal/provider"
)

// memberOf is one member of a routing group that is ready to be used: its
// provider, and the model id that provider knows it by.
type memberOf struct {
	p     provider.Provider
	model string
}

// planGroup is the candidates for a routing group's request, in the order
// they are tried, and what the trace says of them.
func (s *Server) planGroup(g provider.Group, ms []memberOf, from provider.Protocol, tn turnRef) ([]candidate, planned) {
	var pl planned
	var items []placed
	seen := map[string]bool{}
	var pool []candidate
	seg := 0 // a member's place in an ordered group
	for _, m := range ms {
		key := m.p.ID + "/" + m.model
		if seen[key] { // the same model of the same provider counts once
			continue
		}
		seen[key] = true
		cs, left := perKeyOf(m.p, m.model, from)
		for _, c := range left {
			w := weighed(c, m.p, weighing{}, false, from)
			w.Unlisted = true
			pl.left = append(pl.left, w)
		}
		if g.Routing == provider.Ordered {
			cs, wg := weighIn(m.p, cs, m.model, from, tn)
			items = placeAll(items, cs, wg, m.p, false, seg, from, m.p.Routing == provider.Rotate)
			seg++
			continue
		}
		pool = append(pool, cs...)
	}
	if g.Routing == provider.Ordered {
		return arrange(items, pl)
	}
	// the keys that suit each request are the pool; those aside follow it
	main, aside := bestFitPool(pool, from)
	out, wg := weighPool(g.Routing, "group/"+g.ID, main, tn)
	out = append(out, aside...)
	mains := 0
	for _, c := range out {
		if !c.aside {
			mains++
		}
	}
	for i, c := range out {
		w := weighed(c, c.p, wg, false, from)
		w.Turn = i == 0 && g.Routing == provider.Rotate && mains > 1
		items = append(items, placed{c: c, w: w, seg: 0})
	}
	return arrange(items, pl)
}

// bestFitPool is bestFit over the candidates of several providers: accounts
// are always in the main pool, and the keys divide as bestFit divides them.
func bestFitPool(cs []candidate, from provider.Protocol) (main, aside []candidate) {
	var keys []candidate
	for _, c := range cs {
		if c.p.Account != nil {
			main = append(main, c)
		} else {
			keys = append(keys, c)
		}
	}
	km, ka := bestFit(keys, from)
	return append(main, km...), ka
}

// noGroupReady is the answer when a routing group has no member that can be
// used now. The Chinese reads first; the English is what clients have matched
// on from the start.
func noGroupReady(id string) string {
	return "路由组 " + id + " 没有模型就绪 · routing group " + id + " has no model ready"
}

// groupEntry is the model a routing group answers as: its first member that
// can be used now, as the catalog has it. It is false when no member can.
func groupEntry(id string) (provider.Entry, bool) {
	_, ms, ok := provider.FindGroup(id)
	if !ok {
		return provider.Entry{}, false
	}
	first := ms[0]
	for _, e := range provider.Catalog() {
		if e.Group == nil && e.ID == first.String() {
			return e, true
		}
	}
	p, err := provider.Find(first.Provider)
	if err != nil {
		return provider.Entry{}, false
	}
	return provider.Entry{ID: first.String(), Model: first.Model, Name: first.Model, Provider: *p}, true
}
