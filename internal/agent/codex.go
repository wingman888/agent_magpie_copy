package agent

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/yetone/dial/internal/catalog"
	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
)

// Codex talks the OpenAI Responses API to whichever provider config.toml
// names. Routing it through dial means a [model_providers.dial] table,
// `model_provider = "dial"`, and a model catalog file so the whole dial
// catalog shows up inside Codex's own /model picker.

// codexPrompt is Codex's generic system prompt (Apache-2.0, openai/codex,
// core/gpt-5.2-codex_prompt.md). Third-party models need one because the
// bundled catalog only carries prompts for OpenAI models.
//
//go:embed codex_prompt.md
var codexPrompt string

func codex(home string) *Agent {
	dir := filepath.Join(home, ".codex")
	path := filepath.Join(dir, "config.toml")
	catalogPath := filepath.Join(dir, "dial-models.json")
	get := func(k string) string { v, _ := edit.GetTOMLTop(path, k); return v }
	routed := func() bool { return get("model_provider") == dialID }
	models := func() []catalog.Model {
		if routed() {
			return dialModels()
		}
		return catalog.Codex()
	}
	// keep the effort valid for the model; a fresh model gets its default.
	settle := func() error {
		ms := models()
		model, effort := get("model"), get("model_reasoning_effort")
		if e := catalog.Efforts(ms, model); len(e) > 0 && !contains(e, effort) {
			return edit.SetTOMLTop(path, edit.KV{Path: "model_reasoning_effort", Value: defaultEffort(e)})
		}
		return nil
	}
	set := func(v string) error {
		if v == "" {
			// Codex as installed: OpenAI, its own catalog, its default model
			if err := edit.DelTOMLTop(path, "model", "model_provider", "model_catalog_json"); err != nil {
				return err
			}
			if err := edit.DelTOMLTable(path, "model_providers."+dialID); err != nil {
				return err
			}
			os.Remove(catalogPath)
			forget("codex.model", "codex.effort", "codex.provider")
			return nil
		}
		if isDial(v) {
			if !routed() {
				stash(map[string]string{"codex.model": get("model"), "codex.effort": get("model_reasoning_effort"),
					"codex.provider": get("model_provider")})
			}
			if err := edit.SetTOMLTable(path, "model_providers."+dialID,
				edit.KV{Path: "name", Value: "dial"},
				edit.KV{Path: "base_url", Value: gatewayV1()},
				edit.KV{Path: "wire_api", Value: "responses"},
				edit.KV{Path: "experimental_bearer_token", Value: gateway.Token},
			); err != nil {
				return err
			}
			if err := edit.WriteAtomic(catalogPath, codexCatalog(dialModels())); err != nil {
				return err
			}
			if err := edit.SetTOMLTop(path,
				edit.KV{Path: "model_provider", Value: dialID},
				edit.KV{Path: "model_catalog_json", Value: catalogPath},
				edit.KV{Path: "model", Value: v},
			); err != nil {
				return err
			}
			return settle()
		}
		if routed() {
			if err := edit.DelTOMLTop(path, "model_provider", "model_catalog_json"); err != nil {
				return err
			}
			if err := edit.DelTOMLTable(path, "model_providers."+dialID); err != nil {
				return err
			}
			os.Remove(catalogPath)
			unstash("codex.model")
			var back []edit.KV
			if p := unstash("codex.provider"); p != "" {
				back = append(back, edit.KV{Path: "model_provider", Value: p})
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
					own := group("OpenAI", options(catalog.Codex(), ""))
					if p := get("model_provider"); p != "" && p != dialID {
						own = group(p, own)
					}
					return append(own, viaDialFor("codex", "")...)
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

// defaultEffort picks the middle of the road: "medium" or "high" when
// offered, else whatever the list starts with.
func defaultEffort(e []string) string {
	for _, want := range []string{"medium", "high"} {
		if contains(e, want) {
			return want
		}
	}
	return e[0]
}

// codexCatalog renders models in the shape of Codex's models.json so
// `model_catalog_json` can point at it. Only fields Codex requires or that
// change behaviour are set; the rest take Codex's defaults.
func codexCatalog(ms []catalog.Model) []byte {
	type level struct {
		Effort      string `json:"effort"`
		Description string `json:"description"`
	}
	type model struct {
		Slug          string  `json:"slug"`
		DisplayName   string  `json:"display_name"`
		Description   string  `json:"description"`
		Instructions  string  `json:"base_instructions"`
		DefaultEffort *string `json:"default_reasoning_level"`
		Efforts       []level `json:"supported_reasoning_levels"`
		Shell         string  `json:"shell_type"`
		Visibility    string  `json:"visibility"`
		InAPI         bool    `json:"supported_in_api"`
		Priority      int     `json:"priority"`
		Verbosity     bool    `json:"support_verbosity"`
		DefVerbosity  *string `json:"default_verbosity"`
		ApplyPatch    string  `json:"apply_patch_tool_type"`
		Truncation    struct {
			Mode  string `json:"mode"`
			Limit int    `json:"limit"`
		} `json:"truncation_policy"`
		Tools      []string `json:"experimental_supported_tools"`
		Modalities []string `json:"input_modalities"`
	}
	var out struct {
		Models []model `json:"models"`
	}
	for i, m := range ms {
		e := model{
			Slug: m.ID, DisplayName: m.Name, Description: m.Name + " via dial",
			Instructions: codexPrompt, Efforts: []level{},
			Shell: "unified_exec", Visibility: "list", InAPI: true, Priority: i + 1,
			ApplyPatch: "freeform", Tools: []string{}, Modalities: []string{"text"},
		}
		e.Truncation.Mode, e.Truncation.Limit = "tokens", 10000
		for _, ef := range m.Efforts {
			e.Efforts = append(e.Efforts, level{Effort: ef})
		}
		if len(m.Efforts) > 0 {
			d := defaultEffort(m.Efforts)
			e.DefaultEffort = &d
		}
		out.Models = append(out.Models, e)
	}
	b, _ := json.MarshalIndent(out, "", " ")
	return b
}
