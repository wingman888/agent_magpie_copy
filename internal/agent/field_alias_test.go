package agent

import (
	"path/filepath"
	"testing"
)

// Display labels are accepted only where a field declares an Alias. Other
// agents' labels ("thinking" for pi, "large" for crush) must stay rejected,
// so their lookups behave exactly as before.
func TestFieldLabelsNotAcceptedOutsideAlias(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".config")

	p := pi(home)
	if p.Field("thinking") != nil {
		t.Error(`pi: Field("thinking") resolved; the label must not be accepted`)
	}
	if p.Field("effort") == nil {
		t.Error(`pi: Field("effort") (the key) no longer resolves`)
	}

	c := crush(home, cfg)
	if c.Field("large") != nil {
		t.Error(`crush: Field("large") resolved; the label must not be accepted`)
	}
	if c.Field("model") == nil {
		t.Error(`crush: Field("model") (the key) no longer resolves`)
	}
}

// Gemini's provider field is addressed as "auth" through its Alias.
func TestGeminiAuthAliasResolvesProvider(t *testing.T) {
	_, g := geminiEnv(t, "", "")
	f := g.Field("auth")
	if f == nil || f != g.Field("provider") {
		t.Fatal(`gemini: Field("auth") does not resolve to the provider field`)
	}
}
