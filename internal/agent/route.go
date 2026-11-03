package agent

import (
	"fmt"
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
	return viaDialExcept(prefix, "")
}

// viaDialExcept is viaDial without one provider, by id. An agent leaves out
// its own sign-in: its own models need no detour through the gateway.
func viaDialExcept(prefix, skip string) []Option {
	var out []Option
	for _, e := range provider.Catalog() {
		if e.Provider.ID == skip {
			continue
		}
		// a sign-in's note names the account, not the vendor
		note := e.Provider.Name
		if e.Provider.Account != nil {
			note = e.Provider.Account.User
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

// bareRef maps a bare model id (no provider prefix) to its catalog reference,
// "provider/model". It fails when no provider serves the id, and when several
// do: then the user has to name the provider, since dial cannot pick for them.
func bareRef(v string) (string, error) {
	var owners []string
	seen := map[string]bool{}
	for _, e := range provider.Catalog() {
		if e.Model == v && !seen[e.Provider.ID] {
			seen[e.Provider.ID] = true
			owners = append(owners, e.Provider.ID)
		}
	}
	if len(owners) > 1 {
		return "", fmt.Errorf("%q is served by several providers (%s); write one as provider/%s", v, strings.Join(owners, ", "), v)
	}
	p, m, ok := provider.Resolve(v)
	if !ok {
		return "", fmt.Errorf("%q is not a model in dial's catalog, or several providers serve it; write provider/model (see `dial models`)", v)
	}
	return p.ID + "/" + m, nil
}

// dialModels is the catalog as catalog.Models, for agents that keep their
// own model files.
func dialModels() []catalog.Model { return dialModelsExcept("") }

// dialModelsExcept is dialModels without one provider, by id.
func dialModelsExcept(skip string) []catalog.Model {
	var out []catalog.Model
	for _, e := range provider.Catalog() {
		if e.Provider.ID == skip {
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
