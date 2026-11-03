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

// claudeTiers are the model slots Claude Code offers. Each has its own
// ANTHROPIC_DEFAULT_<TIER>_MODEL variable; haiku also serves as the small,
// fast model.
var claudeTiers = []string{"opus", "sonnet", "haiku", "fable"}

const subagentVar = "CLAUDE_CODE_SUBAGENT_MODEL"

func tierVar(tier string) string { return "ANTHROPIC_DEFAULT_" + strings.ToUpper(tier) + "_MODEL" }

// env vars magpie sets while routing through the gateway.
var claudeEnv = func() []string {
	keys := []string{
		"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
		"ANTHROPIC_SMALL_FAST_MODEL", subagentVar,
	}
	for _, t := range claudeTiers {
		keys = append(keys, tierVar(t))
	}
	return keys
}()

// pinKey is the stash key recording that a tier was chosen by hand. Without
// it a tier pinned to the same model as the main one could not be told apart
// from one that follows it.
func pinKey(tier string) string { return "claude.pin." + tier }

// tierWrites works out the per-tier variables for main model mainV. A tier
// in pinned keeps its model; every other tier follows mainV. The subagent
// override is written only while all tiers equal mainV; once any tier is set
// on its own it is removed so Claude Code picks per agent.
func tierWrites(mainV string, pinned map[string]string) ([]edit.KV, []string) {
	var kvs []edit.KV
	tiers := make(map[string]string, len(claudeTiers))
	unified := true
	for _, t := range claudeTiers {
		v := mainV
		if p, ok := pinned[t]; ok {
			v = p
		}
		tiers[t] = v
		if v != mainV {
			unified = false
		}
		kvs = append(kvs, edit.KV{Path: "env." + tierVar(t), Value: v})
	}
	kvs = append(kvs, edit.KV{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: tiers["haiku"]})
	if unified {
		return append(kvs, edit.KV{Path: "env." + subagentVar, Value: mainV}), nil
	}
	return kvs, []string{"env." + subagentVar}
}

func claude(home string) *Agent {
	path := filepath.Join(home, ".claude", "settings.json")
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	model := jsonGet(path, "model")
	routed := func() bool { return env("ANTHROPIC_BASE_URL") == gateway.URL() }
	mainModel := func() string { return env("ANTHROPIC_MODEL") }

	// the value shown: the catalog ref while routed, else Claude's own model.
	get := func() string {
		if routed() {
			if m := mainModel(); m != "" {
				return m
			}
		}
		return model()
	}

	// pinned returns the tiers set on their own, with their models. Only
	// meaningful while routed. Must be read before the main model changes.
	pinned := func() map[string]string {
		out := map[string]string{}
		if !routed() {
			return out
		}
		stashed := stashLoad()
		for _, t := range claudeTiers {
			v := env(tierVar(t))
			if v != "" && (v != mainModel() || stashed[pinKey(t)] != "") {
				out[t] = v
			}
		}
		return out
	}
	forgetPins := func() {
		keys := make([]string, 0, len(claudeTiers))
		for _, t := range claudeTiers {
			keys = append(keys, pinKey(t))
		}
		forget(keys...)
	}

	set := func(v string) error {
		if v == "" {
			// Claude Code as installed: Anthropic's own endpoint and model
			keys := []string{"model"}
			for _, k := range claudeEnv {
				keys = append(keys, "env."+k)
			}
			forget("claude.model", "claude.base_url", "claude.auth_token")
			forgetPins()
			return edit.DelJSON(path, keys...)
		}
		if isMagpie(v) {
			was := routed()
			prior := pinned()
			if !was {
				stash(map[string]string{
					"claude.model":      model(),
					"claude.base_url":   env("ANTHROPIC_BASE_URL"),
					"claude.auth_token": env("ANTHROPIC_AUTH_TOKEN"),
				})
				forgetPins()
				prior = map[string]string{}
			}
			tiers, del := tierWrites(v, prior)
			if err := edit.DelJSON(path, del...); err != nil {
				return err
			}
			kvs := []edit.KV{
				{Path: "env.ANTHROPIC_BASE_URL", Value: gateway.URL()},
				{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: gateway.Token},
				{Path: "env.ANTHROPIC_MODEL", Value: v},
			}
			kvs = append(kvs, tiers...)
			kvs = append(kvs, edit.KV{Path: "model", Value: v})
			return edit.SetJSON(path, kvs...)
		}
		if routed() {
			keys := make([]string, len(claudeEnv))
			for i, k := range claudeEnv {
				keys[i] = "env." + k
			}
			if err := edit.DelJSON(path, keys...); err != nil {
				return err
			}
			forgetPins()
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

	// setTier pins one tier to a magpie model, or with "" lets it follow the
	// main model again.
	setTier := func(tier string) func(string) error {
		return func(v string) error {
			if !routed() || mainModel() == "" {
				if v == "" {
					return nil
				}
				return fmt.Errorf("Claude Code is not using magpie yet; set its main model first: magpie claude <provider/model>")
			}
			if v != "" && !isMagpie(v) {
				return fmt.Errorf("%q is not a model magpie provides (use provider/model)", v)
			}
			pins := pinned()
			if v == "" {
				delete(pins, tier)
				forget(pinKey(tier))
			} else {
				pins[tier] = v
				stash(map[string]string{pinKey(tier): "1"})
			}
			kvs, del := tierWrites(mainModel(), pins)
			if err := edit.DelJSON(path, del...); err != nil {
				return err
			}
			return edit.SetJSON(path, kvs...)
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
		tier := t
		fields = append(fields, Field{
			Key: tier, Label: tier,
			// shown only while pinned; otherwise the tier is the main model
			Get: func() string {
				if routed() {
					if v, ok := pinned()[tier]; ok {
						return v
					}
				}
				return ""
			},
			Set:      setTier(tier),
			Optional: true,
			Options: func(map[string]string) []Option {
				return viaMagpie("")
			},
		})
	}

	return &Agent{
		ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Aliases: []string{"cc", "claude-code"},
		Bin: "claude", Dir: filepath.Dir(path), Path: path,
		Fields: fields,
	}
}
