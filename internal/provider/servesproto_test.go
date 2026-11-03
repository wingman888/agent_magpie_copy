package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A connectivity probe of an endpoint asks only for a model the list says
// that endpoint serves; a model the list says is served elsewhere is not it.
func TestServesProtoFollowsDeclaredAPIs(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := catalog.SaveLive("probe", "https://probe.test", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat", "anthropic"}},
		{ID: "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "probe", Name: "Probe", Key: "k"}
	for _, tc := range []struct {
		model string
		proto Protocol
		want  bool
	}{
		{"gpt-x", Responses, true},
		{"gpt-x", Chat, false},
		{"gpt-x", Anthropic, false},
		{"claude-x", Chat, true},
		{"claude-x", Responses, false},
		{"claude-x", Anthropic, true},
		{"plain", Responses, true}, // the list says nothing: any endpoint may be asked
	} {
		if got := p.servesProto(tc.model, tc.proto); got != tc.want {
			t.Errorf("servesProto(%s, %s) = %v, want %v", tc.model, tc.proto, got, tc.want)
		}
	}
}
