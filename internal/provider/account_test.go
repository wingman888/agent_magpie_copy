package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sandbox points HOME and the XDG dirs at a fresh temp dir, so no test sees
// the real machine's sign-ins or dial config.
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	resetExcluded()
	return home
}

func resetExcluded() {
	excluded.Lock()
	excluded.key = ""
	excluded.Unlock()
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// testJWT builds an unsigned JWT with the given payload.
func testJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

const codexSecret = "acc-secret-token"

func writeCodexChatGPT(t *testing.T, home string) {
	t.Helper()
	idTok := testJWT(map[string]any{
		"email":                        "dev@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
	})
	body, _ := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      idTok,
			"access_token":  codexSecret,
			"refresh_token": "refresh-secret",
			"account_id":    "acct-123",
		},
	})
	writeTestFile(t, filepath.Join(home, ".codex", "auth.json"), string(body))
}

func writeCopilot(t *testing.T, xdg string) {
	t.Helper()
	writeTestFile(t, filepath.Join(xdg, "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octocat","oauth_token":"gho_secret"}}`)
}

func findProvider(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func TestAccountsFromAgentFiles(t *testing.T) {
	home := sandbox(t)
	writeCodexChatGPT(t, home)
	writeCopilot(t, filepath.Join(home, ".config"))

	acc := Accounts()
	cx := findProvider(acc, "codex")
	cp := findProvider(acc, "copilot")
	if cx == nil || cp == nil {
		t.Fatalf("want codex and copilot accounts, got %+v", acc)
	}
	if cx.Account == nil || cx.Account.Agent != "codex" || cx.Account.User != "dev@example.com" || cx.Account.Plan != "pro" {
		t.Errorf("codex account = %+v", cx.Account)
	}
	if cp.Account == nil || cp.Account.Agent != "copilot" || cp.Account.User != "octocat" {
		t.Errorf("copilot account = %+v", cp.Account)
	}
	if !cx.Ready() || !cp.Ready() {
		t.Error("account providers should be ready without a key")
	}
	b, _ := json.Marshal(acc)
	for _, secret := range []string{codexSecret, "refresh-secret", "gho_secret"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("provider JSON leaks token %q: %s", secret, b)
		}
	}
	if !strings.Contains(string(b), `"account":{"agent":"codex"`) {
		t.Errorf("JSON lacks account info: %s", b)
	}
}

func TestCodexFallbackUserWithoutIDToken(t *testing.T) {
	home := sandbox(t)
	writeTestFile(t, filepath.Join(home, ".codex", "auth.json"),
		`{"tokens":{"access_token":"x","account_id":"a"}}`)
	cx := findProvider(Accounts(), "codex")
	if cx == nil || cx.Account.User != "ChatGPT" || cx.Account.Plan != "" {
		t.Fatalf("codex account = %+v", cx)
	}
}

func TestCodexAPIKeyModeIsNotAccount(t *testing.T) {
	home := sandbox(t)
	writeTestFile(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test"}`)
	if findProvider(Accounts(), "codex") != nil {
		t.Fatal("API-key mode auth.json must not be an account")
	}
}

func TestSignedOutDropsAccount(t *testing.T) {
	home := sandbox(t)
	writeCodexChatGPT(t, home)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if findProvider(Accounts(), "codex") == nil {
		t.Fatal("codex should be listed while signed in")
	}
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if findProvider(All(), "codex") != nil {
		t.Fatal("codex should disappear once signed out, even with a stored model choice")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Fatalf("catalog still lists %s after sign-out", e.ID)
		}
	}
}

func TestSaveStoresOnlyModelChoice(t *testing.T) {
	home := sandbox(t)
	writeCodexChatGPT(t, home)
	err := Save(Provider{ID: "codex", Chat: "https://evil.example/v1", Key: "sk-leak", Models: []string{"gpt-5.5"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"evil.example", "sk-leak", "chat"} {
		if strings.Contains(string(raw), s) {
			t.Errorf("config file contains %q: %s", s, raw)
		}
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(raw, &f); err != nil || len(f.Providers) != 1 {
		t.Fatalf("config = %s (%v)", raw, err)
	}
	for k := range f.Providers[0] {
		if k != "id" && k != "models" {
			t.Errorf("stored account record has field %q", k)
		}
	}

	cx := findProvider(All(), "codex")
	if cx == nil || cx.Chat != "" || cx.Key != "" || len(cx.Models) != 1 || cx.Models[0] != "gpt-5.5" {
		t.Fatalf("reloaded codex = %+v", cx)
	}
	exposed := cx.Exposed()
	if len(exposed) != 1 || exposed[0].ID != "gpt-5.5" {
		t.Fatalf("exposed = %+v, want only gpt-5.5", exposed)
	}
}

func TestDeleteClearsAccountChoiceOnly(t *testing.T) {
	home := sandbox(t)
	writeCodexChatGPT(t, home)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	cx := findProvider(All(), "codex")
	if cx == nil {
		t.Fatal("account provider should remain after delete")
	}
	if len(cx.Models) != 0 {
		t.Fatalf("models after delete = %v, want none", cx.Models)
	}
}

func TestSelfBuiltProviderWins(t *testing.T) {
	home := sandbox(t)
	writeCodexChatGPT(t, home)
	if err := Save(Provider{ID: "codex", Name: "Codex", Chat: "https://relay.example.com/v1", Key: "k-own"}); err != nil {
		t.Fatal(err)
	}
	if findProvider(Accounts(), "codex") != nil {
		t.Fatal("a self-built codex provider must suppress the account version")
	}
	cx := findProvider(All(), "codex")
	if cx == nil || cx.Account != nil || cx.Chat != "https://relay.example.com/v1" || cx.Key != "k-own" {
		t.Fatalf("codex = %+v, want the self-built provider", cx)
	}
}

func TestClaudeIsExcludedNotOffered(t *testing.T) {
	home := sandbox(t)
	if len(Excluded()) != 0 {
		t.Fatal("no Claude sign-in, nothing excluded")
	}
	writeTestFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"claude-secret"}}`)
	resetExcluded()
	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "claude" || ex[0].Why == "" {
		t.Fatalf("excluded = %+v", ex)
	}
	if strings.Contains(ex[0].Why, "claude-secret") {
		t.Fatal("exclusion text must not contain credentials")
	}
	if findProvider(All(), "claude") != nil {
		t.Fatal("Claude must never be a provider")
	}
}

func TestClaudeConfigDirHonored(t *testing.T) {
	sandbox(t)
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeTestFile(t, filepath.Join(dir, ".credentials.json"), `{}`)
	resetExcluded()
	if ex := Excluded(); len(ex) != 1 || ex[0].Agent != "claude" {
		t.Fatalf("excluded = %+v", ex)
	}
}

func TestCatalogHasAccountModels(t *testing.T) {
	home := sandbox(t)
	writeCodexChatGPT(t, home)
	writeCopilot(t, filepath.Join(home, ".config"))

	ids := map[string]bool{}
	for _, e := range Catalog() {
		ids[e.ID] = true
	}
	for _, want := range []string{"codex/gpt-5.5", "copilot/claude-sonnet-4.5"} {
		if !ids[want] {
			t.Errorf("catalog lacks %s", want)
		}
	}
	if ids["copilot/auto"] {
		t.Error("copilot/auto must not be offered before a refresh")
	}

	p, model, ok := Resolve("codex/gpt-5.5")
	if !ok || p.ID != "codex" || model != "gpt-5.5" {
		t.Fatalf("Resolve(codex/gpt-5.5) = %v %q %v", p.ID, model, ok)
	}
	for _, e := range Catalog() {
		if e.ID == "codex/gpt-5.5" && e.Provider.Origin() != "dev@example.com" {
			t.Errorf("origin = %q, want the signed-in user", e.Provider.Origin())
		}
	}
}

func TestResolveWithoutSignInFails(t *testing.T) {
	sandbox(t)
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Fatal("codex/gpt-5.5 must not resolve without a sign-in")
	}
}

func TestSignCodexAccount(t *testing.T) {
	home := sandbox(t)
	writeCodexChatGPT(t, home)
	old := CodexBase
	CodexBase = "https://chatgpt.test/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	cx := findProvider(Accounts(), "codex")
	req, _ := http.NewRequest(http.MethodPost, "http://ignored.invalid/v1/responses", nil)
	if err := cx.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != CodexBase+"/responses" {
		t.Errorf("URL = %s", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+codexSecret {
		t.Errorf("Authorization = %q", got)
	}
	if got := req.Header.Get("chatgpt-account-id"); got != "acct-123" {
		t.Errorf("chatgpt-account-id = %q", got)
	}

	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	req2, _ := http.NewRequest(http.MethodPost, CodexBase+"/responses", nil)
	if err := cx.Sign(context.Background(), req2, Responses, nil); err == nil {
		t.Error("signing after sign-out must fail with a clear error")
	}
}

func TestSignOrdinaryProviderUsesKey(t *testing.T) {
	p := Provider{ID: "x", Key: "k-1", Chat: "https://api.example.com/v1"}
	req, _ := http.NewRequest(http.MethodPost, "https://api.example.com/v1/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer k-1" {
		t.Errorf("Authorization = %q", got)
	}
	body := []byte(`{"model":"m","max_tokens":5}`)
	if string(p.Prepare(body)) != string(body) {
		t.Error("ordinary providers must get the body back unchanged")
	}
}

func TestPrepareCodexBody(t *testing.T) {
	p := Provider{ID: "codex", Account: &Account{Agent: "codex", User: "u"}}
	in := []byte(`{"model":"codex/gpt-5.5","input":"hi","stream":false,"store":true,` +
		`"max_output_tokens":16,"temperature":0.2,"reasoning":{"effort":"low"}}`)
	var m map[string]any
	if err := json.Unmarshal(p.Prepare(in), &m); err != nil {
		t.Fatal(err)
	}
	if m["stream"] != true || m["store"] != false {
		t.Errorf("stream/store = %v/%v", m["stream"], m["store"])
	}
	if _, ok := m["max_output_tokens"]; ok {
		t.Error("max_output_tokens must be removed")
	}
	if _, ok := m["temperature"]; ok {
		t.Error("temperature must be removed")
	}
	if r, _ := m["reasoning"].(map[string]any); r["effort"] != "low" {
		t.Errorf("reasoning must be kept: %v", m["reasoning"])
	}
	if m["model"] != "gpt-5.5" {
		t.Errorf("model = %v", m["model"])
	}
	items, ok := m["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input = %v, want a list of one item", m["input"])
	}
	msg := items[0].(map[string]any)
	content := msg["content"].([]any)[0].(map[string]any)
	if msg["role"] != "user" || content["text"] != "hi" {
		t.Errorf("message = %v", msg)
	}
}
