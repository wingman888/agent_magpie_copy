package gateway

// Routing groups: a "group/<id>" request goes to the keys and accounts of
// all the group's members at once. Routed as the group's routing says, the
// keys that suit the request are pooled across the members; the ones made
// for another protocol come after them all, in the order they suit. A
// member's own fallbacks are not the group's, so they are not added.

import (
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// groupRequest is the group id a request names by its model, if it names one.
func groupRequest(model string) (string, bool) {
	m := strings.TrimSpace(model)
	if len(m) < len("group/") || !strings.EqualFold(m[:len("group/")], "group/") {
		return "", false
	}
	return m[len("group/"):], true
}

// viaGroup is the "group/<id>" a request asked for by its model, or "" when
// it named a model instead. Usage keeps it apart from provider usage.
func viaGroup(model string) string {
	id, ok := groupRequest(model)
	if !ok {
		return ""
	}
	return "group/" + id
}

// groupScope is what a group's conversation rotation and affinity are kept for.
func groupScope(id string) string { return "group/" + id }

// groupRouting is a group's routing, or "" for one it doesn't know.
func groupRouting(s string) string {
	switch s {
	case provider.Ordered, provider.Rotate, provider.LeastUsed:
		return s
	}
	return ""
}

// planGroup orders the candidates of a routing group's available members.
func (s *Server) planGroup(g provider.Group, ms []provider.Member, from provider.Protocol, tc turnCtx) ([]candidate, planned) {
	var pl planned
	scope := groupScope(g.ID)
	tc.scope = scope
	pl.scope = scope
	routing := groupRouting(g.Routing)

	// each member's candidates, once each, and those it leaves out
	type member struct {
		m  provider.Member
		cs []candidate
	}
	var mems []member
	seen := map[string]bool{}
	var members []string
	for _, m := range ms {
		members = append(members, m.Provider.ID+"/"+m.Model)
		cs, left := perKeyOf(m.Provider, m.Model, from)
		var keep []candidate
		for _, c := range cs {
			if !seen[candKey(c)] {
				seen[candKey(c)] = true
				keep = append(keep, c)
			}
		}
		mems = append(mems, member{m, keep})
		for _, c := range left {
			w := weighed(c, m.Provider, weighing{}, false, from)
			w.Unlisted = true
			pl.left = append(pl.left, w)
		}
	}

	// the keys that suit the request best, across all members, make the
	// main pool; the others are asides behind it. Accounts are always main.
	best := -1
	for _, x := range mems {
		for _, c := range x.cs {
			if c.p.Account == nil {
				if t := keyFit(c.p, c.model, from); best < 0 || t < best {
					best = t
				}
			}
		}
	}
	perMain := make([][]candidate, len(mems))
	var asides []candidate
	for i, x := range mems {
		for _, c := range x.cs {
			c.aside = c.p.Account == nil && best >= 0 && keyFit(c.p, c.model, from) > best
			if c.aside {
				asides = append(asides, c)
			} else {
				perMain[i] = append(perMain[i], c)
			}
		}
	}
	sort.SliceStable(asides, func(i, j int) bool {
		return keyFit(asides[i].p, asides[i].model, from) < keyFit(asides[j].p, asides[j].model, from)
	})

	var out []candidate
	var wg weighing
	if routing == provider.Ordered {
		// member by member, each routed as its own provider routes
		for i, x := range mems {
			mtc := tc
			mtc.scope = x.m.Provider.ID + "/" + x.m.Model // a member's own rotation
			cs, mwg := weighTurn(x.m.Provider, perMain[i], x.m.Model, from, mtc)
			out = append(out, cs...)
			wg.merge(mwg)
		}
	} else {
		var all []candidate
		for _, cs := range perMain {
			all = append(all, cs...)
		}
		out, wg = weighMain(routing, scope, all, tc)
	}
	out = append(out, asides...)

	ws := make([]Weighed, len(out))
	for i, c := range out {
		ws[i] = weighed(c, c.p, wg, false, from)
		ws[i].Turn = i == 0 && routing == provider.Rotate && len(out) > 1
	}
	out, order, aff := settle(out, ws, affinityMode(g.Affinity), scope, routing, tc)
	pl.order, pl.aff = order, aff
	pl.group = &GroupTrace{ID: g.ID, Name: g.Name, Routing: routing, Affinity: g.Affinity,
		Auto: g.Auto, Members: members}
	return out, pl
}
