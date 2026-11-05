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

// claudeTier is one model tier: the alias Claude Code resolves through, and
// the environment variable that names its model.
type claudeTier struct{ key, env string }

var claudeTiers = []claudeTier{
	{"opus", "ANTHROPIC_DEFAULT_OPUS_MODEL"},
	{"sonnet", "ANTHROPIC_DEFAULT_SONNET_MODEL"},
	{"haiku", "ANTHROPIC_DEFAULT_HAIKU_MODEL"},
	{"fable", "ANTHROPIC_DEFAULT_FABLE_MODEL"},
}

// env vars magpie sets while routing through the gateway.
var claudeEnv = []string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
}

// claudePin is the stash key of a tier set on its own. A tier without a pin
// follows the main model; the pin keeps its value when the main model changes.
func claudePin(key string) string { return "claude.pin." + key }

func claudePinKeys() []string {
	keys := make([]string, 0, len(claudeTiers))
	for _, t := range claudeTiers {
		keys = append(keys, claudePin(t.key))
	}
	return keys
}

// claudeTierKVs returns the tier env values for a main model, the small fast
// model (which is the haiku tier), and the shared subagent model when every
// tier is the main model. unified reports whether that shared model applies;
// when it does not, CLAUDE_CODE_SUBAGENT_MODEL must be removed.
func claudeTierKVs(main string, pins map[string]string) (kvs []edit.KV, unified bool) {
	vals := map[string]string{}
	unified = true
	for _, t := range claudeTiers {
		v := main
		if p := pins[claudePin(t.key)]; p != "" {
			v = p
		}
		vals[t.key] = v
		if v != main {
			unified = false
		}
		kvs = append(kvs, edit.KV{Path: "env." + t.env, Value: v})
	}
	kvs = append(kvs, edit.KV{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: vals["haiku"]})
	if unified {
		kvs = append(kvs, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: main})
	}
	return kvs, unified
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

	// sync rewrites every tier, the small fast model and the subagent model
	// from the current main model and pins.
	sync := func() error {
		kvs, unified := claudeTierKVs(env("ANTHROPIC_MODEL"), stashLoad())
		if err := edit.SetJSON(path, kvs...); err != nil {
			return err
		}
		if unified {
			return nil
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
			forget(append([]string{"claude.model", "claude.base_url", "claude.auth_token"}, claudePinKeys()...)...)
			return edit.DelJSON(path, keys...)
		}
		if isMagpie(v) {
			if !routed() {
				forget(claudePinKeys()...)
				stash(map[string]string{
					"claude.model":      model(),
					"claude.base_url":   env("ANTHROPIC_BASE_URL"),
					"claude.auth_token": env("ANTHROPIC_AUTH_TOKEN"),
				})
			}
			tiers, unified := claudeTierKVs(v, stashLoad())
			kvs := []edit.KV{
				{Path: "env.ANTHROPIC_BASE_URL", Value: gateway.URL()},
				{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: gateway.Token},
				{Path: "env.ANTHROPIC_MODEL", Value: v},
			}
			kvs = append(kvs, tiers...)
			kvs = append(kvs, edit.KV{Path: "model", Value: v})
			if err := edit.SetJSON(path, kvs...); err != nil {
				return err
			}
			if unified {
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
			forget(claudePinKeys()...)
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

	// tier builds the field for one model tier. Unset, it follows the main
	// model; setting it pins the tier, and clearing it puts it back to following.
	tier := func(t claudeTier) Field {
		return Field{
			Key: t.key, Label: t.key, Extra: true,
			Get: func() string {
				if !routed() || stashLoad()[claudePin(t.key)] == "" {
					return ""
				}
				return env(t.env)
			},
			Set: func(v string) error {
				if v == "" {
					forget(claudePin(t.key))
					if !routed() || env("ANTHROPIC_MODEL") == "" {
						return nil
					}
					return sync()
				}
				if !routed() || env("ANTHROPIC_MODEL") == "" {
					return fmt.Errorf("choose a main model through magpie first: magpie claude <provider/model>")
				}
				if !isMagpie(v) {
					return fmt.Errorf("%q is not a magpie model (expected provider/model)", v)
				}
				stash(map[string]string{claudePin(t.key): v})
				return sync()
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
