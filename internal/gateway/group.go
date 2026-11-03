package gateway

// Routing groups: a request for "group/<id>" is spread over the keys and
// accounts of all the group's members, as one provider's would be (see plan).
// Smart, rotate and least-used routing weigh all of them together; order
// routing goes member by member, each member's own keys ordered by its
// provider's routing. A member's fallbacks are not the group's: the group
// only ever tries its members. A member listed twice, or a group listed in a
// group, counts once or not at all (see provider.FindGroup).

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// isGroupModel reports whether a model an agent asked for is a routing group's.
func isGroupModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "group/")
}

// viaGroup is the routing group ("group/<id>") a request asked for, or "" when
// it named a provider's model.
func viaGroup(model string) string {
	if isGroupModel(model) {
		return strings.TrimSpace(model)
	}
	return ""
}

// groupTrace is a group as a request's trace shows it.
func groupTrace(g provider.Group, ms []provider.Member) *GroupTrace {
	name := g.Name
	if name == "" {
		name = g.ID
	}
	gt := &GroupTrace{ID: g.ID, Name: name, Routing: g.Routing, Affinity: g.Affinity, Auto: g.Auto, Members: []string{}}
	for _, m := range ms {
		gt.Members = append(gt.Members, m.Provider+"/"+m.Model)
	}
	return gt
}

// groupRotates is whether a group's requests rotate over their keys, as the
// affinity decision needs to know (see keepTo). A rotating group does; so does
// an order group whose member's own routing rotates, since planGroup rotates
// that member's keys by its provider's routing. A smart or least-used group
// weighs every member's keys together and never rotates a member's.
func groupRotates(g provider.Group, ms []provider.Member) bool {
	switch g.Routing {
	case provider.Rotate:
		return true
	case provider.Ordered:
		for _, m := range ms {
			if m.Info.Routing == provider.Rotate {
				return true
			}
		}
	}
	return false
}

// candidateKey tells candidates apart by key and model: one key may serve
// two models of a group.
func candidateKey(c candidate) string { return c.rest + "\x00" + c.model }

// planGroup is the candidates of a routing group, in the order they are tried,
// and how each was weighed.
func (s *Server) planGroup(g provider.Group, ms []provider.Member, from provider.Protocol, turn *sessionTurn) ([]candidate, planned) {
	var pl planned
	type part struct {
		q           provider.Provider
		model       string
		main, aside []candidate
	}
	parts := make([]part, 0, len(ms))
	for _, m := range ms {
		cs, left := perKeyOf(m.Info, m.Model, from)
		if len(cs) == 0 {
			continue
		}
		main, aside := pools(m.Info, cs, m.Model, from)
		parts = append(parts, part{m.Info, m.Model, main, aside})
		for _, c := range left {
			w := weighed(c, c.p, weighing{}, false, from)
			w.Unlisted = true
			pl.left = append(pl.left, w)
		}
	}

	wg := weighing{lefts: map[string]left{}, tokens: map[string]float64{}}
	onTurn := map[string]bool{} // the key whose turn it is, among the main pool
	markTurn := func(cs []candidate, rotating bool) {
		if rotating && len(cs) > 1 {
			onTurn[candidateKey(cs[0])] = true
		}
	}
	var mainOrder, asideAll []candidate
	if g.Routing == provider.Ordered {
		// member by member, in the order they were listed; each member's keys
		// as its own provider routes them
		for _, pt := range parts {
			out, w := routeMain(pt.q, pt.main, pt.model, turn)
			wg.merge(w)
			markTurn(out, pt.q.Routing == provider.Rotate)
			mainOrder = append(mainOrder, out...)
		}
	} else {
		// one pool over all the members' keys, weighed by the group's routing
		var all []candidate
		for _, pt := range parts {
			all = append(all, pt.main...)
		}
		out, w := routeMain(provider.Provider{ID: "group/" + g.ID, Routing: g.Routing}, all, "", turn)
		wg.merge(w)
		markTurn(out, g.Routing == provider.Rotate)
		mainOrder = out
	}
	for _, pt := range parts {
		asideAll = append(asideAll, pt.aside...)
	}

	var main, aside []slot
	for _, c := range mainOrder {
		w := weighed(c, c.p, wg, false, from)
		w.Turn = onTurn[candidateKey(c)]
		main = append(main, slot{c, w})
	}
	for _, c := range asideAll {
		w := weighed(c, c.p, wg, false, from)
		w.Aside = true
		aside = append(aside, slot{c, w})
	}
	return arrange(main, aside, from, pl)
}

// noModelReady is the reply to a request for a group none of whose members is usable.
func noModelReady(id string) string {
	return fmt.Sprintf("路由组 group/%s 没有模型就绪 (group/%s has no model ready: none of its members is usable)", id, id)
}
