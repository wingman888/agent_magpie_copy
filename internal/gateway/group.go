package gateway

// Routing groups: a request for group/<id> is one request to several
// models, and routing spreads it over the keys and accounts of all of their
// providers. A member's own fallbacks don't count: a group's members are its
// fallbacks.

import (
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// isGroupRef says whether a model names a routing group.
func isGroupRef(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), provider.GroupPrefix)
}

// planGroup orders the candidates of a routing group's ready members for a
// request. The keys and accounts that suit the request form the pool, which
// the group's routing orders as one across all members; with the order
// routing, member by member, each by its own provider's routing. The asides
// follow the pool, the unnamed ones before those made for another protocol.
func (s *Server) planGroup(g provider.Group, ms []provider.Member, from provider.Protocol, cv convo) ([]candidate, planned) {
	var pl planned
	type part struct {
		m      provider.Member
		pool   []candidate
		asides []candidate
	}
	parts := make([]part, 0, len(ms))
	for _, m := range ms {
		cs, left := perKeyOf(m.Target, m.Model, from)
		pool, asides := splitAside(cs)
		parts = append(parts, part{m, pool, asides})
		for _, c := range left {
			w := weighed(c, m.Target, weighing{}, false, from)
			w.Unlisted = true
			pl.left = append(pl.left, w)
		}
	}

	var out []candidate
	var ws []Weighed
	wg := weighing{lefts: map[string]left{}}
	seen := map[string]bool{}
	// add appends candidates, each once, with what routing went by for them
	add := func(cs []candidate, x weighing, turn bool) {
		for i, c := range cs {
			k := candKey(c)
			if seen[k] {
				continue
			}
			seen[k] = true
			for key, v := range x.lefts {
				wg.lefts[key] = v
			}
			if x.tokens != nil {
				if wg.tokens == nil {
					wg.tokens = map[string]float64{}
				}
				for key, v := range x.tokens {
					wg.tokens[key] = v
				}
			}
			w := weighed(c, c.p, x, false, from)
			w.Turn = turn && i == 0 && len(cs) > 1
			out = append(out, c)
			ws = append(ws, w)
		}
	}

	if g.Routing == provider.Ordered {
		for _, pt := range parts {
			q := pt.m.Target
			pool, x := weighIn(q.Routing, pt.pool, pt.m.Model, from, cv, q.ID, q.Account == nil)
			add(pool, x, q.Routing == provider.Rotate)
		}
	} else {
		var pool []candidate
		for _, pt := range parts {
			pool = append(pool, pt.pool...)
		}
		pool, x := weighIn(g.Routing, pool, "", from, cv, provider.GroupPrefix+g.ID, false)
		add(pool, x, g.Routing == provider.Rotate)
	}

	var asides []candidate
	for _, pt := range parts {
		asides = append(asides, pt.asides...)
	}
	sort.SliceStable(asides, func(i, j int) bool {
		return keyFit(asides[i].p, asides[i].model, from) < keyFit(asides[j].p, asides[j].model, from)
	})
	add(asides, weighing{}, false)

	mode := affinityMode(g.Affinity, parts[0].m.Target.Affinity)
	if cv.id != "" {
		out, ws, pl.affinity = affine(mode, out, ws, wg.lefts, cv, g.Routing == provider.Rotate, time.Now())
	}
	pl.order = ws

	name := g.Name
	if name == "" {
		name = g.ID
	}
	members := make([]string, 0, len(ms))
	for _, m := range ms {
		members = append(members, m.Provider+"/"+m.Model)
	}
	pl.group = &GroupTrace{ID: g.ID, Name: name, Routing: g.Routing, Affinity: mode, Auto: g.Auto, Members: members}
	out, pl = sortResting(out, pl)
	return out, pl
}
