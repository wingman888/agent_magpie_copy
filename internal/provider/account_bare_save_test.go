package provider

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// A bare Provider under an agent's id (no Account set, no address) that carries
// a key must not write it to the config while that agent is signed in.
func TestBareSaveUnderSignedInAccountStoresOnlyPicks(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})
	if err := Save(Provider{
		ID:     "codex",
		Key:    "sk-LEAK-ME",
		Models: []string{"gpt-5.5"},
	}); err != nil {
		t.Fatal(err)
	}
	text := configText(t)
	for _, leak := range []string{"sk-LEAK-ME"} {
		if strings.Contains(text, leak) {
			t.Errorf("config holds %q:\n%s", leak, text)
		}
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal([]byte(text), &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Providers) != 1 || len(f.Providers[0]) != 2 || f.Providers[0]["id"] != "codex" {
		t.Fatalf("record should hold only id and models, got:\n%s", text)
	}
	p := findAccount(All(), "codex")
	if p == nil || p.Account == nil || !reflect.DeepEqual(p.Models, []string{"gpt-5.5"}) {
		t.Fatalf("codex should still be the account with its pick, got %+v", p)
	}
	if p.Chat != "" || p.Responses != "" || p.Key != "" {
		t.Errorf("account carries an address or key: %+v", p)
	}
}

// Without a sign-in, a bare save under an account id still keeps no address or
// key: only the model list is written.
func TestBareSaveWithoutSignInStoresOnlyPicks(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	if err := Save(Provider{
		ID:     "copilot",
		Chat:   "https://relay.example.com/v1",
		Key:    "sk-LEAK-ME",
		Models: []string{"claude-sonnet-4.5"},
	}); err != nil {
		t.Fatal(err)
	}
	text := configText(t)
	for _, leak := range []string{"relay.example.com", "sk-LEAK-ME"} {
		if strings.Contains(text, leak) {
			t.Errorf("config holds %q:\n%s", leak, text)
		}
	}
	if findAccount(All(), "copilot") != nil {
		t.Error("copilot is not signed in, so it must not be offered")
	}
}

// Signed in, with no user's own addressed provider in the config, an addressed
// save under the agent's id still stores only the picks: the address and key
// are ignored, and no self-built provider is created.
func TestAddressedSaveUnderSignedInAccountStoresOnlyPicks(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})
	if err := Save(Provider{
		ID:     "codex",
		Name:   "My Codex",
		Chat:   "https://relay.example.com/v1",
		Key:    "sk-LEAK-ME",
		Models: []string{"gpt-5.5"},
	}); err != nil {
		t.Fatal(err)
	}
	text := configText(t)
	for _, leak := range []string{"relay.example.com", "sk-LEAK-ME", "My Codex"} {
		if strings.Contains(text, leak) {
			t.Errorf("config holds %q:\n%s", leak, text)
		}
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal([]byte(text), &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Providers) != 1 || len(f.Providers[0]) != 2 || f.Providers[0]["id"] != "codex" {
		t.Fatalf("record should hold only id and models, got:\n%s", text)
	}
	p := findAccount(All(), "codex")
	if p == nil || p.Account == nil || !reflect.DeepEqual(p.Models, []string{"gpt-5.5"}) {
		t.Fatalf("codex should be the account with its pick, got %+v", p)
	}
	if p.Chat != "" || p.Responses != "" || p.Key != "" {
		t.Errorf("account carries an address or key: %+v", p)
	}
}
