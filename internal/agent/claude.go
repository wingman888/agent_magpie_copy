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

// claudeTier is one model tier Claude Code lets a user name by hand. A tier
// the user has not named follows the main model.
type claudeTier struct{ key, env string }

var claudeTiers = []claudeTier{
	{"opus", "ANTHROPIC_DEFAULT_OPUS_MODEL"},
	{"sonnet", "ANTHROPIC_DEFAULT_SONNET_MODEL"},
	{"haiku", "ANTHROPIC_DEFAULT_HAIKU_MODEL"},
	{"fable", "ANTHROPIC_DEFAULT_FABLE_MODEL"},
}

// claudePinPrefix names the stash entries holding a tier the user set by hand.
// They live in magpie's own stash, not in Claude's settings, so a tier that
// happens to equal the main model still counts as chosen.
const claudePinPrefix = "claude.tier."

// env vars magpie sets while routing through the gateway.
var claudeEnv = func() []string {
	env := []string{
		"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
	}
	for _, t := range claudeTiers {
		env = append(env, t.env)
	}
	return append(env, "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL")
}()

// claudePins returns the tiers the user named by hand, keyed by tier.
func claudePins() map[string]string {
	m := stashLoad()
	out := map[string]string{}
	for _, t := range claudeTiers {
		if v := m[claudePinPrefix+t.key]; v != "" {
			out[t.key] = v
		}
	}
	return out
}

// claudeForget drops every tier the user named by hand.
func claudeForget() {
	var keys []string
	for _, t := range claudeTiers {
		keys = append(keys, claudePinPrefix+t.key)
	}
	forget(keys...)
}

// claudeApply writes the tier variables for main model v: each tier is its
// pin when one is set, else v. Claude Code's shared subagent model is kept
// only while every tier equals v; ANTHROPIC_SMALL_FAST_MODEL follows haiku.
func claudeApply(path, v string) error {
	pins := claudePins()
	var kvs []edit.KV
	unified := true
	for _, t := range claudeTiers {
		val := v
		if p, ok := pins[t.key]; ok {
			val = p
		}
		if val != v {
			unified = false
		}
		kvs = append(kvs, edit.KV{Path: "env." + t.env, Value: val})
		if t.key == "haiku" {
			kvs = append(kvs, edit.KV{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: val})
		}
	}
	if err := edit.SetJSON(path, kvs...); err != nil {
		return err
	}
	if unified {
		return edit.SetJSON(path, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: v})
	}
	return edit.DelJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL")
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
			forget("claude.model", "claude.base_url", "claude.auth_token")
			claudeForget()
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
			return claudeApply(path, v)
		}
		if routed() {
			keys := make([]string, len(claudeEnv))
			for i, k := range claudeEnv {
				keys[i] = "env." + k
			}
			if err := edit.DelJSON(path, keys...); err != nil {
				return err
			}
			claudeForget()
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
		t := t
		fields = append(fields, Field{
			Key: t.key, Label: t.key, Optional: true,
			// the tier's own choice only; empty while it follows the main model
			Get: func() string { return claudePins()[t.key] },
			Set: func(v string) error {
				main := ""
				if routed() {
					main = env("ANTHROPIC_MODEL")
				}
				if v == "" {
					if main == "" {
						return nil
					}
					forget(claudePinPrefix + t.key)
					return claudeApply(path, main)
				}
				if main == "" {
					return fmt.Errorf("Claude Code is not using a magpie model yet; choose one first: magpie claude <provider/model>")
				}
				if !isMagpie(v) {
					return fmt.Errorf("%q is not a model magpie provides (expected provider/model)", v)
				}
				stash(map[string]string{claudePinPrefix + t.key: v})
				return claudeApply(path, main)
			},
			Options: func(map[string]string) []Option { return viaMagpie("") },
		})
	}

	return &Agent{
		ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Aliases: []string{"cc", "claude-code"},
		Bin: "claude", Dir: filepath.Dir(path), Path: path,
		Fields: fields,
	}
}
