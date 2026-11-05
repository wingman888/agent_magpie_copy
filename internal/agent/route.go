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

// groupSection is the picker's section for routing groups, listed first.
const groupSection = "Routing groups"

// viaMagpie lists the catalog for a picker, one group per provider, and the
// routing groups first, in a section of their own.
func viaMagpie(prefix string) []Option {
	var out, groups []Option
	for _, e := range provider.Catalog() {
		if e.Group != nil {
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

// groupOption is a routing group's entry in a picker: its name, and the
// icon of each provider among its members, once per provider.
func groupOption(prefix string, e provider.Entry) Option {
	o := Option{Value: prefix + e.ID, Label: e.Name, Note: "routing group · via magpie", Group: groupSection, Ref: e.ID}
	_, ms, _ := provider.FindGroup(e.Group.ID)
	seen := map[string]bool{}
	for _, m := range ms {
		if seen[m.Provider.ID] || m.Provider.Icon == "" {
			continue
		}
		seen[m.Provider.ID] = true
		o.Icons = append(o.Icons, m.Provider.Icon)
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
