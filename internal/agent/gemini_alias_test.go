package agent

import (
	"testing"

	"github.com/yetone/dial/internal/gateway"
)

// gemAliasLabelled returns the field whose display label is the given name,
// failing the test when no field or more than one field carries it.
func gemAliasLabelled(t *testing.T, a *Agent, label string) *Field {
	t.Helper()
	var hit *Field
	for i := range a.Fields {
		if a.Fields[i].Label == label {
			if hit != nil {
				t.Fatalf("label %q is not unique", label)
			}
			hit = &a.Fields[i]
		}
	}
	if hit == nil {
		t.Fatalf("no field labelled %q", label)
	}
	return hit
}

// TestGemAliasLabelMatchesKey: every display label resolves to the same
// field as its key.
func TestGemAliasLabelMatchesKey(t *testing.T) {
	a := gemini(t.TempDir())
	for _, f := range a.Fields {
		byKey := a.Field(f.Key)
		byLabel := a.Field(f.Label)
		if byKey == nil || byLabel == nil {
			t.Fatalf("field key %q label %q: lookup returned nil (key=%v label=%v)", f.Key, f.Label, byKey, byLabel)
		}
		if byKey != byLabel {
			t.Errorf("label %q resolves to %q, key %q resolves to %q", f.Label, byLabel.Key, f.Key, byKey.Key)
		}
	}
	if got := a.Field("auth"); got == nil || got.Key != "provider" {
		t.Errorf(`Field("auth") = %v, want the provider field`, got)
	}
	if got := a.Field("model"); got == nil || got.Key != "model" {
		t.Errorf(`Field("model") = %v, want the model field`, got)
	}
}

// TestGemAliasCaseAndSpace: label matching ignores case and surrounding space.
func TestGemAliasCaseAndSpace(t *testing.T) {
	a := gemini(t.TempDir())
	for _, name := range []string{"AUTH", " Auth ", "\tauth\n", "aUtH"} {
		got := a.Field(name)
		if got == nil || got.Key != "provider" {
			t.Errorf("Field(%q) = %v, want the provider field", name, got)
		}
	}
	for _, name := range []string{"Model", "  MODEL  "} {
		got := a.Field(name)
		if got == nil || got.Key != "model" {
			t.Errorf("Field(%q) = %v, want the model field", name, got)
		}
	}
}

// TestGemAliasUnknownStillNil: names matching neither key nor label, and
// blank names, still find nothing.
func TestGemAliasUnknownStillNil(t *testing.T) {
	a := gemini(t.TempDir())
	for _, name := range []string{"", "   ", "nope", "auths", "provide"} {
		if got := a.Field(name); got != nil {
			t.Errorf("Field(%q) = %q, want nil", name, got.Key)
		}
	}
}

// TestGemAliasKeyWins: a key match beats a label match on another field.
func TestGemAliasKeyWins(t *testing.T) {
	a := &Agent{Fields: []Field{
		{Key: "a", Label: "b"},
		{Key: "b", Label: "a"},
	}}
	if got := a.Field("a"); got == nil || got.Key != "a" {
		t.Errorf(`Field("a") = %v, want the field keyed "a"`, got)
	}
	if got := a.Field("b"); got == nil || got.Key != "b" {
		t.Errorf(`Field("b") = %v, want the field keyed "b"`, got)
	}
}

// TestGemAliasSetByLabel: setting through a display name has the same effect
// as setting through the key.
func TestGemAliasSetByLabel(t *testing.T) {
	a, settings, envPath := gemRouteEnv(t, "m1")
	f := gemAliasLabelled(t, a, "model")
	if err := a.Field(" MODEL ").Set("fake/m1"); err != nil {
		t.Fatalf("set by label: %v", err)
	}
	if f != a.Field("model") {
		t.Fatalf("label lookup returned a different field than the key")
	}
	gemRouteCheck(t, gemRouteFacts(settings, envPath), map[string]string{
		"base": gateway.URL(), "key": gateway.Token, "auth": "gemini-api-key", "model": "fake/m1",
	})
}
