package gui

import (
	"encoding/json"
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

// groupsJSON is what the Routing view manages: the routing groups, the
// models they can be made of, and each provider with several keys or
// accounts on — which any of its models routes over already.
type groupsJSON struct {
	Groups []groupJSON `json:"groups"`
	Models []modelRef  `json:"models"`
	Pools  []poolJSON  `json:"pools"`
}

type groupJSON struct {
	provider.Group
	Ready bool         `json:"ready"` // a member is: agents can pick it
	Info  []memberJSON `json:"memberInfo"`
}

type memberJSON struct {
	ID       string `json:"id"`
	Ready    bool   `json:"ready"`
	Provider string `json:"provider,omitempty"`
	Name     string `json:"name,omitempty"` // the provider's
	Icon     string `json:"icon,omitempty"`
	Model    string `json:"model,omitempty"` // what the vendor is asked for
	On       int    `json:"on"`              // its keys or accounts on
}

type modelRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	PName    string `json:"providerName"`
	Icon     string `json:"icon,omitempty"`
}

type poolJSON struct {
	Provider string   `json:"provider"`
	Name     string   `json:"name"`
	Icon     string   `json:"icon,omitempty"`
	Kind     string   `json:"kind"` // "account" or "key"
	Who      []string `json:"who"`
	Routing  string   `json:"routing"`
	Affinity string   `json:"affinity"`
	// Protocol: the one its keys are made for, when the provider's keys are
	// made for more than one — each protocol's keys are a pool of their own
	Protocol provider.Protocol `json:"protocol,omitempty"`
}

// onOf is who a provider's requests spread over: its accounts, or keys.
func onOf(p provider.Provider) (kind string, who []string) {
	if p.Account != nil {
		who = append(who, p.Account.User)
		for _, q := range p.AlsoOn() {
			who = append(who, q.Account.User)
		}
		return "account", who
	}
	for _, k := range p.KeysOn() {
		n := k.Name
		if n == "" {
			n = provider.Mask(k.Key)
		}
		who = append(who, n)
	}
	return "key", who
}

// keyPools is a provider's keys by the protocol they're made for, as the
// gateway routes over them: keys made for different protocols are not one
// pool (gateway.perKeyOf).
func keyPools(p provider.Provider) []poolJSON {
	var out []poolJSON
	at := map[provider.Protocol]int{}
	for _, k := range p.KeysOn() {
		if len(p.WithKey(k).Speaks()) == 0 {
			continue // made for a protocol this provider has no endpoint for
		}
		i, ok := at[k.Protocol]
		if !ok {
			i = len(out)
			at[k.Protocol] = i
			out = append(out, poolJSON{Protocol: k.Protocol})
		}
		n := k.Name
		if n == "" {
			n = provider.Mask(k.Key)
		}
		out[i].Who = append(out[i].Who, n)
	}
	return out
}

func groupsState() groupsJSON {
	out := groupsJSON{Groups: []groupJSON{}, Models: []modelRef{}, Pools: []poolJSON{}}
	for _, e := range provider.Catalog() {
		if e.Group == "" {
			out.Models = append(out.Models, modelRef{ID: e.ID, Name: e.Name, Provider: e.Provider.ID, PName: e.Provider.Name, Icon: e.Provider.Icon})
		}
	}
	for _, g := range provider.Groups() {
		gj := groupJSON{Group: g, Info: []memberJSON{}}
		for _, id := range g.Members {
			m := memberJSON{ID: id}
			if p, model, ok := provider.Resolve(id); ok {
				_, who := onOf(p)
				m.Ready, m.Provider, m.Name, m.Icon, m.Model, m.On = true, p.ID, p.Name, p.Icon, model, max(len(who), 1)
				gj.Ready = true
			}
			gj.Info = append(gj.Info, m)
		}
		out.Groups = append(out.Groups, gj)
	}
	for _, p := range provider.All() {
		if !p.Ready() {
			continue
		}
		if kind, who := onOf(p); kind == "account" && len(who) > 1 {
			out.Pools = append(out.Pools, poolJSON{Provider: p.ID, Name: p.Name, Icon: p.Icon, Kind: kind, Who: who, Routing: p.Routing, Affinity: p.Affinity})
			continue
		}
		pools := keyPools(p)
		for _, kp := range pools {
			if len(kp.Who) > 1 {
				kp.Provider, kp.Name, kp.Icon, kp.Kind, kp.Routing, kp.Affinity = p.ID, p.Name, p.Icon, "key", p.Routing, p.Affinity
				if len(pools) == 1 {
					kp.Protocol = ""
				}
				out.Pools = append(out.Pools, kp)
			}
		}
	}
	return out
}

func groupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/groups", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, groupsState())
	})
	mux.HandleFunc("POST /api/groups/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var in provider.Group
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var err error
		switch r.PathValue("action") {
		case "save":
			err = provider.SaveGroup(in)
		case "delete":
			err = provider.DeleteGroup(in.ID)
		case "show":
			err = provider.ShowGroup(in.ID)
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, groupsState())
	})
}
