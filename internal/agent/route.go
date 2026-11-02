package agent

import (
	"strings"

	"github.com/yetone/dial/internal/catalog"
	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

// Every agent's model picker has two halves: the models the agent reaches
// on its own (its sign-in, its own keys), and dial's catalog — every model
// of every provider the user added, reached through the local gateway.
// A catalog model is spelled "provider/model"; an agent that sends that to
// the gateway gets the vendor's reply in whichever API the agent speaks.

// dialID is the provider id agents know the gateway by.
const dialID = "dial"

// viaDial lists the catalog for a picker, one group per provider.
func viaDial(prefix string) []Option { return viaDialExcept(prefix, "") }

// viaDialExcept is viaDial without the models of one agent's own account:
// sending Codex through dial back to its own ChatGPT sign-in is a detour.
func viaDialExcept(prefix, agentID string) []Option {
	var out []Option
	for _, e := range catalogExcept(agentID) {
		note := e.Provider.Name
		if a := e.Provider.Account; a != nil {
			note = a.User
		}
		out = append(out, Option{Value: prefix + e.ID, Label: e.Name, Note: note + " · via dial",
			Icon: e.Provider.Icon, Group: e.Provider.Name})
	}
	return out
}

// isDial reports whether a model value is a catalog reference.
func isDial(v string) bool {
	_, _, ok := provider.Resolve(v)
	return ok && strings.Contains(v, "/")
}

// dialModels is the catalog as catalog.Models, for agents that keep their
// own model files.
func dialModels() []catalog.Model { return dialModelsExcept("") }

func dialModelsExcept(agentID string) []catalog.Model {
	var out []catalog.Model
	for _, e := range catalogExcept(agentID) {
		out = append(out, catalog.Model{ID: e.ID, Name: e.Name + " · " + e.Provider.Name, Provider: e.Provider.Catalog, Efforts: e.Efforts})
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

// catalogExcept is the catalog minus the models of the account an agent is signed in with.
func catalogExcept(agentID string) []provider.Entry {
	all := provider.Catalog()
	if agentID == "" {
		return all
	}
	var out []provider.Entry
	for _, e := range all {
		if a := e.Provider.Account; a != nil && a.Agent == agentID {
			continue
		}
		out = append(out, e)
	}
	return out
}
