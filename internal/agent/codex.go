package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// Codex talks the OpenAI Responses API. Signed in (ChatGPT or an API key),
// it is routed through magpie with `openai_base_url` alone: Codex keeps its
// built-in OpenAI provider and sign-in, so its own models, its threads
// (listed per provider) and the Codex app's model picker stay as they are,
// and magpie's gateway passes its own models through to OpenAI while it
// answers the rest — the model list included, so magpie's models join
// OpenAI's in /model. Not signed in, the built-in provider can't run, and
// magpie is a provider of its own: a [model_providers.magpie] table,
// `model_provider = "magpie"`, and a model catalog file for /model.

func codex(home string) *Agent {
	dir := filepath.Join(home, ".codex")
	path := filepath.Join(dir, "config.toml")
	catalogPath := filepath.Join(dir, "magpie-models.json")
	get := func(k string) string { v, _ := edit.GetTOMLTop(path, k); return v }
	asProvider := func() bool { return get("model_provider") == magpieID }
	viaBase := func() bool { return isCodexGateway(get("openai_base_url")) }
	routed := func() bool { return asProvider() || viaBase() }
	models := func() []catalog.Model {
		switch {
		case asProvider():
			return magpieModels()
		case viaBase():
			return append(catalog.Codex(), magpieModels()...)
		}
		return catalog.Codex()
	}
	// keep the effort valid for the model; a fresh model gets its default.
	settle := func() error {
		ms := models()
		model, effort := get("model"), get("model_reasoning_effort")
		if e := catalog.Efforts(ms, model); len(e) > 0 && !contains(e, effort) {
			return edit.SetTOMLTop(path, edit.KV{Path: "model_reasoning_effort", Value: codexcat.DefaultEffort(e)})
		}
		return nil
	}
	// dropProvider takes magpie out as a provider of Codex's.
	dropProvider := func() error {
		if !asProvider() {
			return nil
		}
		if err := edit.DelTOMLTop(path, "model_provider", "model_catalog_json"); err != nil {
			return err
		}
		if err := edit.DelTOMLTable(path, "model_providers."+magpieID); err != nil {
			return err
		}
		os.Remove(catalogPath)
		return nil
	}
	dropBase := func() error {
		if !viaBase() {
			return nil
		}
		return edit.DelTOMLTop(path, "openai_base_url")
	}
	set := func(v string) error {
		if v == "" {
			// Codex as installed: OpenAI, its own catalog, its default model
			if err := dropBase(); err != nil {
				return err
			}
			if err := edit.DelTOMLTop(path, "model", "model_provider", "model_catalog_json"); err != nil {
				return err
			}
			if err := edit.DelTOMLTable(path, "model_providers."+magpieID); err != nil {
				return err
			}
			os.Remove(catalogPath)
			forget("codex.model", "codex.effort", "codex.provider", "codex.catalog")
			return nil
		}
		if isMagpie(v) {
			if !routed() {
				stash(map[string]string{"codex.model": get("model"), "codex.effort": get("model_reasoning_effort"),
					"codex.provider": get("model_provider"), "codex.catalog": get("model_catalog_json")})
			}
			if codexSignedIn(dir) {
				if err := dropProvider(); err != nil {
					return err
				}
				// the base URL is the built-in provider's, and a catalog
				// file would stand in for the list magpie hands out
				if err := edit.DelTOMLTop(path, "model_provider", "model_catalog_json"); err != nil {
					return err
				}
				if err := edit.SetTOMLTop(path,
					edit.KV{Path: "openai_base_url", Value: codexGatewayURL()},
					edit.KV{Path: "model", Value: v},
				); err != nil {
					return err
				}
				return settle()
			}
			if err := dropBase(); err != nil {
				return err
			}
			if err := edit.SetTOMLTable(path, "model_providers."+magpieID,
				edit.KV{Path: "name", Value: "magpie"},
				edit.KV{Path: "base_url", Value: gatewayV1()},
				edit.KV{Path: "wire_api", Value: "responses"},
				edit.KV{Path: "experimental_bearer_token", Value: gateway.Token},
			); err != nil {
				return err
			}
			if err := edit.WriteAtomic(catalogPath, codexcat.Catalog(magpieModels())); err != nil {
				return err
			}
			if err := edit.SetTOMLTop(path,
				edit.KV{Path: "model_provider", Value: magpieID},
				edit.KV{Path: "model_catalog_json", Value: catalogPath},
				edit.KV{Path: "model", Value: v},
			); err != nil {
				return err
			}
			return settle()
		}
		if routed() {
			if err := dropBase(); err != nil {
				return err
			}
			if err := dropProvider(); err != nil {
				return err
			}
			unstash("codex.model")
			var back []edit.KV
			if p := unstash("codex.provider"); p != "" && p != magpieID {
				back = append(back, edit.KV{Path: "model_provider", Value: p})
			}
			if c := unstash("codex.catalog"); c != "" && c != catalogPath {
				back = append(back, edit.KV{Path: "model_catalog_json", Value: c})
			}
			if e := unstash("codex.effort"); e != "" {
				back = append(back, edit.KV{Path: "model_reasoning_effort", Value: e})
			}
			if len(back) > 0 {
				if err := edit.SetTOMLTop(path, back...); err != nil {
					return err
				}
			}
		}
		if err := edit.SetTOMLTop(path, edit.KV{Path: "model", Value: v}); err != nil {
			return err
		}
		return settle()
	}

	return &Agent{
		ID: "codex", Name: "Codex", Icon: "codex-color", Bin: "codex", Dir: dir, Path: path,
		// the app-server behind the Codex app (and every codex TUI) builds
		// its model list once, at start-up.
		Notice: func() string {
			if Running(`(^|/)codex( |$)`) {
				return "Codex builds its model list at start-up — restart the Codex app (and open codex sessions) to see this."
			}
			return ""
		},
		Fields: []Field{
			{
				Key: "model", Label: "model",
				Get: func() string { return get("model") },
				Set: set,
				Options: func(map[string]string) []Option {
					var own []Option
					if p := get("model_provider"); p != "" && p != magpieID {
						own = group(p, options(catalog.Codex(), ""))
					} else {
						own = group("OpenAI", options(ownCodex(), ""))
					}
					return append(own, viaMagpieFor("codex", "")...)
				},
			},
			{
				Key: "effort", Label: "effort",
				Get: func() string { return get("model_reasoning_effort") },
				Set: func(v string) error {
					if v == "" {
						return edit.DelTOMLTop(path, "model_reasoning_effort")
					}
					return edit.SetTOMLTop(path, edit.KV{Path: "model_reasoning_effort", Value: v})
				},
				Options: func(cur map[string]string) []Option {
					if e := catalog.Efforts(models(), cur["model"]); len(e) > 0 {
						return static(e...)
					}
					return static("low", "medium", "high", "xhigh")
				},
			},
		},
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ownCodex is Codex's own models, narrowed to the ones ticked on its ChatGPT
// subscription in magpie when any are.
func ownCodex() []catalog.Model {
	ms := catalog.Codex()
	p, err := provider.Find("codex")
	if err != nil || len(p.Models) == 0 {
		return ms
	}
	var out []catalog.Model
	for _, m := range ms {
		if slices.Contains(p.Models, m.ID) {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return ms
	}
	return out
}

// codexGatewayURL is where Codex's built-in OpenAI provider is pointed to
// reach magpie.
func codexGatewayURL() string { return gateway.URL() + gateway.CodexPath }

// isCodexGateway reports whether an openai_base_url is magpie's, on
// whichever port it listened on then.
func isCodexGateway(u string) bool {
	return strings.HasPrefix(u, "http://127.0.0.1:") && strings.HasSuffix(strings.TrimSuffix(u, "/"), gateway.CodexPath)
}

// codexSignedIn reports whether Codex has a sign-in of its own, a ChatGPT
// account or an API key, which its built-in OpenAI provider needs.
func codexSignedIn(dir string) bool {
	var a struct {
		Key    string `json:"OPENAI_API_KEY"`
		Tokens struct {
			Access string `json:"access_token"`
		} `json:"tokens"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil || json.Unmarshal(b, &a) != nil {
		return false
	}
	return a.Key != "" || a.Tokens.Access != ""
}
