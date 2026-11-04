package gateway

// Routing groups in the gateway: a request for group/<id> is spread over the
// keys and accounts of all the group's members, as one provider's would be
// (see provider.Group). Ordered groups take the members one after another,
// each routed as its own provider routes it; the others weigh every member's
// candidates together, by the group's routing.

import "github.com/yetone/magpie/internal/provider"

// planGroup is plan for a routing group: the candidates of its members, and
// what routing went by. A member's own fallbacks are not the group's.
func (s *Server) planGroup(g provider.Group, members []provider.Member, from provider.Protocol) ([]candidate, planned) {
	var pl planned
	var out []candidate
	if g.Routing == provider.Ordered {
		for _, m := range members {
			cs, left := perKeyOf(m.Provider, m.Model, from)
			cs, wg := weigh(m.Provider, cs, m.Model, from)
			for _, c := range cs {
				pl.order = append(pl.order, weighed(c, c.p, wg, false, from))
			}
			pl.left = append(pl.left, unlistedOf(left, from)...)
			out = append(out, cs...)
		}
		return settle(out, pl)
	}
	var all []candidate
	for _, m := range members {
		cs, left := perKeyOf(m.Provider, m.Model, from)
		all = append(all, cs...)
		pl.left = append(pl.left, unlistedOf(left, from)...)
	}
	// the group's own routing, over every member's keys and accounts; the
	// routing state is the group's, keyed by its id
	gp := provider.Provider{ID: provider.GroupPrefix + g.ID, Name: g.Name, Routing: g.Routing}
	cs, wg := weigh(gp, all, "", from)
	for i, c := range cs {
		w := weighed(c, c.p, wg, false, from)
		w.Turn = i == 0 && g.Routing == provider.Rotate && len(cs) > 1
		pl.order = append(pl.order, w)
	}
	return settle(cs, pl)
}

// unlistedOf is the candidates a vendor's lists left out, as the trace shows them.
func unlistedOf(left []candidate, from provider.Protocol) []Weighed {
	var out []Weighed
	for _, c := range left {
		w := weighed(c, c.p, weighing{}, false, from)
		w.Unlisted = true
		out = append(out, w)
	}
	return out
}

// groupTrace describes the group a request named, for the routing trace.
func groupTrace(g provider.Group, members []provider.Member) *GroupTrace {
	t := &GroupTrace{ID: g.ID, Name: g.Name, Routing: g.Routing, Affinity: g.Affinity, Auto: g.Auto, Members: []string{}}
	for _, m := range members {
		t.Members = append(t.Members, m.Name)
	}
	return t
}
