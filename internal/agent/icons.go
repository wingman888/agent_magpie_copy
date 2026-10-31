package agent

import (
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// Every model in a picker carries its vendor's real logo. A models.dev
// provider id maps through the presets; a model id the presets do not
// cover is recognised by its family name. Anything else has no icon at
// all, never a made-up one.

// byFamily pairs a model id prefix with the vendor logo it belongs to.
var byFamily = []struct{ prefix, icon string }{
	{"claude", "claude-color"},
	{"gpt", "openai"}, {"o1", "openai"}, {"o3", "openai"}, {"o4", "openai"}, {"codex", "openai"},
	{"gemini", "gemini-color"}, {"gemma", "gemini-color"},
	{"deepseek", "deepseek-color"},
	{"grok", "xai"},
	{"kimi", "kimi"}, {"moonshot", "kimi"},
	{"glm", "zhipu-color"},
	{"qwen", "qwen-color"}, {"qwq", "qwen-color"},
	{"mistral", "mistral-color"}, {"codestral", "mistral-color"}, {"devstral", "mistral-color"}, {"magistral", "mistral-color"},
	{"minimax", "minimax-color"},
}

// modelIcon names the bundled logo for a model, given the models.dev
// provider it came from (may be empty) and its id.
func modelIcon(catalogID, modelID string) string {
	if catalogID != "" {
		if ic := provider.IconForCatalog(catalogID); ic != "" {
			return ic
		}
	}
	id := strings.ToLower(modelID)
	if _, rest, ok := strings.Cut(id, "/"); ok { // "vendor/model" on relays
		id = rest
	}
	for _, f := range byFamily {
		if strings.HasPrefix(id, f.prefix) {
			return f.icon
		}
	}
	return ""
}
