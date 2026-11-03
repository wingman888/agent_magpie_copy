package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestNormalizeModelKeySameModel(t *testing.T) {
	want := "claude-opus-5-5"
	for _, id := range []string{
		"claude-opus-5-5",
		"anthropic/claude-opus-5.5",
		"Claude-Opus-5.5",
		"claude-opus-5-5-20260801",
		"anthropic/Claude-Opus-5.5-20260801",
		"claude-opus-5.5",
	} {
		if got := NormalizeModelKey(id); got != want {
			t.Errorf("NormalizeModelKey(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestNormalizeModelKeyRules(t *testing.T) {
	// A variant (":batch") is a different model, so it is kept as it is;
	// a dot not between digits stays; every version dot becomes a hyphen,
	// overlapping ones too; only an 8-digit date is dropped.
	cases := map[string]string{
		"x:batch":               "x:batch",
		"x":                     "x",
		"v1.beta":               "v1.beta",
		"gpt-4.1.2":             "gpt-4-1-2",
		"gpt-5.5-123456789":     "gpt-5-5-123456789",
		"model-20260801":        "model",
		"Claude-Opus-5.5:BATCH": "claude-opus-5-5:batch",
		"vendor/model:batch":    "model:batch",
	}
	for id, want := range cases {
		if got := NormalizeModelKey(id); got != want {
			t.Errorf("NormalizeModelKey(%q) = %q, want %q", id, got, want)
		}
	}
	if NormalizeModelKey("x:batch") == NormalizeModelKey("x") {
		t.Error("x:batch must not normalize to the same key as x")
	}
}

func TestOfficialModelNamePrefersRealName(t *testing.T) {
	ms := []catalog.Model{
		{ID: "claude-opus-5-5", Name: "claude-opus-5-5"},
		{ID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5"},
	}
	if got := OfficialModelName(ms); got != "Claude Opus 5.5" {
		t.Errorf("OfficialModelName = %q, want the display name", got)
	}
	if got := OfficialModelName([]catalog.Model{{ID: "only-id"}}); got != "only-id" {
		t.Errorf("OfficialModelName without a name = %q, want the id", got)
	}
	if got := OfficialModelName(nil); got != "" {
		t.Errorf("OfficialModelName(nil) = %q, want empty", got)
	}
}
