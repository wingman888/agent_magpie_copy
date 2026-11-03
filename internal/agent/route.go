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
func viaDial(prefix string) []Option {
	return viaDialSkipping(prefix, nil)
}

// viaDialSkipping is viaDial without the providers skip returns true for.
func viaDialSkipping(prefix string, skip func(provider.Provider) bool) []Option {
	var out []Option
	for _, e := range provider.Catalog() {
		if skip != nil && skip(e.Provider) {
			continue
		}
		out = append(out, Option{Value: prefix + e.ID, Label: e.Name, Note: e.Provider.Owner() + " · via dial",
			Icon: e.Provider.Icon, Group: e.Provider.Name})
	}
	return out
}

// ownAccount is true for the provider that rides on Codex's own login: the
// Codex CLI already reaches those models, so it must not be offered them back.
func ownAccount(agentID string) func(provider.Provider) bool {
	return func(p provider.Provider) bool {
		return p.Account != nil && p.Account.Agent == agentID
	}
}

// isDial reports whether a model value is a catalog reference.
func isDial(v string) bool {
	_, _, ok := provider.Resolve(v)
	return ok && strings.Contains(v, "/")
}

// dialModels is the catalog as catalog.Models, for agents that keep their
// own model files.
func dialModels() []catalog.Model {
	return dialModelsSkipping(nil)
}

// dialModelsSkipping is dialModels without the providers skip returns true for.
func dialModelsSkipping(skip func(provider.Provider) bool) []catalog.Model {
	var out []catalog.Model
	for _, e := range provider.Catalog() {
		if skip != nil && skip(e.Provider) {
			continue
		}
		out = append(out, catalog.Model{ID: e.ID, Name: e.Name + " · " + e.Provider.Owner(), Provider: e.Provider.Catalog, Efforts: e.Efforts})
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
