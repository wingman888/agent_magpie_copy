package gateway

// Routing a routing group: the candidates of all its members, the keys and
// accounts each has, weighed together as the group's routing says — or, in
// order, member by member, each as its own provider routes. The protocol
// tiers are the group's own: the keys that fit the request best are the main
// pool across members, the rest follow, aside.

import (
	"sort"

	"github.com/yetone/magpie/internal/provider"
)

// planGroup is the candidates a request for a routing group tries, in order,
// and what routing went by. A member's own fallback models are not the
// group's: they are not tried for it.
func (s *Server) planGroup(g provider.Group, ms []provider.Member, from provider.Protocol, cv convo) ([]candidate, planned) {
	var pl planned
	type part struct {
		m  provider.Member
		cs []candidate
	}
	var parts []part
	var all []candidate
	seen := map[string]bool{}
	for _, m := range ms {
		cs, left := perKeyOf(m.Provider, m.Model, from)
		for _, c := range left {
			w := weighed(c, c.p, weighing{}, false, from)
			w.Unlisted = true
			pl.left = append(pl.left, w)
		}
		var mine []candidate
		for _, c := range cs {
			k := candKey(c)
			if seen[k] {
				continue // the same key for the same model counts once
			}
			seen[k] = true
			mine = append(mine, c)
		}
		parts = append(parts, part{m, mine})
		all = append(all, mine...)
	}
	// the best fit across the group, not each member's: a key made for
	// another protocol is aside here even when its member has no better one
	best := -1
	for _, c := range all {
		if f := keyFit(c.p, c.model, from); best < 0 || f < best {
			best = f
		}
	}
	fits := func(c candidate) bool { return keyFit(c.p, c.model, from) == best }

	var main []candidate
	var wg weighing
	wg.lefts, wg.tokens = map[string]left{}, map[string]float64{}
	if g.Routing == provider.Ordered {
		// each member in turn, its own keys routed as its provider routes them
		for _, pt := range parts {
			var mine []candidate
			for _, c := range pt.cs {
				if fits(c) {
					mine = append(mine, c)
				}
			}
			cs, w := weighConv(pt.m.Provider, mine, pt.m.Model, from, cv)
			mergeWeighing(&wg, w)
			main = append(main, cs...)
		}
	} else {
		var pool []candidate
		for _, pt := range parts {
			for _, c := range pt.cs {
				if fits(c) {
					pool = append(pool, c)
				}
			}
		}
		var w weighing
		main, w = weighBy(g.Routing, "group/"+g.ID, pool, from, cv)
		mergeWeighing(&wg, w)
	}

	var asides []candidate
	for _, c := range all {
		if !fits(c) {
			c.aside = true
			asides = append(asides, c)
		}
	}
	sort.SliceStable(asides, func(i, j int) bool {
		return keyFit(asides[i].p, asides[i].model, from) < keyFit(asides[j].p, asides[j].model, from)
	})

	out := append(append([]candidate{}, main...), asides...)
	for i, c := range out {
		w := weighed(c, c.p, wg, false, from)
		if !c.aside {
			w.Turn = i == 0 && g.Routing == provider.Rotate && len(main) > 1
		}
		pl.order = append(pl.order, w)
	}
	return settle(out, pl, affinityMode(g, ms), cv)
}

// mergeWeighing adds what one routing weighed to what a group's routing
// weighed.
func mergeWeighing(dst *weighing, src weighing) {
	if dst.lefts == nil {
		dst.lefts = map[string]left{}
	}
	if dst.tokens == nil {
		dst.tokens = map[string]float64{}
	}
	for k, v := range src.lefts {
		dst.lefts[k] = v
	}
	for k, v := range src.tokens {
		dst.tokens[k] = v
	}
}
