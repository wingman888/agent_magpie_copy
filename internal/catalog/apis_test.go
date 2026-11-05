package catalog

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestLiveKeepsAPIs(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	in := []Model{
		{ID: "gpt-x", Name: "gpt-x", APIs: []string{"responses"}},
		{ID: "plain", Name: "plain"},
	}
	if err := SaveLive("relay", "https://relay.example/v1", in); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(LivePath("relay"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), `"apis"`) != 1 {
		t.Fatalf("apis should be written only where declared: %s", b)
	}
	got, _, ok := Live("relay")
	if !ok || len(got) != 2 {
		t.Fatalf("live: %+v ok=%v", got, ok)
	}
	if !slices.Equal(got[0].APIs, []string{"responses"}) || got[1].APIs != nil {
		t.Fatalf("apis round trip: %+v", got)
	}
}

func TestDecorateKeepsLiveAPIs(t *testing.T) {
	live := []Model{{ID: "claude-x", APIs: []string{"chat", "anthropic"}}}
	known := []Model{{ID: "claude-x", Name: "Claude X", Released: "2026-01-01"}}
	out := Decorate(live, known)
	if len(out) != 1 || !slices.Equal(out[0].APIs, []string{"chat", "anthropic"}) || out[0].Name != "Claude X" {
		t.Fatalf("decorate: %+v", out)
	}
}
