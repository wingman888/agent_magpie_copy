package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// s4Isolate points HOME and XDG_CONFIG_HOME at an empty temporary directory.
func s4Isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

// s4Token builds an unsigned JWT with the given claims.
func s4Token(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".x"
}

// s4LoginCodex writes a ChatGPT-mode Codex credential file.
func s4LoginCodex(t *testing.T, home, access string) {
	t.Helper()
	plan := map[string]any{"chatgpt_plan_type": "pro"}
	id := s4Token(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": plan})
	doc := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": id, "access_token": access, "refresh_token": "r", "account_id": "acc-1",
	}}
	b, _ := json.Marshal(doc)
	path := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func s4Fresh() string {
	return s4Token(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
}

func s4Find(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func TestS4RecordHoldsOnlyModels(t *testing.T) {
	home := s4Isolate(t)
	s4LoginCodex(t, home, s4Fresh())
	// Save the account as Find returns it, with an address and key tacked on:
	// both must be ignored.
	acct, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	acct.Chat = "https://relay.example.com/v1"
	acct.Key = "sk-secret-123"
	acct.Models = []string{"gpt-5.5"}
	if err := Save(*acct); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"relay.example.com", "sk-secret-123", `"Account"`} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("config file holds %q:\n%s", bad, raw)
		}
	}
	p := s4Find(All(), "codex")
	if p == nil || p.Account == nil || p.Account.User != "me@example.com" || p.Account.Plan == nil || *p.Account.Plan != "pro" {
		t.Fatalf("codex account missing or wrong: %+v", p)
	}
	if p.Chat != "" || p.Key != "" || !p.Ready() {
		t.Errorf("account provider carries address or key, or is not ready: %+v", p)
	}
	if got := p.Exposed(); len(got) != 1 || got[0].ID != "gpt-5.5" {
		t.Errorf("exposed models = %v, want only gpt-5.5", got)
	}
}

func TestS4DeleteClearsChoiceKeepsProvider(t *testing.T) {
	home := s4Isolate(t)
	s4LoginCodex(t, home, s4Fresh())
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatalf("provider vanished after delete: %v", err)
	}
	if len(p.Models) != 0 || p.Account == nil {
		t.Errorf("selection not cleared: %+v", p)
	}
	if raw, _ := os.ReadFile(Path()); strings.Contains(string(raw), "gpt-5.5") {
		t.Errorf("selection still stored:\n%s", raw)
	}
}

func TestS4LoggedOutRecordIsIgnored(t *testing.T) {
	home := s4Isolate(t)
	s4LoginCodex(t, home, s4Fresh())
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if s4Find(All(), "codex") != nil {
		t.Error("logged-out codex still listed")
	}
	if _, err := Find("codex"); err == nil {
		t.Error("Find still returns logged-out codex")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Errorf("catalog still has %s", e.ID)
		}
	}
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err == nil {
		t.Error("saving a choice for a signed-out account should fail")
	}
}

func TestS4OwnProviderWinsOverAccount(t *testing.T) {
	home := s4Isolate(t)
	s4LoginCodex(t, home, s4Fresh())
	// The user's own provider already exists in the store (with an address).
	// Save no longer makes one under an account id, so it is seeded directly.
	if err := store(file{Providers: []Provider{{ID: "codex", Name: "My codex", Chat: "https://relay.example.com/v1", Key: "k"}}}); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range All() {
		if p.ID == "codex" {
			n++
			if p.Account != nil {
				t.Errorf("account added on top of own provider: %+v", p)
			}
		}
	}
	if n != 1 {
		t.Errorf("codex listed %d times, want 1", n)
	}
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err == nil {
		t.Error("account-type save should not replace an own provider")
	}
}

func TestS4CatalogResolvesAccountModels(t *testing.T) {
	home := s4Isolate(t)
	s4LoginCodex(t, home, s4Fresh())
	p, model, ok := Resolve("codex/gpt-5.5")
	if !ok || p.ID != "codex" || model != "gpt-5.5" {
		t.Fatalf("Resolve(codex/gpt-5.5) = %q %q %v", p.ID, model, ok)
	}
	found := false
	for _, e := range Catalog() {
		if e.ID == "codex/gpt-5.5" && e.Provider.Account != nil {
			found = true
		}
	}
	if !found {
		t.Error("codex/gpt-5.5 missing from catalog")
	}
}

func TestS4CopilotCatalogHasNoAuto(t *testing.T) {
	home := s4Isolate(t)
	dir := filepath.Join(home, "xdg", "github-copilot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "apps.json"),
		[]byte(`{"github.com:octo":{"user":"octo","oauth_token":"gho_x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, e := range Catalog() {
		ids[e.ID] = true
	}
	if !ids["copilot/claude-sonnet-4.5"] {
		t.Error("copilot/claude-sonnet-4.5 missing from catalog")
	}
	if ids["copilot/auto"] {
		t.Error("copilot/auto must not be in the catalog before a refresh")
	}
}

func TestS4AccountTestUsesSignedRequest(t *testing.T) {
	home := s4Isolate(t)
	access := s4Fresh()
	s4LoginCodex(t, home, access)

	var gotPath, gotAuth, gotAcct string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAcct = r.Header.Get("chatgpt-account-id")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer srv.Close()
	old := CodexBase
	CodexBase = srv.URL
	defer func() { CodexBase = old }()

	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	res := p.Test(context.Background())
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("test result = %+v", res)
	}
	if gotPath != "/responses" || gotAuth != "Bearer "+access || gotAcct != "acc-1" {
		t.Errorf("path=%q auth=%q account=%q", gotPath, gotAuth, gotAcct)
	}
	if gotBody["stream"] != true || gotBody["store"] != false {
		t.Errorf("body not streamed/unstored: %v", gotBody)
	}
	if _, has := gotBody["max_output_tokens"]; has {
		t.Errorf("max_output_tokens was not dropped: %v", gotBody)
	}
	if _, ok := gotBody["input"].([]any); !ok {
		t.Errorf("input is not an item list: %v", gotBody["input"])
	}
}
