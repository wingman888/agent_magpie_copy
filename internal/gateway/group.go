package gateway

// Routing groups in the gateway: a request for "group/<id>" is routed over the
// keys and accounts of all its members at once, in the group's routing; with
// "order" the members go in turn, each by its own provider's routing.

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// GroupTrace is the routing group a request went to, for the trace.
type GroupTrace struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Routing  string   `json:"routing"`
	Affinity string   `json:"affinity"` // the group's own; "" when it takes its provider's
	Auto     bool     `json:"auto"`
	Members  []string `json:"members"` // the members that can be used now, "provider/model"
}

// isGroupID: the model id is a routing group's.
func isGroupID(model string) bool {
	return strings.HasPrefix(model, provider.GroupPrefix+"/")
}

// countsUsage is false for a request an agent sent to a routing group; the
// provider usage log does not count those.
func countsUsage(model string) bool {
	return !isGroupID(model)
}

// groupNotReady answers a group none of whose members can be used.
func groupNotReady(id string) string {
	return fmt.Sprintf("routing group %s 没有模型就绪", id)
}

// groupAffinity is how a group keeps conversations: its own, else the first
// member's provider's.
func groupAffinity(g provider.Group, ms []provider.Member) string {
	if g.Affinity != "" {
		return provider.NormalizeAffinity(g.Affinity)
	}
	if len(ms) > 0 {
		return ms[0].Provider.Affinity
	}
	return provider.AffinityAuto
}

func groupTraceOf(g provider.Group, ms []provider.Member) *GroupTrace {
	t := &GroupTrace{ID: g.ID, Name: g.Name, Routing: provider.NormalizeRouting(g.Routing), Affinity: g.Affinity, Auto: g.Auto}
	for _, m := range ms {
		t.Members = append(t.Members, m.ID)
	}
	return t
}

// planGroup orders the candidates of a group's members. In order, each
// member's candidates come in the member's order, weighed by its provider's
// routing; in any other routing, the candidates of all members are weighed
// together, by the group's. Those that are aside come after the rest.
func planGroup(g provider.Group, ms []provider.Member, from provider.Protocol, rc routeCtx) ([]candidate, planned) {
	routing := provider.NormalizeRouting(g.Routing)
	wg := weighing{lefts: map[string]left{}, tokens: map[string]float64{}}
	var main, aside, unlisted, gathered []candidate
	seen := map[string]bool{}
	take := func(cs []candidate) {
		for _, c := range cs {
			if k := candKey(c); !seen[k] {
				seen[k] = true
				if c.aside {
					aside = append(aside, c)
				} else {
					main = append(main, c)
				}
			}
		}
	}
	for _, m := range ms {
		cs, left := perKeyOf(m.Provider, m.Model, from)
		unlisted = append(unlisted, left...)
		if routing == provider.Ordered {
			ordered, w := weighCtx(m.Provider, cs, m.Model, from, rc)
			mergeWeighing(&wg, w)
			take(ordered)
			continue
		}
		gathered = append(gathered, cs...)
	}
	if routing != provider.Ordered {
		var mains, asides []candidate
		for _, c := range gathered {
			if c.aside {
				asides = append(asides, c)
			} else {
				mains = append(mains, c)
			}
		}
		ordered, w := weighMain(routing, provider.GroupPrefix+"/"+g.ID, mains, "", from, rc)
		mergeWeighing(&wg, w)
		take(ordered)
		take(asides)
	}
	cs := append(main, aside...)
	var pl planned
	for _, c := range cs {
		pl.order = append(pl.order, weighed(c, c.p, wg, false, from))
	}
	for _, c := range unlisted {
		w := weighed(c, c.p, wg, false, from)
		w.Unlisted = true
		pl.left = append(pl.left, w)
	}
	// those resting after a failure go to the back, as plan does
	var ready, resting []candidate
	var wReady, wResting []Weighed
	for i, c := range cs {
		if r, ok := restOf(c.rest); ok {
			pl.order[i].Rest = &r
			resting, wResting = append(resting, c), append(wResting, pl.order[i])
		} else {
			ready, wReady = append(ready, c), append(wReady, pl.order[i])
		}
	}
	pl.order = append(wReady, wResting...)
	return append(ready, resting...), pl
}

// mergeWeighing adds what one weighing went by to dst.
func mergeWeighing(dst *weighing, src weighing) {
	for k, v := range src.lefts {
		dst.lefts[k] = v
	}
	for k, v := range src.tokens {
		if dst.tokens == nil {
			dst.tokens = map[string]float64{}
		}
		dst.tokens[k] = v
	}
}

// groupModelObject answers a capability question about a group: as its
// first member does.
func (s *Server) groupModelObject(w http.ResponseWriter, id string) {
	g, ms, ok := provider.FindGroup(id)
	if !ok {
		writeError(w, provider.Chat, http.StatusNotFound, groupNotReady(id))
		return
	}
	first := ms[0]
	obj := modelObject(provider.Entry{ID: provider.GroupPrefix + "/" + g.ID, Name: g.Name, Provider: first.Provider})
	efforts := slices.Clone(first.Efforts)
	if efforts == nil {
		efforts = []string{}
	}
	obj["owned_by"] = "magpie" // as the list says: a group belongs to no provider
	obj["efforts"] = efforts
	writeJSON(w, http.StatusOK, obj)
}
