package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetAffinity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	get := func() string {
		t.Helper()
		p, err := Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		return p.Affinity
	}
	if got := get(); got != AffinityAuto {
		t.Fatalf("new provider affinity %q, want automatic", got)
	}
	for _, want := range []string{AffinitySession, AffinityTurn, AffinityOff, AffinityAuto} {
		if err := SetAffinity("relay", want); err != nil {
			t.Fatal(err)
		}
		if got := get(); got != want {
			t.Fatalf("set %q, read %q", want, got)
		}
	}
	// an unknown mode falls back to automatic, as the file would read it
	if err := SetAffinity("relay", "sometimes"); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != AffinityAuto {
		t.Fatalf("unknown mode kept as %q", got)
	}
	if err := SetAffinity("nowhere", AffinitySession); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestAffinityFromFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	os.MkdirAll(filepath.Join(dir, "magpie"), 0o755)
	// a file from before affinity existed reads as automatic; a mistyped
	// value falls back to automatic too
	os.WriteFile(Path(), []byte(`{"providers":[
		{"id":"old","name":"Old","chat":"https://old.example/v1","key":"k"},
		{"id":"typo","name":"Typo","chat":"https://typo.example/v1","key":"k","affinity":"forever"},
		{"id":"kept","name":"Kept","chat":"https://kept.example/v1","key":"k","affinity":"Session"}]}`), 0o600)
	for id, want := range map[string]string{"old": AffinityAuto, "typo": AffinityAuto, "kept": AffinitySession} {
		p, err := Find(id)
		if err != nil {
			t.Fatal(err)
		}
		if p.Affinity != want {
			t.Fatalf("%s: affinity %q, want %q", id, p.Affinity, want)
		}
	}
}

func TestKeyTier(t *testing.T) {
	for _, c := range []struct {
		key  Protocol
		want []Protocol
		tier int
	}{
		{Chat, []Protocol{Chat, Responses}, TierBest},
		{Responses, []Protocol{Chat, Responses}, TierBest},
		{"", []Protocol{Chat, Responses}, TierAny},
		{Anthropic, []Protocol{Chat, Responses}, TierOther},
		{Anthropic, []Protocol{Anthropic}, TierBest},
	} {
		if got := KeyTier(c.key, c.want...); got != c.tier {
			t.Fatalf("KeyTier(%q, %v) = %d, want %d", c.key, c.want, got, c.tier)
		}
	}
}
