package provider

import (
	"reflect"
	"strings"
	"testing"
)

// A plain Provider literal saved under a signed-in account's id, with an
// address and key typed in, keeps only the picks: the id belongs to the agent.
func TestSaveUnderSignedInAccountIdStoresOnlyPicks(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})
	err := Save(Provider{
		ID:        "codex",
		Chat:      "https://relay.example.com/v1",
		Responses: "https://relay.example.com/v1",
		Key:       "sk-LEAK-ME",
		Models:    []string{"gpt-5.5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := configText(t)
	for _, leak := range []string{"relay.example.com", "sk-LEAK-ME"} {
		if strings.Contains(text, leak) {
			t.Errorf("config holds %q:\n%s", leak, text)
		}
	}
	p := findAccount(All(), "codex")
	if p == nil {
		t.Fatal("codex account not offered after saving picks")
	}
	if p.Chat != "" || p.Responses != "" || p.Key != "" {
		t.Errorf("account carries an address or key: %+v", p)
	}
	if !reflect.DeepEqual(p.Models, []string{"gpt-5.5"}) {
		t.Errorf("picks = %v, want [gpt-5.5]", p.Models)
	}
}

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
