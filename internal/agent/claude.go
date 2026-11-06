package agent

import (
	"errors"
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
//
// A tier follows the main model until it is set on its own; from then on its
// env var keeps that model when the main model changes.

// claudeTierL3x is one model slot Claude Code reads.
type claudeTierL3x struct{ key, env string }

// claudeTiersL3x, in the order magpie lists them. haiku also serves as Claude
// Code's small, fast model.
var claudeTiersL3x = []claudeTierL3x{
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

	// pinned is the model a tier was set to on its own, "" while it follows
	// the main model.
	pinned := func(t claudeTierL3x) string {
		if !routed() {
			return ""
		}
		v := env(t.env)
		if v == "" || v == env("ANTHROPIC_MODEL") {
			return ""
		}
		return v
	}

	// writeTiers stores every tier for the main model. pick gives each tier's
	// own model, "" to follow main. The one subagent model is set only while
	// every tier is main; otherwise subagents follow their own tier.
	writeTiers := func(main string, pick func(claudeTierL3x) string) error {
		var kvs []edit.KV
		small, uniform := main, true
		for _, t := range claudeTiersL3x {
			v := pick(t)
			if v == "" {
				v = main
			}
			if v != main {
				uniform = false
			}
			kvs = append(kvs, edit.KV{Path: "env." + t.env, Value: v})
			if t.key == "haiku" {
				small = v
			}
		}
		kvs = append(kvs, edit.KV{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: small})
		if uniform {
			return edit.SetJSON(path, append(kvs, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: main})...)
		}
		if err := edit.DelJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL"); err != nil {
			return err
		}
		return edit.SetJSON(path, kvs...)
	}

	modelOptions := func(map[string]string) []Option {
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
			// tiers that followed the old main model follow the new one;
			// those set on their own stay as they are
			old := ""
			if wasRouted {
				old = env("ANTHROPIC_MODEL")
			}
			picks := map[string]string{}
			if wasRouted {
				for _, t := range claudeTiersL3x {
					if cur := env(t.env); cur != old {
						picks[t.key] = cur
					}
				}
			}
			if err := edit.SetJSON(path,
				edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: gateway.URL()},
				edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: gateway.Token},
				edit.KV{Path: "env.ANTHROPIC_MODEL", Value: v},
				edit.KV{Path: "model", Value: v},
			); err != nil {
				return err
			}
			return writeTiers(v, func(t claudeTierL3x) string { return picks[t.key] })
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

	// tier is one slot: `magpie claude haiku <provider/model>` pins it, and
	// `magpie claude haiku ""` puts it back to following the main model.
	tier := func(t claudeTierL3x) Field {
		return Field{
			Key: t.key, Label: t.key, Named: true,
			Get: func() string { return pinned(t) },
			Set: func(v string) error {
				main := env("ANTHROPIC_MODEL")
				if !routed() || main == "" {
					if v == "" {
						return nil
					}
					return errors.New("set Claude Code's main model first, a model magpie provides: magpie claude <provider/model>")
				}
				if v != "" && !isMagpie(v) {
					return fmt.Errorf("%q is not a model magpie provides; use provider/model", v)
				}
				return writeTiers(main, func(x claudeTierL3x) string {
					if x.key == t.key {
						return v
					}
					return pinned(x)
				})
			},
			Options: modelOptions,
		}
	}

	fields := []Field{{Key: "model", Label: "model", Get: get, Set: set, Options: modelOptions}}
	for _, t := range claudeTiersL3x {
		fields = append(fields, tier(t))
	}
	return &Agent{
		ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Aliases: []string{"cc", "claude-code"},
		Bin: "claude", Dir: filepath.Dir(path), Path: path,
		Fields: fields,
	}
}
