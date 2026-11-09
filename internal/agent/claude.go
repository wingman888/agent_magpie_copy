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

// claudeTiers are the model tiers, each of which may name its own model.
// A tier nobody named follows the main model.
var claudeTiers = []struct{ name, env string }{
	{"opus", "ANTHROPIC_DEFAULT_OPUS_MODEL"},
	{"sonnet", "ANTHROPIC_DEFAULT_SONNET_MODEL"},
	{"haiku", "ANTHROPIC_DEFAULT_HAIKU_MODEL"},
	{"fable", "ANTHROPIC_DEFAULT_FABLE_MODEL"},
}

// claudePinKey is where a tier's own pick is remembered (in the stash, so it
// survives a change of main model).
func claudePinKey(tier string) string { return "claude.tier." + tier }

func claude(home string) *Agent {
	path := filepath.Join(home, ".claude", "settings.json")
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	model := jsonGet(path, "model")
	routed := func() bool { return env("ANTHROPIC_BASE_URL") == gateway.URL() }
	// routedModel: routed through magpie with a main model in place.
	routedModel := func() bool { return routed() && env("ANTHROPIC_MODEL") != "" }

	// the value shown: the catalog ref while routed, else Claude's own model.
	get := func() string {
		if routed() {
			if m := env("ANTHROPIC_MODEL"); m != "" {
				return m
			}
		}
		return model()
	}

	// sync writes every tier's env var from the main model and the pins, the
	// small fast model from haiku, and the subagent model only when every
	// tier is the same model (otherwise subagents pick per tier).
	sync := func(main string) error {
		pins := stashLoad()
		var kvs []edit.KV
		unified := true
		for _, t := range claudeTiers {
			v := main
			if p := pins[claudePinKey(t.name)]; p != "" {
				v = p
			}
			if v != main {
				unified = false
			}
			kvs = append(kvs, edit.KV{Path: "env." + t.env, Value: v})
			if t.name == "haiku" {
				kvs = append(kvs, edit.KV{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: v})
			}
		}
		if err := edit.SetJSON(path, kvs...); err != nil {
			return err
		}
		if unified {
			return edit.SetJSON(path, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: main})
		}
		return edit.DelJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL")
	}

	pinKeys := func() []string {
		var out []string
		for _, t := range claudeTiers {
			out = append(out, claudePinKey(t.name))
		}
		return out
	}

	set := func(v string) error {
		if v == "" {
			// Claude Code as installed: Anthropic's own endpoint and model
			keys := []string{"model"}
			for _, k := range claudeEnv {
				keys = append(keys, "env."+k)
			}
			forget(append([]string{"claude.model", "claude.base_url", "claude.auth_token"}, pinKeys()...)...)
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
			return sync(v)
		}
		if routed() {
			keys := make([]string, len(claudeEnv))
			for i, k := range claudeEnv {
				keys[i] = "env." + k
			}
			if err := edit.DelJSON(path, keys...); err != nil {
				return err
			}
			forget(pinKeys()...)
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

	// tier builds the field for one tier. Unset tiers show nothing; setting
	// one pins it, and an empty value unpins it so it follows the main model.
	tier := func(name string) Field {
		return Field{
			Key: name, Label: name,
			Override: true,
			Get: func() string {
				if !routed() {
					return ""
				}
				return stashLoad()[claudePinKey(name)]
			},
			Set: func(v string) error {
				if v == "" {
					if !routedModel() {
						return nil
					}
					forget(claudePinKey(name))
					return sync(env("ANTHROPIC_MODEL"))
				}
				if !routedModel() {
					return fmt.Errorf("set the main model through magpie first, e.g. magpie claude <provider/model>")
				}
				if !isMagpie(v) {
					return fmt.Errorf("expected a provider/model that magpie offers, got %q", v)
				}
				stash(map[string]string{claudePinKey(name): v})
				return sync(env("ANTHROPIC_MODEL"))
			},
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
