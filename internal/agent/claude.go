package agent

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// Claude Code reads its endpoint from the `env` block of settings.json.
// Pointing ANTHROPIC_BASE_URL at the gateway and naming a catalog model in
// ANTHROPIC_MODEL (and the aliases opus/sonnet/haiku/fable resolve through) is
// all it takes to run it on any provider.

// claudeTiers are the model tiers Claude Code offers, in display order.
var claudeTiers = []string{"opus", "sonnet", "haiku", "fable"}

// tierEnv is the variable a tier's model is read from.
func tierEnv(tier string) string {
	return "ANTHROPIC_DEFAULT_" + strings.ToUpper(tier) + "_MODEL"
}

// env vars magpie sets while routing through the gateway.
var claudeEnv = []string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
}

// claudeModelKVs is what Claude Code reads for its models while routed. The
// main model is written, and so is every tier: a tier that is not in explicit
// follows the main model. The haiku tier is also the small fast model. Subagents
// share one model only while every tier agrees; once a tier is set apart, the
// shared setting is returned in unset to be removed.
func claudeModelKVs(main string, explicit map[string]string) (kvs []edit.KV, unset []string) {
	kvs = append(kvs, edit.KV{Path: "env.ANTHROPIC_MODEL", Value: main})
	same := true
	for _, t := range claudeTiers {
		v := explicit[t]
		if v == "" {
			v = main
		}
		if v != main {
			same = false
		}
		kvs = append(kvs, edit.KV{Path: "env." + tierEnv(t), Value: v})
		if t == "haiku" {
			kvs = append(kvs, edit.KV{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: v})
		}
	}
	if same {
		kvs = append(kvs, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: main})
	} else {
		unset = append(unset, "env.CLAUDE_CODE_SUBAGENT_MODEL")
	}
	return kvs, unset
}

func claude(home string) *Agent {
	path := filepath.Join(home, ".claude", "settings.json")
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	model := jsonGet(path, "model")
	routed := func() bool { return env("ANTHROPIC_BASE_URL") == gateway.URL() }

	// explicitTiers are the tiers pointed at a model of their own, as the file
	// stands. A tier naming the main model, or naming nothing, follows it.
	explicitTiers := func() map[string]string {
		out := map[string]string{}
		if !routed() {
			return out
		}
		main := env("ANTHROPIC_MODEL")
		for _, t := range claudeTiers {
			if v := env(tierEnv(t)); v != "" && v != main {
				out[t] = v
			}
		}
		return out
	}

	// the value shown: the catalog ref while routed, else Claude's own model.
	get := func() string {
		if routed() {
			if m := env("ANTHROPIC_MODEL"); m != "" {
				return m
			}
		}
		return model()
	}
	set := func(v string) error {
		if v == "" {
			// Claude Code as installed: Anthropic's own endpoint and model
			keys := []string{"model"}
			for _, k := range claudeEnv {
				keys = append(keys, "env."+k)
			}
			forget("claude.model", "claude.base_url", "claude.auth_token")
			return edit.DelJSON(path, keys...)
		}
		if isMagpie(v) {
			if !routed() {
				stash(map[string]string{
					"claude.model":      model(),
					"claude.base_url":   env("ANTHROPIC_BASE_URL"),
					"claude.auth_token": env("ANTHROPIC_AUTH_TOKEN"),
				})
			}
			// read before anything is written: the tiers' own settings are
			// what decides which of them keep their model
			kvs, unset := claudeModelKVs(v, explicitTiers())
			kvs = append([]edit.KV{
				{Path: "env.ANTHROPIC_BASE_URL", Value: gateway.URL()},
				{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: gateway.Token},
			}, kvs...)
			kvs = append(kvs, edit.KV{Path: "model", Value: v})
			if err := edit.SetJSON(path, kvs...); err != nil {
				return err
			}
			if len(unset) > 0 {
				return edit.DelJSON(path, unset...)
			}
			return nil
		}
		if routed() {
			keys := make([]string, len(claudeEnv))
			for i, k := range claudeEnv {
				keys[i] = "env." + k
			}
			if err := edit.DelJSON(path, keys...); err != nil {
				return err
			}
			unstash("claude.model")
			var back []edit.KV
			if u := unstash("claude.base_url"); u != "" {
				back = append(back, edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: u})
			}
			if t := unstash("claude.auth_token"); t != "" {
				back = append(back, edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: t})
			}
			if len(back) > 0 {
				if err := edit.SetJSON(path, back...); err != nil {
					return err
				}
			}
		}
		return edit.SetJSON(path, edit.KV{Path: "model", Value: v})
	}

	// setTier points one tier at a magpie model, or back at the main model
	// when v is empty. Only meaningful once Claude Code is routed through magpie.
	setTier := func(tier, v string) error {
		main := env("ANTHROPIC_MODEL")
		if !routed() || main == "" {
			if v == "" {
				return nil // nothing to clear: Claude Code is not using magpie
			}
			return fmt.Errorf("Claude Code is not using magpie yet; set its main model first: magpie claude <provider/model>")
		}
		if v != "" && !isMagpie(v) {
			return fmt.Errorf("%q is not a model magpie provides; use provider/model", v)
		}
		explicit := explicitTiers()
		delete(explicit, tier)
		if v != "" {
			explicit[tier] = v
		}
		kvs, unset := claudeModelKVs(main, explicit)
		if err := edit.SetJSON(path, kvs...); err != nil {
			return err
		}
		if len(unset) > 0 {
			return edit.DelJSON(path, unset...)
		}
		return nil
	}

	fields := []Field{{
		Key: "model", Label: "model",
		Get: get,
		Set: set,
		Options: func(map[string]string) []Option {
			var own []Option
			for _, m := range catalog.Provider("anthropic") {
				if strings.HasPrefix(m.ID, "claude") {
					own = append(own, Option{Value: m.ID, Note: m.Name, Icon: "claude-color"})
				}
			}
			name := "Claude Code"
			if u := env("ANTHROPIC_BASE_URL"); u != "" && !routed() {
				name += " · " + hostOf(u)
			}
			// Only the catalog's models; Claude Code's own short aliases are
			// not something any API lists, and a compiled-in copy would just
			// go stale.
			return append(group(name, own), viaMagpie("")...)
		},
	}}
	for _, t := range claudeTiers {
		tier := t
		fields = append(fields, Field{
			Key: tier, Label: tier, Explicit: true,
			// a tier with no model of its own shows nothing: it is the main model
			Get: func() string {
				if !routed() {
					return ""
				}
				v := env(tierEnv(tier))
				if v == env("ANTHROPIC_MODEL") {
					return ""
				}
				return v
			},
			Set:     func(v string) error { return setTier(tier, v) },
			Options: func(map[string]string) []Option { return viaMagpie("") },
		})
	}

	return &Agent{
		ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Aliases: []string{"cc", "claude-code"},
		Bin: "claude", Dir: filepath.Dir(path), Path: path,
		Fields: fields,
	}
}
