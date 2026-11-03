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

// groupsSection is the picker's header for routing groups, which come first.
const groupsSection = "Routing groups"

// viaMagpie lists the catalog for a picker: the routing groups first, in a
// section of their own, then one group per provider.
func viaMagpie(prefix string) []Option {
	var groups, out []Option
	for _, e := range provider.Catalog() {
		if e.Group != "" {
			groups = append(groups, groupOption(prefix, e))
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

// groupOption is a routing group's picker entry. Its icons are those of the
// providers its members come from, each shown once.
func groupOption(prefix string, e provider.Entry) Option {
	o := Option{Value: prefix + e.ID, Label: e.Name, Note: "routing group · via magpie",
		Group: groupsSection, Ref: e.ID}
	_, ms, _ := provider.FindGroup(e.ID)
	seen := map[string]bool{}
	for _, m := range ms {
		if seen[m.Provider] {
			continue
		}
		seen[m.Provider] = true
		icon := m.Target.Icon
		if icon == "" {
			icon = m.Provider
		}
		o.Icons = append(o.Icons, icon)
	}
	if len(o.Icons) > 0 {
		o.Icon = o.Icons[0]
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
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), provider.GroupPrefix) {
		_, _, ok := provider.FindGroup(v)
		return ok
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
		name := e.Name + " · " + e.Provider.Name
		if e.Group != "" {
			name = e.Name + " · routing group"
		}
		out = append(out, catalog.Model{ID: e.ID, Name: name, Provider: firstOf(e.Provider.Catalogs()), Efforts: e.Efforts})
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
