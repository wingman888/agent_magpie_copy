// Package codexcat renders models the way Codex describes them: the entries
// of its models.json, for `model_catalog_json` and for the model list magpie
// hands Codex in place of the ChatGPT backend's.
package codexcat

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// Prompt is Codex's generic system prompt (Apache-2.0, openai/codex,
// core/gpt-5.2-codex_prompt.md). Third-party models need one because the
// bundled catalog only carries prompts for OpenAI models.
//
//go:embed codex_prompt.md
var Prompt string

// DefaultEffort picks the middle of the road: "medium" or "high" when
// offered, else whatever the list starts with.
func DefaultEffort(e []string) string {
	for _, want := range []string{"medium", "high"} {
		if slices.Contains(e, want) {
			return want
		}
	}
	return e[0]
}

// Catalog renders models as a whole models.json.
func Catalog(ms []catalog.Model) []byte {
	out := struct {
		Models []any `json:"models"`
	}{Models: Entries(ms, 0)}
	if out.Models == nil {
		out.Models = []any{}
	}
	b, _ := json.MarshalIndent(out, "", " ")
	return b
}

// Entries renders models as models.json entries, ranked after the first
// `after`. Only fields Codex requires or that change behaviour are set; the
// rest take Codex's defaults.
func Entries(ms []catalog.Model, after int) []any {
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
	own := CacheEntries()
	var entries []any
	for i, m := range ms {
		if raw, ok := own[strings.TrimPrefix(m.ID, "codex/")]; ok && strings.HasPrefix(m.ID, "codex/") {
			entries = append(entries, ownEntry(raw, m.ID, m.Name, after+i+1))
			continue
		}
		e := model{
			Slug: m.ID, DisplayName: m.Name, Description: m.Name + " via magpie",
			Instructions: Prompt, Efforts: []level{},
			Shell: "unified_exec", Visibility: "list", InAPI: true, Priority: after + i + 1,
			ApplyPatch: "freeform", Tools: []string{}, Modalities: []string{"text"},
		}
		e.Truncation.Mode, e.Truncation.Limit = "tokens", 10000
		for _, ef := range m.Efforts {
			e.Efforts = append(e.Efforts, level{Effort: ef})
		}
		if len(m.Efforts) > 0 {
			d := DefaultEffort(m.Efforts)
			e.DefaultEffort = &d
		}
		entries = append(entries, e)
	}
	return entries
}

// CacheEntries is Codex's own models, as models_cache.json describes them
// for the ChatGPT account it last asked with, by slug.
func CacheEntries() map[string]map[string]any {
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".codex", "models_cache.json"))
	if err != nil {
		return nil
	}
	var cache struct {
		Models []map[string]any `json:"models"`
	}
	if json.Unmarshal(b, &cache) != nil {
		return nil
	}
	out := map[string]map[string]any{}
	for _, m := range cache.Models {
		if slug, _ := m["slug"].(string); slug != "" {
			out[slug] = m
		}
	}
	return out
}

// ownEntry is one of Codex's own models, reached through magpie with the
// ChatGPT sign-in: its entry as Codex has it (images, context window, tools,
// instructions), under magpie's id. The start-up notice and the upgrade
// prompt are left out; they name slugs the catalog does not have.
func ownEntry(raw map[string]any, id, name string, priority int) map[string]any {
	e := make(map[string]any, len(raw))
	for k, v := range raw {
		e[k] = v
	}
	delete(e, "availability_nux")
	delete(e, "upgrade")
	e["slug"], e["display_name"], e["priority"], e["visibility"] = id, name, priority, "list"
	if s, _ := e["base_instructions"].(string); s == "" {
		e["base_instructions"] = Prompt
	}
	return e
}
