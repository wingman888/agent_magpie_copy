package gateway

// Routing groups in the gateway: a request to group/<id> goes to every key and
// account of the group's members at once, weighed together as the group's
// routing says. The members' own fallbacks are not the group's.

import (
	"github.com/yetone/magpie/internal/provider"
)

// planGroup plans a request to a routing group whose members (resolved by
// provider.FindGroup, each key or account a member) can answer now.
func (s *Server) planGroup(rq *reqInfo, g provider.Group, ms []provider.Member, from provider.Protocol) ([]candidate, planned) {
	var cs []candidate
	seen := map[string]bool{}
	for _, m := range ms {
		var got []candidate
		if m.Provider.Account != nil {
			got, _ = perKeyOf(m.Provider, m.Model, from)
		} else {
			rest := m.Provider.ID
			if len(m.Provider.KeysOn()) > 1 {
				rest += "#" + provider.KeyID(m.Key.Key) // as perKeyOf names a provider's keys
			}
			got = []candidate{{m.Provider, m.Model, rest}}
		}
		for _, c := range got {
			k := c.rest + "\x00" + c.model
			if !seen[k] {
				seen[k] = true
				cs = append(cs, c)
			}
		}
	}

	main, aside := tiered(cs, from)
	var ordered []candidate
	wg := weighing{lefts: map[string]left{}, tokens: map[string]float64{}}
	if g.Routing == provider.Ordered {
		// member by member, in order; each member's keys go by its provider's routing
		for i := 0; i < len(main); {
			j := i + 1
			for j < len(main) && main[j].p.ID == main[i].p.ID && main[j].model == main[i].model {
				j++
			}
			chunk := main[i:j]
			owner := "member:" + chunk[0].p.ID + "/" + chunk[0].model
			out, got := orderMain(rq, chunk[0].p.Routing, owner, chunk, chunk[0].model, from, chunk[0].p.Account == nil)
			ordered = append(ordered, out...)
			mergeWG(&wg, got)
			i = j
		}
	} else {
		ordered, wg = orderMain(rq, g.Routing, "group:"+g.ID, main, "", from, false)
	}

	var items []item
	for i, c := range ordered {
		w := weighed(c, c.p, wg, false, from)
		w.Turn = i == 0 && g.Routing == provider.Rotate && len(ordered) > 1
		items = append(items, item{c: c, w: w})
	}
	for _, c := range aside {
		w := weighed(c, c.p, wg, false, from)
		w.Aside = true
		items = append(items, item{c: c, w: w, aside: true})
	}

	its, aff := settle(rq, g.Affinity, items)
	gt := &GroupTrace{ID: g.ID, Name: g.Name, Routing: g.Routing, Affinity: g.Affinity, Auto: g.Auto}
	names := map[string]bool{}
	for _, m := range ms {
		name := m.Provider.ID + "/" + m.Model
		if !names[name] {
			names[name] = true
			gt.Members = append(gt.Members, name)
		}
	}
	return candidatesOf(its), planned{order: weighedOf(its), group: gt, aff: aff}
}
