package agent

import (
	"fmt"
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

// viaMagpie lists the catalog for a picker, one group per provider.
func viaMagpie(prefix string) []Option {
	var out []Option
	for _, e := range provider.Catalog() {
		note := e.Provider.Name + " · via magpie"
		if a := e.Provider.Account; a != nil {
			note = a.User + " · via magpie"
		}
		out = append(out, Option{Value: prefix + e.ID, Label: e.Name, Note: note,
			Icon: e.Provider.Icon, Group: e.Provider.Name})
	}
	return out
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

// magpieModels is the catalog as catalog.Models, for agents that keep their
// own model files.
func magpieModels() []catalog.Model {
	var out []catalog.Model
	for _, e := range provider.Catalog() {
		out = append(out, catalog.Model{ID: e.ID, Name: e.Name + " · " + e.Provider.Name, Provider: e.Provider.Catalog, Efforts: e.Efforts})
	}
	return out
}

// FieldForValue returns the non-model field a bare value belongs to, or nil
// when the value is a model or belongs to no field. A value the model field
// already offers is a model, even when another field offers it too (Claude
// Code's tiers take the same provider/model).
func FieldForValue(a *Agent, v string) *Field {
	vals := a.Values()
	for _, o := range a.Fields[0].Options(vals) {
		if o.Value == v {
			return nil
		}
	}
	for i := 1; i < len(a.Fields); i++ {
		for _, o := range a.Fields[i].Options(vals) {
			if o.Value == v {
				return &a.Fields[i]
			}
		}
	}
	return nil
}

// BareTarget decides which field `magpie <agent> <value>` sets: the field the
// value belongs to (see FieldForValue), else the main model. A lone tier name
// such as `magpie claude opus` names a tier with no model after it; it is
// refused rather than taken as the main model's literal value.
func BareTarget(a *Agent, v string) (string, error) {
	if f := FieldForValue(a, v); f != nil {
		return f.Key, nil
	}
	for i := 1; i < len(a.Fields); i++ {
		if f := &a.Fields[i]; f.Optional && f.Key == v {
			return "", fmt.Errorf("usage: magpie %s %s <provider/model>  (or magpie %s %s \"\" to follow the main model)", a.ID, v, a.ID, v)
		}
	}
	return a.Fields[0].Key, nil
}

// group tags every option with a group name.
func group(name string, opts []Option) []Option {
	for i := range opts {
		opts[i].Group = name
	}
	return opts
}

func gatewayV1() string { return gateway.URL() + "/v1" }
