package provider

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// configText is dial's own providers file as it sits on disk.
func configText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSaveAccountStoresOnlyPicks(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})
	// What the CLI saves: the account as Find returned it, with extra fields
	// the caller might have typed. Those must not reach the config.
	acct := findAccount(All(), "codex")
	if acct == nil {
		t.Fatal("codex account not listed")
	}
	acct.Chat = "https://relay.example.com/v1"
	acct.Responses = "https://relay.example.com/v1"
	acct.Key = "sk-LEAK-ME"
	acct.Models = []string{"gpt-5.5"}
	if err := Save(*acct); err != nil {
		t.Fatal(err)
	}
	text := configText(t)
	for _, leak := range []string{"relay.example.com", "sk-LEAK-ME", "dev@example.com", "codex-access-SECRET"} {
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
	if len(f.Providers) != 1 {
		t.Fatalf("want one record, got %d:\n%s", len(f.Providers), text)
	}
	if rec := f.Providers[0]; len(rec) != 2 || rec["id"] != "codex" {
		t.Errorf("record should hold only id and models, got %v", rec)
	}
	p := findAccount(All(), "codex")
	if p == nil || !reflect.DeepEqual(p.Models, []string{"gpt-5.5"}) {
		t.Fatalf("codex should expose only its pick, got %+v", p)
	}
	if p.Chat != "" || p.Responses != "" || p.Key != "" {
		t.Errorf("account carries an address or key: %+v", p)
	}
	if got := p.Exposed(); len(got) != 1 || got[0].ID != "gpt-5.5" {
		t.Errorf("Exposed() = %+v, want only gpt-5.5", got)
	}
}

func TestDeleteAccountClearsPicksButKeepsProvider(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p := findAccount(All(), "codex")
	if p == nil {
		t.Fatal("codex should stay offered while Codex is signed in")
	}
	if len(p.Models) != 0 {
		t.Errorf("picks not cleared: %v", p.Models)
	}
	if n := len(p.Exposed()); n == 0 || n != len(p.Available()) {
		t.Errorf("with no picks every model should be exposed: exposed %d of %d", n, len(p.Available()))
	}
}

func TestSignOutHidesAccountAndItsPicks(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(homeFile(".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	for _, p := range All() {
		if p.ID == "codex" {
			t.Fatalf("codex still listed after sign-out: %+v", p)
		}
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Errorf("catalog still has %s after sign-out", e.ID)
		}
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Error("Resolve still maps codex/gpt-5.5 after sign-out")
	}
}

func TestDeleteLeftoverPickAfterSignOut(t *testing.T) {
	setupEnv(t)
	if err := store(file{Providers: []Provider{{ID: "codex", Models: []string{"gpt-5.5"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatalf("deleting a leftover pick should succeed: %v", err)
	}
	if strings.Contains(configText(t), "codex") {
		t.Error("leftover pick not removed from config")
	}
	if err := Delete("codex"); err == nil {
		t.Error("second delete should report that there is no such provider")
	}
}

func TestCatalogOffersCodexAndCopilotModels(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})
	writeCopilot(t, "apps.json", `{"github.com:x":{"user":"octocat","oauth_token":"gho_cat"}}`)

	ids := map[string]bool{}
	for _, e := range Catalog() {
		ids[e.ID] = true
		if e.ID == "copilot/auto" {
			t.Error("catalog offers copilot/auto")
		}
	}
	for _, want := range []string{"codex/gpt-5.5", "copilot/claude-sonnet-4.5"} {
		if !ids[want] {
			t.Errorf("catalog lacks %s", want)
		}
	}

	p, model, ok := Resolve("codex/gpt-5.5")
	if !ok || p.ID != "codex" || model != "gpt-5.5" {
		t.Errorf("Resolve(codex/gpt-5.5) = %q %q %v", p.ID, model, ok)
	}
	p, model, ok = Resolve("copilot/claude-sonnet-4.5")
	if !ok || p.ID != "copilot" || model != "claude-sonnet-4.5" {
		t.Errorf("Resolve(copilot/claude-sonnet-4.5) = %q %q %v", p.ID, model, ok)
	}
	if p.Account == nil || p.Account.User != "octocat" {
		t.Errorf("copilot account = %+v, want user octocat", p.Account)
	}
}

func TestCodexFetchIsBuiltinAndOffline(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})
	p := findAccount(All(), "codex")
	if p == nil {
		t.Fatal("codex account not listed")
	}
	// CodexBase points nowhere: a refresh that touched the network would fail.
	saved := CodexBase
	CodexBase = "http://127.0.0.1:1/unreachable"
	defer func() { CodexBase = saved }()
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !reflect.DeepEqual(ms, p.Available()) || len(ms) == 0 {
		t.Errorf("Fetch should return the built-in list, got %d models", len(ms))
	}
}
