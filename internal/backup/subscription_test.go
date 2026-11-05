package backup

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
)

// A signed-in account's entry in providers.json holds model picks only. It must
// stay out of a backup, while a plain provider without a URL but with a key stays in.
func TestCollectLeavesOutSubscriptionEntries(t *testing.T) {
	isolate(t)
	seedLocal(t)
	if _, err := provider.PutStored(provider.Provider{ID: "claude", Models: []string{"opus"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.PutStored(provider.Provider{ID: "codex", Fallback: []string{"relay/x"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.PutStored(provider.Provider{ID: "keyonly", Name: "Key Only", Key: "sk-keyonly-999"}); err != nil {
		t.Fatal(err)
	}

	b, err := Collect(true, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, id := range []string{`"id":"claude"`, `"id":"codex"`} {
		if strings.Contains(s, id) {
			t.Errorf("subscription entry %s present in bundle", id)
		}
	}
	found := false
	for _, p := range b.Providers {
		if p.ID == "keyonly" {
			found = true
		}
		if subscriptionIDs[p.ID] {
			t.Errorf("subscription provider %q kept in bundle", p.ID)
		}
	}
	if !found {
		t.Errorf("keyed provider without URL was dropped")
	}
}

func TestIsSubscription(t *testing.T) {
	cases := []struct {
		p    provider.Provider
		want bool
	}{
		{provider.Provider{ID: "claude"}, true},
		{provider.Provider{ID: "grok", Models: []string{"x"}}, true},
		{provider.Provider{ID: "claude", Anthropic: "https://x.example"}, false},
		{provider.Provider{ID: "acme", Key: "sk-1"}, false},
		{provider.Provider{ID: "acme", Keys: []provider.KeyAccount{{Name: "a", Key: "k"}}}, false},
		{provider.Provider{ID: "acme"}, true},
	}
	for _, c := range cases {
		if got := isSubscription(c.p); got != c.want {
			t.Errorf("isSubscription(%+v) = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestDropSubscriptionRefs(t *testing.T) {
	in := profile.Profile{
		"claude.model":          "opus",
		"claude.provider":       "claude",
		"codex.model":           "codex/gpt-5",
		"copilot.model":         "copilot/gpt-4o",
		"models.large.provider": "deepseek",
		"gemini.model":          "acme/x",
		"gemini.effort":         "high",
	}
	got := dropSubscriptionRefs(in)
	for _, k := range []string{"claude.provider", "codex.model", "copilot.model"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s still refers to a subscription", k)
		}
	}
	for _, k := range []string{"claude.model", "models.large.provider", "gemini.model", "gemini.effort"} {
		if got[k] != in[k] {
			t.Errorf("%s = %q, want %q", k, got[k], in[k])
		}
	}
	if len(in) != 7 {
		t.Errorf("input profile was modified")
	}
}
