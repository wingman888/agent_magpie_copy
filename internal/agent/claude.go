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
// ANTHROPIC_MODEL (and the aliases opus/sonnet/haiku resolve through) is
// all it takes to run it on any provider.

// env vars magpie sets while routing through the gateway.
var claudeEnv = []string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL",
}

// claudeTiers are the model tiers Claude Code can be given separately.
var claudeTiers = []string{"opus", "sonnet", "haiku", "fable"}

func tierEnv(tier string) string { return "ANTHROPIC_DEFAULT_" + strings.ToUpper(tier) + "_MODEL" }

func claude(home string) *Agent {
	path := filepath.Join(home, ".claude", "settings.json")
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	model := jsonGet(path, "model")
	routed := func() bool { return env("ANTHROPIC_BASE_URL") == gateway.URL() }

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
			wasRouted := routed()
			if !wasRouted {
				stash(map[string]string{
					"claude.model":      model(),
					"claude.base_url":   env("ANTHROPIC_BASE_URL"),
					"claude.auth_token": env("ANTHROPIC_AUTH_TOKEN"),
				})
			}
			// tiers pinned to their own model stay; the rest follow the new main
			tiers := map[string]string{}
			if wasRouted {
				old := env("ANTHROPIC_MODEL")
				for _, t := range claudeTiers {
					if cur := env(tierEnv(t)); cur != "" && cur != old {
						tiers[t] = cur
					}
				}
			}
			kvs := []edit.KV{
				{Path: "env.ANTHROPIC_BASE_URL", Value: gateway.URL()},
				{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: gateway.Token},
				{Path: "env.ANTHROPIC_MODEL", Value: v},
				{Path: "model", Value: v},
			}
			kvs = append(kvs, tierKVs(v, tiers)...)
			if err := edit.SetJSON(path, kvs...); err != nil {
				return err
			}
			if len(tiers) == 0 {
				return nil
			}
			return edit.DelJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL")
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

	// A tier field: its own model through magpie, or empty to follow the main model.
	tierField := func(tier string) Field {
		return Field{
			Key: tier, Label: tier, HideEmpty: true,
			Get: func() string {
				if !routed() {
					return ""
				}
				if cur := env(tierEnv(tier)); cur != "" && cur != env("ANTHROPIC_MODEL") {
					return cur
				}
				return ""
			},
			Set: func(v string) error {
				if !routed() || env("ANTHROPIC_MODEL") == "" {
					if v == "" {
						return nil
					}
					return fmt.Errorf("Claude Code is not going through magpie yet: pick a main model from magpie first (magpie claude <provider/model>)")
				}
				main := env("ANTHROPIC_MODEL")
				if v != "" && !isMagpie(v) {
					return fmt.Errorf("%q is not a model magpie offers (use provider/model; magpie models lists them)", v)
				}
				tiers := map[string]string{}
				for _, t := range claudeTiers {
					cur := env(tierEnv(t))
					if t == tier {
						cur = v
					}
					if cur != "" && cur != main {
						tiers[t] = cur
					}
				}
				if err := edit.SetJSON(path, tierKVs(main, tiers)...); err != nil {
					return err
				}
				if len(tiers) > 0 {
					return edit.DelJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL")
				}
				return nil
			},
			Options: func(map[string]string) []Option { return viaMagpie("") },
		}
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
		fields = append(fields, tierField(t))
	}

	return &Agent{
		ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Aliases: []string{"cc", "claude-code"},
		Bin: "claude", Dir: filepath.Dir(path), Path: path,
		Fields: fields,
	}
}

// tierKVs writes every tier's env var: the pinned model, else the main
// model. Haiku also decides the small/fast model; the subagent model is
// forced to the main one only while no tier is pinned.
func tierKVs(main string, pinned map[string]string) []edit.KV {
	var kvs []edit.KV
	val := func(t string) string {
		if v := pinned[t]; v != "" {
			return v
		}
		return main
	}
	for _, t := range claudeTiers {
		kvs = append(kvs, edit.KV{Path: "env." + tierEnv(t), Value: val(t)})
	}
	kvs = append(kvs, edit.KV{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: val("haiku")})
	if len(pinned) == 0 {
		kvs = append(kvs, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: main})
	}
	return kvs
}
