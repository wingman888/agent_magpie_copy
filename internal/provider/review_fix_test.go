package provider

import (
	"testing"
)

// The least-used routing is spelled "least-used"; the old "usage" still
// reads as it, and anything else falls back to smart.
func TestLeastUsedSpellings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cases := map[string]string{
		"least-used": LeastUsed,
		"usage":      LeastUsed,
		"rotate":     Rotate,
		"order":      Ordered,
		"bogus":      "",
		"":           "",
	}
	for in, want := range cases {
		if err := Save(Provider{ID: "lu", Name: "LU", Chat: "https://lu.example.com/v1", Key: "k", Models: []string{"m"}, Routing: in}); err != nil {
			t.Fatal(err)
		}
		p, err := Find("lu")
		if err != nil {
			t.Fatal(err)
		}
		if p.Routing != want {
			t.Errorf("routing %q saved as %q, want %q", in, p.Routing, want)
		}
	}
	if LeastUsed != "least-used" {
		t.Errorf("LeastUsed = %q", LeastUsed)
	}
}
