package provider

import (
	"strings"
	"testing"
)

// The preset id is matched case-insensitively, as `provider add` does.
func TestImportFixPresetIDCaseInsensitive(t *testing.T) {
	p := impTestMustParse(t, "magpie://import?preset=DeepSeek&key=sk-fix")
	if p.ID != "deepseek" || p.Preset != "deepseek" || p.Key != "sk-fix" {
		t.Fatalf("got id=%q preset=%q key=%q", p.ID, p.Preset, p.Key)
	}
}

// Ids the agent accounts use are refused before anything is saved, whether
// given explicitly or derived from a name.
func TestImportFixAccountIDsRefused(t *testing.T) {
	for _, link := range []string{
		"magpie://import?name=x&chat=https://relay.example/v1&id=claude",
		"magpie://import?name=x&chat=https://relay.example/v1&id=codex",
		"magpie://import?name=x&chat=https://relay.example/v1&id=copilot",
		"magpie://import?name=Claude&chat=https://relay.example/v1",
		"magpie://import?preset=deepseek&id=Claude",
	} {
		_, err := ParseImport(link)
		if err == nil {
			t.Fatalf("ParseImport(%q) accepted an account id", link)
		}
		if !strings.Contains(err.Error(), "reserved by a logged-in agent account") {
			t.Fatalf("ParseImport(%q) error = %v", link, err)
		}
	}
}

// Ids that only look similar to an account id are still allowed.
func TestImportFixNearAccountIDsAccepted(t *testing.T) {
	impTestMustParse(t, "magpie://import?name=Claude%20Relay&chat=https://relay.example/v1")
	impTestMustParse(t, "magpie://import?name=x&chat=https://relay.example/v1&id=claude-relay")
}
