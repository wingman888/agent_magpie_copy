package provider

import (
	"reflect"
	"testing"
)

// With the user's own provider under the same id, the account is not offered
// and saving goes to the own provider as an ordinary edit.
func TestSaveUnderOwnProviderEditsItAsUsual(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})
	if err := store(file{Providers: []Provider{{ID: "codex", Chat: "https://own.example.com/v1", Key: "sk-own"}}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "codex", Chat: "https://other.example.com/v1", Key: "sk-other"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if p.Account != nil {
		t.Fatalf("own provider should win over the account, got %+v", p)
	}
	if p.Chat != "https://other.example.com/v1" || p.Key != "sk-other" {
		t.Errorf("own provider not edited as usual: %+v", p)
	}
}

// Picks saved for an account keep their order and drop duplicates, and the
// account's own address and key are never part of them.
func TestPicksForAccountAreCleanedOnSave(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})
	if err := Save(Provider{ID: "codex", Models: []string{" gpt-5.5 ", "gpt-5.5", "gpt-5.3-codex"}}); err != nil {
		t.Fatal(err)
	}
	p := findAccount(All(), "codex")
	if p == nil || !reflect.DeepEqual(p.Models, []string{"gpt-5.5", "gpt-5.3-codex"}) {
		t.Fatalf("picks = %+v, want [gpt-5.5 gpt-5.3-codex]", p)
	}
}
