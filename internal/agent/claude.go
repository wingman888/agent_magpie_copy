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

// claudeTier is one model alias Claude Code resolves from its own env var.
type claudeTier struct{ key, env string }

var claudeTiers = []claudeTier{
	{"opus", "ANTHROPIC_DEFAULT_OPUS_MODEL"},
	{"sonnet", "ANTHROPIC_DEFAULT_SONNET_MODEL"},
	{"haiku", "ANTHROPIC_DEFAULT_HAIKU_MODEL"},
	{"fable", "ANTHROPIC_DEFAULT_FABLE_MODEL"},
}

// claudeTierFlag is the stash key recording that a tier was picked on its own.
// A tier without the flag follows the main model.
func claudeTierFlag(key string) string { return "claude.tier." + key }

func claude(home string) *Agent {
	path := filepath.Join(home, ".claude", "settings.json")
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	model := jsonGet(path, "model")
	routed := func() bool { return env("ANTHROPIC_BASE_URL") == gateway.URL() }
	tierPicked := func(key string) bool { return stashLoad()[claudeTierFlag(key)] != "" }

	var tierFlags []string
	for _, t := range claudeTiers {
		tierFlags = append(tierFlags, claudeTierFlag(t.key))
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

	// relink brings the tier variables, the small fast model and the shared
	// subagent model in line with the main model. Tiers picked on their own
	// keep their value; the rest follow the main model. While any tier differs
	// from the main model, subagents are not forced onto one model.
	relink := func() error {
		main := env("ANTHROPIC_MODEL")
		var kvs []edit.KV
		uniform := true
		for _, t := range claudeTiers {
			v := main
			if cur := env(t.env); tierPicked(t.key) && cur != "" {
				v = cur
			}
			if v != main {
				uniform = false
			}
			kvs = append(kvs, edit.KV{Path: "env." + t.env, Value: v})
			if t.key == "haiku" {
				kvs = append(kvs, edit.KV{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: v})
			}
		}
		if err := edit.SetJSON(path, kvs...); err != nil {
			return err
		}
		if uniform {
			return edit.SetJSON(path, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: main})
		}
		return edit.DelJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL")
	}

	set := func(v string) error {
		if v == "" {
			// Claude Code as installed: Anthropic's own endpoint and model
			keys := []string{"model"}
			for _, k := range claudeEnv {
				keys = append(keys, "env."+k)
			}
			forget(append([]string{"claude.model", "claude.base_url", "claude.auth_token"}, tierFlags...)...)
			return edit.DelJSON(path, keys...)
		}
		if isMagpie(v) {
			old := env("ANTHROPIC_MODEL")
			if !routed() {
				stash(map[string]string{
					"claude.model":      model(),
					"claude.base_url":   env("ANTHROPIC_BASE_URL"),
					"claude.auth_token": env("ANTHROPIC_AUTH_TOKEN"),
				})
				// tier values left by an earlier, unrelated config are not picks
				forget(tierFlags...)
			} else {
				// already routed: a tier that differs from the old main model was
				// picked on its own; remember it before the main model moves.
				marks := map[string]string{}
				for _, t := range claudeTiers {
					if cur := env(t.env); cur != "" && cur != old {
						marks[claudeTierFlag(t.key)] = "1"
					}
				}
				stash(marks)
			}
			if err := edit.SetJSON(path,
				edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: gateway.URL()},
				edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: gateway.Token},
				edit.KV{Path: "env.ANTHROPIC_MODEL", Value: v},
				edit.KV{Path: "model", Value: v},
			); err != nil {
				return err
			}
			return relink()
		}
		if routed() {
			keys := make([]string, len(claudeEnv))
			for i, k := range claudeEnv {
				keys[i] = "env." + k
			}
			if err := edit.DelJSON(path, keys...); err != nil {
				return err
			}
			forget(tierFlags...)
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

	// tier is one of the opus/sonnet/haiku/fable aliases. It is listed only
	// once picked; clearing it returns it to following the main model.
	tier := func(t claudeTier) Field {
		return Field{
			Key: t.key, Label: t.key, Optional: true,
			Get: func() string {
				if !routed() || !tierPicked(t.key) {
					return ""
				}
				return env(t.env)
			},
			Set: func(v string) error {
				if v == "" {
					if !routed() {
						return nil
					}
					forget(claudeTierFlag(t.key))
					return relink()
				}
				if !isMagpie(v) {
					return fmt.Errorf("%s: %q is not a model magpie provides; use provider/model", t.key, v)
				}
				if !routed() || env("ANTHROPIC_MODEL") == "" {
					return fmt.Errorf("pick Claude Code's main model through magpie first: magpie claude <provider/model>")
				}
				stash(map[string]string{claudeTierFlag(t.key): "1"})
				if err := edit.SetJSON(path, edit.KV{Path: "env." + t.env, Value: v}); err != nil {
					return err
				}
				return relink()
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
		fields = append(fields, tier(t))
	}

	return &Agent{
		ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Aliases: []string{"cc", "claude-code"},
		Bin: "claude", Dir: filepath.Dir(path), Path: path,
		Fields: fields,
	}
}
