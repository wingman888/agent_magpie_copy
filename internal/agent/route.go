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
func viaDial(prefix string) []Option { return viaDialFor(prefix, "") }

// viaDialFor is viaDial for an agent that may be signed in to an account
// itself; that account's models are left out (see catalogFor).
func viaDialFor(prefix, self string) []Option {
	var out []Option
	for _, e := range catalogFor(self) {
		out = append(out, Option{Value: prefix + e.ID, Label: e.Name, Note: who(e.Provider) + " · via dial",
			Icon: e.Provider.Icon, Group: e.Provider.Name})
	}
	return out
}

// catalogFor is the dial catalog as the agent named self should see it. An
// agent's own sign-in is left out: sending its models back through dial would
// only come around to where it started. self is "" for no exclusion.
func catalogFor(self string) []provider.Entry {
	var out []provider.Entry
	for _, e := range provider.Catalog() {
		if self != "" && e.Provider.Account != nil && e.Provider.Account.Agent == self {
			continue
		}
		out = append(out, e)
	}
	return out
}

// who names a provider in a picker: the signed-in user for an account, else
// the provider's name.
func who(p provider.Provider) string {
	if p.Account != nil && p.Account.User != "" {
		return p.Account.User
	}
	return p.Name
}

// isDial reports whether a model value is a catalog reference.
func isDial(v string) bool {
	_, _, ok := provider.Resolve(v)
	return ok && strings.Contains(v, "/")
}

// dialModels is the catalog as catalog.Models, for agents that keep their
// own model files.
func dialModels() []catalog.Model { return dialModelsFor("") }

// dialModelsFor is dialModels as seen by the agent named self (see catalogFor).
func dialModelsFor(self string) []catalog.Model {
	var out []catalog.Model
	for _, e := range catalogFor(self) {
		out = append(out, catalog.Model{ID: e.ID, Name: e.Name + " · " + who(e.Provider), Provider: e.Provider.Catalog, Efforts: e.Efforts})
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
