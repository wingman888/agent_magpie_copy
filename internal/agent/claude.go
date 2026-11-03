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

// claudeTier is one model alias Claude Code resolves through its env block.
type claudeTier struct {
	name string // opus, sonnet, haiku, fable: also the field key
	env  string // the ANTHROPIC_DEFAULT_<NAME>_MODEL variable
}

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

// tierKey is the stash key holding a tier the user pinned explicitly. A tier
// with no entry follows the main model.
func tierKey(name string) string { return "claude.tier." + name }

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

	// apply writes every magpie-owned variable for main: the tiers that follow
	// it get main, the pinned ones keep their own model. The unified subagent
	// model is only set while every tier resolves to main.
	apply := func(main string) error {
		pinned := stashLoad()
		kvs := []edit.KV{
			{Path: "env.ANTHROPIC_BASE_URL", Value: gateway.URL()},
			{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: gateway.Token},
			{Path: "env.ANTHROPIC_MODEL", Value: main},
		}
		unified := true
		var haiku string
		for _, t := range claudeTiers {
			v := pinned[tierKey(t.name)]
			if v == "" {
				v = main
			}
			if v != main {
				unified = false
			}
			if t.name == "haiku" {
				haiku = v
			}
			kvs = append(kvs, edit.KV{Path: "env." + t.env, Value: v})
		}
		kvs = append(kvs,
			edit.KV{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: haiku},
			edit.KV{Path: "model", Value: main},
		)
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

	set := func(v string) error {
		if v == "" {
			// Claude Code as installed: Anthropic's own endpoint and model
			keys := []string{"model"}
			for _, k := range claudeEnv {
				keys = append(keys, "env."+k)
			}
			forget("claude.model", "claude.base_url", "claude.auth_token")
			for _, t := range claudeTiers {
				forget(tierKey(t.name))
			}
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
			return apply(v)
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
			for _, t := range claudeTiers {
				forget(tierKey(t.name))
			}
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

	// Only the catalog's models; Claude Code's own short aliases are
	// not something any API lists, and a compiled-in copy would just
	// go stale.
	mainField := Field{
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
			return append(group(name, own), viaMagpie("")...)
		},
	}

	fields := []Field{mainField}
	for _, t := range claudeTiers {
		t := t
		key := tierKey(t.name)
		fields = append(fields, Field{
			Key: t.name, Label: t.name,
			Optional: true,
			Get: func() string {
				if !routed() {
					return ""
				}
				return stashLoad()[key]
			},
			Set: func(v string) error {
				if !routed() || env("ANTHROPIC_MODEL") == "" {
					if v == "" {
						return nil // nothing pinned, nothing to clear
					}
					return fmt.Errorf("pick a magpie model for Claude Code first: magpie claude <provider/model>")
				}
				main := env("ANTHROPIC_MODEL")
				if v == "" {
					stash(map[string]string{key: ""})
					return apply(main)
				}
				if !isMagpie(v) {
					return fmt.Errorf("%q is not a model magpie provides; use provider/model", v)
				}
				stash(map[string]string{key: v})
				return apply(main)
			},
			Options: func(map[string]string) []Option {
				return group("Claude Code", viaMagpie(""))
			},
		})
	}

	return &Agent{
		ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Aliases: []string{"cc", "claude-code"},
		Bin: "claude", Dir: filepath.Dir(path), Path: path,
		Fields: fields,
	}
}
