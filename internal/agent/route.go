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
func viaDial(prefix string) []Option { return viaDialFor("", prefix) }

// viaDialFor is viaDial for one agent: the agent's own account comes back
// out (Codex has no use for its own sign-in reached through dial).
func viaDialFor(agentID, prefix string) []Option {
	var out []Option
	for _, e := range catalogFor(agentID) {
		out = append(out, Option{Value: prefix + e.ID, Label: e.Name, Note: source(e) + " · via dial",
			Icon: e.Provider.Icon, Group: e.Provider.Name})
	}
	return out
}

// catalogFor is the catalog without the agent's own account.
func catalogFor(agentID string) []provider.Entry {
	var out []provider.Entry
	for _, e := range provider.Catalog() {
		if agentID != "" && e.Provider.Account != nil && e.Provider.Account.Agent == agentID {
			continue
		}
		out = append(out, e)
	}
	return out
}

// source is who serves a catalog entry: the signed-in user for an account,
// else the provider's name.
func source(e provider.Entry) string {
	if a := e.Provider.Account; a != nil && a.User != "" {
		return a.User
	}
	return e.Provider.Name
}

// isDial reports whether a model value is a catalog reference.
func isDial(v string) bool {
	_, _, ok := provider.Resolve(v)
	return ok && strings.Contains(v, "/")
}

// dialModels is the catalog as catalog.Models, for agents that keep their
// own model files.
func dialModels() []catalog.Model { return dialModelsFor("") }

// dialModelsFor is dialModels without the agent's own account.
func dialModelsFor(agentID string) []catalog.Model {
	var out []catalog.Model
	for _, e := range catalogFor(agentID) {
		out = append(out, catalog.Model{ID: e.ID, Name: e.Name + " · " + source(e), Provider: e.Provider.Catalog, Efforts: e.Efforts})
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
