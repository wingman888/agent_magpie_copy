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
// ANTHROPIC_MODEL (and the aliases opus/sonnet/haiku resolve through) is
// all it takes to run it on any provider.

// env vars magpie sets while routing through the gateway.
var claudeEnv = []string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL",
}

// claudeTiers are the model tiers Claude Code exposes besides the main model,
// each with the env var it reads. A tier that is not picked on its own follows
// the main model and holds the same value. haiku also decides the small, fast model.
var claudeTiers = []struct{ name, env string }{
	{"opus", "ANTHROPIC_DEFAULT_OPUS_MODEL"},
	{"sonnet", "ANTHROPIC_DEFAULT_SONNET_MODEL"},
	{"haiku", "ANTHROPIC_DEFAULT_HAIKU_MODEL"},
	{"fable", "ANTHROPIC_DEFAULT_FABLE_MODEL"},
}

// claudeHaiku is the index of the haiku tier in claudeTiers.
const claudeHaiku = 2

// claudeTierPicks reads which tiers were picked on their own. An empty entry
// is a tier that follows main. Without a main model every tier follows.
func claudeTierPicks(env func(string) string, main string) []string {
	out := make([]string, len(claudeTiers))
	if main == "" {
		return out
	}
	for i, t := range claudeTiers {
		if v := env(t.env); v != "" && v != main {
			out[i] = v
		}
	}
	return out
}

// writeClaudeTiers stores the extra keys, the main model and every tier. An
// empty pick follows main. CLAUDE_CODE_SUBAGENT_MODEL is written only while
// every tier equals main, so a tier picked on its own is not overridden.
func writeClaudeTiers(path, main string, picks []string, extra ...edit.KV) error {
	kvs := append([]edit.KV{}, extra...)
	same := true
	for i, t := range claudeTiers {
		v := picks[i]
		if v == "" {
			v = main
		}
		if v != main {
			same = false
		}
		kvs = append(kvs, edit.KV{Path: "env." + t.env, Value: v})
		if i == claudeHaiku {
			kvs = append(kvs, edit.KV{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: v})
		}
	}
	if same {
		kvs = append(kvs, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: main})
	} else if err := edit.DelJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL"); err != nil {
		return err
	}
	return edit.SetJSON(path, kvs...)
}

func claude(home string) *Agent {
	path := filepath.Join(home, ".claude", "settings.json")
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	model := jsonGet(path, "model")
	routed := func() bool { return env("ANTHROPIC_BASE_URL") == gateway.URL() }
	// the main model is set while routed through the gateway
	mainSet := func() bool { return routed() && env("ANTHROPIC_MODEL") != "" }

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
			// tiers picked on their own keep their model; the others follow v
			prev := ""
			if routed() {
				prev = env("ANTHROPIC_MODEL")
			}
			return writeClaudeTiers(path, v, claudeTierPicks(env, prev),
				edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: gateway.URL()},
				edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: gateway.Token},
				edit.KV{Path: "env.ANTHROPIC_MODEL", Value: v},
				edit.KV{Path: "model", Value: v},
			)
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

	// setTier picks a tier's model on its own; "" makes it follow the main model again.
	setTier := func(i int, v string) error {
		if !mainSet() {
			if v == "" {
				return nil
			}
			return errors.New("Claude Code is not routed through magpie yet; set its main model first: magpie claude <provider/model>")
		}
		if v != "" && !isMagpie(v) {
			return fmt.Errorf("%s is not a model magpie provides; use provider/model, see magpie models", v)
		}
		main := env("ANTHROPIC_MODEL")
		picks := claudeTierPicks(env, main)
		picks[i] = v
		return writeClaudeTiers(path, main, picks)
	}

	options := func(map[string]string) []Option {
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

	fields := []Field{{
		Key: "model", Label: "model",
		Get:     get,
		Set:     set,
		Options: options,
	}}
	for i := range claudeTiers {
		i := i
		fields = append(fields, Field{
			Key: claudeTiers[i].name, Label: claudeTiers[i].name, Optional: true,
			// a tier shows only while it is picked on its own
			Get: func() string {
				if !mainSet() {
					return ""
				}
				return claudeTierPicks(env, env("ANTHROPIC_MODEL"))[i]
			},
			Set:     func(v string) error { return setTier(i, v) },
			Options: options,
		})
	}

	return &Agent{
		ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Aliases: []string{"cc", "claude-code"},
		Bin: "claude", Dir: filepath.Dir(path), Path: path,
		Fields: fields,
	}
}
