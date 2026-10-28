package agent

import (
	"path/filepath"
	"strings"

	"github.com/yetone/dial/internal/catalog"
	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
)

// Claude Code reads its endpoint from the `env` block of settings.json.
// Pointing ANTHROPIC_BASE_URL at the gateway and naming a catalog model in
// ANTHROPIC_MODEL (and the aliases opus/sonnet/haiku resolve through) is
// all it takes to run it on any provider.

var claudeAliases = []Option{
	{Value: "opus", Note: "alias · latest Opus"},
	{Value: "sonnet", Note: "alias · latest Sonnet"},
	{Value: "haiku", Note: "alias · latest Haiku"},
	{Value: "opusplan", Note: "alias · Opus for planning, Sonnet for work"},
	{Value: "sonnet[1m]", Note: "alias · Sonnet with 1M context"},
}

// env vars dial sets while routing through the gateway.
var claudeEnv = []string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_SMALL_FAST_MODEL",
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
		if isDial(v) {
			if !routed() {
				stash(map[string]string{
					"claude.model":      model(),
					"claude.base_url":   env("ANTHROPIC_BASE_URL"),
					"claude.auth_token": env("ANTHROPIC_AUTH_TOKEN"),
				})
			}
			return edit.SetJSON(path,
				edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: gateway.URL()},
				edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: gateway.Token},
				edit.KV{Path: "env.ANTHROPIC_MODEL", Value: v},
				edit.KV{Path: "env.ANTHROPIC_DEFAULT_OPUS_MODEL", Value: v},
				edit.KV{Path: "env.ANTHROPIC_DEFAULT_SONNET_MODEL", Value: v},
				edit.KV{Path: "env.ANTHROPIC_DEFAULT_HAIKU_MODEL", Value: v},
				edit.KV{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: v},
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

	return &Agent{
		ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Aliases: []string{"cc", "claude-code"},
		Bin: "claude", Dir: filepath.Dir(path), Path: path,
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: get,
			Set: set,
			Options: func(map[string]string) []Option {
				var own []Option
				for _, m := range catalog.Provider("anthropic") {
					if strings.HasPrefix(m.ID, "claude") {
						own = append(own, Option{Value: m.ID, Note: m.Name})
					}
				}
				out := group("Claude Code", append(append([]Option{}, claudeAliases...), own...))
				if u := env("ANTHROPIC_BASE_URL"); u != "" && !routed() {
					out[0].Note += " · " + hostOf(u)
				}
				return append(out, viaDial("")...)
			},
		}},
	}
}
