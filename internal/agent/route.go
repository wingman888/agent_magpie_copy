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
	return viaDialExcept(prefix, nil)
}

// viaDialExcept is viaDial without the providers skip reports true for.
func viaDialExcept(prefix string, skip func(provider.Provider) bool) []Option {
	var out []Option
	for _, e := range provider.Catalog() {
		if skip != nil && skip(e.Provider) {
			continue
		}
		out = append(out, Option{Value: prefix + e.ID, Label: e.Name, Note: viaNote(e.Provider),
			Icon: e.Provider.Icon, Group: e.Provider.Name})
	}
	return out
}

// viaNote is the note under a catalog model: an account provider is named by
// the login it runs on, everything else by its vendor.
func viaNote(p provider.Provider) string {
	if p.IsAccount() && p.Account.User != "" {
		return p.Account.User + " · via dial"
	}
	return p.Name + " · via dial"
}

// isDial reports whether a model value is a catalog reference.
func isDial(v string) bool {
	_, _, ok := provider.Resolve(v)
	return ok && strings.Contains(v, "/")
}

// dialModels is the catalog as catalog.Models, for agents that keep their
// own model files.
func dialModels() []catalog.Model {
	var out []catalog.Model
	for _, e := range provider.Catalog() {
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
