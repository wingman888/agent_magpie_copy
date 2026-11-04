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

// viaDialExcept is viaDial without the models of one signed-in account (an
// agent id such as "codex"). An agent that reaches that account on its own
// does not need it routed back through dial. "" skips nothing.
func viaDialExcept(prefix, account string) []Option {
	var out []Option
	for _, e := range provider.Catalog() {
		if skipAccount(e.Provider, account) {
			continue
		}
		out = append(out, Option{Value: prefix + e.ID, Label: e.Name, Note: sourceName(e.Provider) + " · via dial",
			Icon: e.Provider.Icon, Group: e.Provider.Name})
	}
	return out
}

// skipAccount is true for a model of the named signed-in account.
func skipAccount(p provider.Provider, account string) bool {
	return account != "" && p.Account != nil && p.Account.Agent == account
}

// sourceName is the name a catalog note gives a model's source: the signed-in
// user for an account, else the provider's name.
func sourceName(p provider.Provider) string {
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
func dialModels() []catalog.Model { return dialModelsExcept("") }

// dialModelsExcept is dialModels without the models of one signed-in account.
func dialModelsExcept(account string) []catalog.Model {
	var out []catalog.Model
	for _, e := range provider.Catalog() {
		if skipAccount(e.Provider, account) {
			continue
		}
		out = append(out, catalog.Model{ID: e.ID, Name: e.Name + " · " + sourceName(e.Provider), Provider: e.Provider.Catalog, Efforts: e.Efforts})
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
