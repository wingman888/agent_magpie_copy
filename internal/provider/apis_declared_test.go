package provider

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Provider.APIs answers from the vendor's last list: the declared protocols
// in Protocols' order, nil for an undeclared, unknown or unusable entry.
func TestApisDeclaredProtocols(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := catalog.SaveLive("apisdecl", "http://x", []catalog.Model{
		{ID: "apisdecl-gpt", APIs: []string{"responses"}},
		{ID: "apisdecl-both", APIs: []string{"anthropic", "chat", "gemini"}},
		{ID: "apisdecl-odd", APIs: []string{"gemini"}},
		{ID: "apisdecl-silent"},
	}); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "apisdecl"}
	for model, want := range map[string][]Protocol{
		"apisdecl-gpt":    {Responses},
		"apisdecl-both":   {Chat, Anthropic},
		"apisdecl-odd":    nil,
		"apisdecl-silent": nil,
		"apisdecl-absent": nil,
	} {
		if got := p.APIs(model); !reflect.DeepEqual(got, want) {
			t.Errorf("APIs(%q) = %v, want %v", model, got, want)
		}
	}
	if got := (Provider{ID: "apisdecl-nolist"}).APIs("apisdecl-gpt"); got != nil {
		t.Errorf("no list: APIs = %v, want nil", got)
	}
}

// Copilot declares chat, responses and anthropic, all under its one base.
func TestApisCopilotDeclaresThreeEndpoints(t *testing.T) {
	p := copilotProvider(copilotApp{User: "apisdecl"}, "individual")
	if !reflect.DeepEqual(p.Speaks(), []Protocol{Chat, Responses, Anthropic}) {
		t.Errorf("Speaks = %v", p.Speaks())
	}
	for _, pr := range p.Speaks() {
		if p.Base(pr) != copilotBase {
			t.Errorf("%s base = %q", pr, p.Base(pr))
		}
	}
}

// supported_endpoints maps to the catalog's API names; other endpoints are
// ignored and an absent list is unknown.
func TestApisCopilotEndpointMapping(t *testing.T) {
	for in, want := range map[string][]string{
		"": nil,
		`["/chat/completions","/responses","/v1/messages"]`: {"chat", "responses", "anthropic"},
		`["/responses","ws:/responses"]`:                    {"responses"},
		`["ws:/responses","/embeddings"]`:                   nil,
	} {
		var eps []string
		if in != "" {
			eps = mustJSONStrings(t, in)
		}
		if got := copilotAPIs(eps); !reflect.DeepEqual(got, want) {
			t.Errorf("copilotAPIs(%s) = %v, want %v", in, got, want)
		}
	}
}

func mustJSONStrings(t *testing.T, s string) []string {
	t.Helper()
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
