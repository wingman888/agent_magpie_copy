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

// env vars magpie sets while routing through the gateway.
var claudeEnv = []string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
}

// claudeTiers are Claude Code's model tiers. A tier without its own pick
// follows the main model; a pick is kept in the stash so that changing the
// main model leaves it alone.
var claudeTiers = []struct{ name, env string }{
	{"opus", "ANTHROPIC_DEFAULT_OPUS_MODEL"},
	{"sonnet", "ANTHROPIC_DEFAULT_SONNET_MODEL"},
	{"haiku", "ANTHROPIC_DEFAULT_HAIKU_MODEL"},
	{"fable", "ANTHROPIC_DEFAULT_FABLE_MODEL"},
}

const tierPrefix = "claude.tier."

// tierKeys lists the stash keys holding tier picks.
func tierKeys() []string {
	var out []string
	for _, t := range claudeTiers {
		out = append(out, tierPrefix+t.name)
	}
	return out
}

// tierValues is what each tier resolves to: its own pick, else the main model.
func tierValues(main string) map[string]string {
	picks := stashLoad()
	out := make(map[string]string, len(claudeTiers))
	for _, t := range claudeTiers {
		out[t.name] = main
		if p := picks[tierPrefix+t.name]; p != "" {
			out[t.name] = p
		}
	}
	return out
}

// writeTiers writes every tier variable for the given main model. The shared
// subagent model is set only while every tier resolves to the main model;
// once one tier differs, Claude Code must use each tier's own model instead.
func writeTiers(path, main string) error {
	vals := tierValues(main)
	var kvs []edit.KV
	unified := true
	for _, t := range claudeTiers {
		kvs = append(kvs, edit.KV{Path: "env." + t.env, Value: vals[t.name]})
		if vals[t.name] != main {
			unified = false
		}
	}
	kvs = append(kvs, edit.KV{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: vals["haiku"]})
	if unified {
		kvs = append(kvs, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: main})
	}
	if err := edit.SetJSON(path, kvs...); err != nil {
		return err
	}
	if !unified {
		return edit.DelJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL")
	}
	return nil
}

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
			forget(append([]string{"claude.model", "claude.base_url", "claude.auth_token"}, tierKeys()...)...)
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
			if err := edit.SetJSON(path,
				edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: gateway.URL()},
				edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: gateway.Token},
				edit.KV{Path: "env.ANTHROPIC_MODEL", Value: v},
				edit.KV{Path: "model", Value: v},
			); err != nil {
				return err
			}
			return writeTiers(path, v)
		}
		if routed() {
			keys := make([]string, len(claudeEnv))
			for i, k := range claudeEnv {
				keys[i] = "env." + k
			}
			if err := edit.DelJSON(path, keys...); err != nil {
				return err
			}
			forget(tierKeys()...)
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

	// tier returns the field for one tier. Its value is its own pick only;
	// a tier following the main model shows nothing.
	tier := func(name string) Field {
		setTier := func(v string) error {
			main := ""
			if routed() {
				main = env("ANTHROPIC_MODEL")
			}
			if v == "" {
				if main == "" {
					return nil // not routed: nothing to clear
				}
				forget(tierPrefix + name)
				return writeTiers(path, main)
			}
			if main == "" {
				return fmt.Errorf("Claude Code is not routed through magpie yet: set its main model first, e.g. magpie claude deepseek/pro")
			}
			if !isMagpie(v) {
				return fmt.Errorf("%q is not a model magpie offers: use provider/model, e.g. deepseek/pro", v)
			}
			stash(map[string]string{tierPrefix + name: v})
			return writeTiers(path, main)
		}
		return Field{
			Key: name, Label: name, Optional: true,
			Get: func() string {
				if !routed() {
					return ""
				}
				return stashLoad()[tierPrefix+name]
			},
			Set: setTier,
			Options: func(map[string]string) []Option {
				return viaMagpie("")
			},
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
		fields = append(fields, tier(t.name))
	}

	return &Agent{
		ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Aliases: []string{"cc", "claude-code"},
		Bin: "claude", Dir: filepath.Dir(path), Path: path,
		Fields: fields,
	}
}
