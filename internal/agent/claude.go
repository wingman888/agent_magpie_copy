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
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
}

// claudeTiers are Claude Code's model tiers; each can be pinned to its own
// model, otherwise it follows the main one.
var claudeTiers = []string{"opus", "sonnet", "haiku", "fable"}

func tierVar(tier string) string { return "ANTHROPIC_DEFAULT_" + strings.ToUpper(tier) + "_MODEL" }

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
			if !routed() {
				stash(map[string]string{
					"claude.model":      model(),
					"claude.base_url":   env("ANTHROPIC_BASE_URL"),
					"claude.auth_token": env("ANTHROPIC_AUTH_TOKEN"),
				})
			}
			// tiers pinned to their own model keep it; the rest follow the new main
			prev := env("ANTHROPIC_MODEL")
			wasRouted := routed()
			kvs := []edit.KV{
				{Path: "env.ANTHROPIC_BASE_URL", Value: gateway.URL()},
				{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: gateway.Token},
				{Path: "env.ANTHROPIC_MODEL", Value: v},
				{Path: "model", Value: v},
			}
			tiers := map[string]string{}
			for _, t := range claudeTiers {
				tiers[t] = v
				if cur := env(tierVar(t)); wasRouted && cur != "" && cur != prev {
					tiers[t] = cur
				}
			}
			return writeTiers(path, kvs, v, tiers)
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

	main := func() string {
		if routed() {
			return env("ANTHROPIC_MODEL")
		}
		return ""
	}
	tierField := func(tier string) Field {
		return Field{
			Key: tier, Label: tier,
			Get: func() string {
				m := main()
				if v := env(tierVar(tier)); m != "" && v != m {
					return v
				}
				return ""
			},
			Set: func(v string) error {
				m := main()
				if v == "" {
					if m == "" {
						return nil
					}
				} else {
					if m == "" {
						return fmt.Errorf("Claude Code is not going through magpie yet: pick a main model via magpie first (magpie claude provider/model)")
					}
					if !isMagpie(v) {
						return fmt.Errorf("%q is not a model magpie provides (use provider/model; magpie models lists them)", v)
					}
				}
				tiers := map[string]string{}
				for _, t := range claudeTiers {
					tiers[t] = env(tierVar(t))
					if tiers[t] == "" {
						tiers[t] = m
					}
				}
				if v == "" {
					v = m
				}
				tiers[tier] = v
				return writeTiers(path, nil, m, tiers)
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

// writeTiers writes the per-tier models (kvs are extra settings to write in
// the same edit). The small fast model is the haiku tier; the subagent model
// is forced to the main one only while no tier is pinned elsewhere.
func writeTiers(path string, kvs []edit.KV, main string, tiers map[string]string) error {
	uniform := true
	for _, t := range claudeTiers {
		kvs = append(kvs, edit.KV{Path: "env." + tierVar(t), Value: tiers[t]})
		if tiers[t] != main {
			uniform = false
		}
	}
	kvs = append(kvs, edit.KV{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: tiers["haiku"]})
	if uniform {
		kvs = append(kvs, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: main})
		return edit.SetJSON(path, kvs...)
	}
	if err := edit.SetJSON(path, kvs...); err != nil {
		return err
	}
	return edit.DelJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL")
}
