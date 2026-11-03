package agent

import (
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// Every agent's model picker has two halves: the models the agent reaches
// on its own (its sign-in, its own keys), and magpie's catalog — every model
// of every provider the user added, reached through the local gateway.
// A catalog model is spelled "provider/model"; an agent that sends that to
// the gateway gets the vendor's reply in whichever API the agent speaks.

// magpieID is the provider id agents know the gateway by.
const magpieID = "magpie"

// routingGroups is the picker section of routing groups, shown first.
const routingGroups = "Routing groups"

// viaMagpie lists the catalog for a picker: the routing groups first, then
// one group per provider.
func viaMagpie(prefix string) []Option {
	return magpieOptions(provider.Catalog(), prefix)
}

// magpieOptions is viaMagpie over given catalog entries.
func magpieOptions(entries []provider.Entry, prefix string) []Option {
	var groups, out []Option
	for _, e := range entries {
		if e.Group != nil {
			groups = append(groups, groupOption(e, prefix))
			continue
		}
		note := e.Provider.Name + " · via magpie"
		if a := e.Provider.Account; a != nil {
			note = a.User + " · via magpie"
		}
		out = append(out, Option{Value: prefix + e.ID, Label: e.Name, Note: note,
			Icon: e.Provider.Icon, Group: e.Provider.Name, Ref: e.ID})
	}
	return append(groups, out...)
}

// groupOption is a routing group in the picker, with the icon of each of its
// members' providers, once each.
func groupOption(e provider.Entry, prefix string) Option {
	var icons []string
	seenProv, seenIcon := map[string]bool{}, map[string]bool{}
	for _, m := range e.Members {
		if seenProv[m.Provider] {
			continue
		}
		seenProv[m.Provider] = true
		if ic := m.Info.Icon; ic != "" && !seenIcon[ic] {
			seenIcon[ic] = true
			icons = append(icons, ic)
		}
	}
	o := Option{Value: prefix + e.ID, Label: e.Name, Note: "routing group · via magpie",
		Group: routingGroups, Ref: e.ID, Icons: icons}
	if len(icons) > 0 {
		o.Icon = icons[0]
	}
	return o
}

// viaMagpieFor is viaMagpie without the agent's own account: Codex CLI going
// through magpie to its own ChatGPT login would only add a hop.
func viaMagpieFor(agentID, prefix string) []Option {
	var out []Option
	for _, o := range viaMagpie(prefix) {
		if !strings.HasPrefix(o.Value, prefix+agentID+"/") {
			out = append(out, o)
		}
	}
	return out
}

// isMagpie reports whether a model value is a catalog reference.
func isMagpie(v string) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "group/") {
		// a routing group is magpie's, whichever provider its members are on
		return true
	}
	_, _, ok := provider.Resolve(v)
	return ok && strings.Contains(v, "/")
}

func firstOf(xs []string) string {
	if len(xs) > 0 {
		return xs[0]
	}
	return ""
}

// magpieModels is the catalog as catalog.Models, for agents that keep their
// own model files.
func magpieModels() []catalog.Model {
	var out []catalog.Model
	for _, e := range provider.Catalog() {
		if e.Group != nil {
			out = append(out, catalog.Model{ID: e.ID, Name: e.Name + " · routing group", Efforts: e.Efforts})
			continue
		}
		out = append(out, catalog.Model{ID: e.ID, Name: e.Name + " · " + e.Provider.Name, Provider: firstOf(e.Provider.Catalogs()), Efforts: e.Efforts})
	}
	return out
}

// group tags every option with a group name.
func group(name string, opts []Option) []Option {
	for i := range opts {
		opts[i].Group = name
	}
	return opts
}

func gatewayV1() string { return gateway.URL() + "/v1" }
