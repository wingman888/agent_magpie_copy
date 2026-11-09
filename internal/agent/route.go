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

// viaDial lists the catalog for a picker, one group per provider. An account
// provider's models name the signed-in user rather than the vendor.
func viaDial(prefix string) []Option {
	var out []Option
	for _, e := range provider.Catalog() {
		note := e.Provider.Name + " · via dial"
		if e.Provider.Account != nil {
			note = e.Provider.Account.User + " · via dial"
		}
		out = append(out, Option{Value: prefix + e.ID, Label: e.Name, Note: note,
			Icon: e.Provider.Icon, Group: e.Provider.Name})
	}
	return out
}

// withoutProvider drops the options whose catalog reference names provider id.
func withoutProvider(opts []Option, id string) []Option {
	var out []Option
	for _, o := range opts {
		if !strings.HasPrefix(o.Value, id+"/") {
			out = append(out, o)
		}
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
func dialModels() []catalog.Model {
	return dialModelsExcept("")
}

// dialModelsExcept is dialModels without the models of one provider id.
// Codex's own picker skips its account models: going through dial for them
// would only add a hop.
func dialModelsExcept(skip string) []catalog.Model {
	var out []catalog.Model
	for _, e := range provider.Catalog() {
		if skip != "" && e.Provider.ID == skip {
			continue
		}
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
