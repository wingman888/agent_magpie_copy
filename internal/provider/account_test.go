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

// acctSandbox points HOME and the XDG dirs at a fresh temp dir, so no test sees
// the real machine's sign-ins or dial config.
func acctSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	acctResetExcluded()
	return home
}

func acctResetExcluded() {
	excluded.Lock()
	excluded.key = ""
	excluded.Unlock()
}

func acctWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// acctJWT builds an unsigned JWT with the given payload.
func acctJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

const acctCodexSecret = "acc-secret-token"

func acctWriteCodexChatGPT(t *testing.T, home string) {
	t.Helper()
	idTok := acctJWT(map[string]any{
		"email":                       "dev@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
	})
	body, _ := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      idTok,
			"access_token":  acctCodexSecret,
			"refresh_token": "refresh-secret",
			"account_id":    "acct-123",
		},
	})
	acctWriteFile(t, filepath.Join(home, ".codex", "auth.json"), string(body))
}

func acctWriteCopilot(t *testing.T, xdg string) {
	t.Helper()
	acctWriteFile(t, filepath.Join(xdg, "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octocat","oauth_token":"gho_secret"}}`)
}

func acctFindProvider(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func TestAccountsFromAgentFiles_Acct(t *testing.T) {
	home := acctSandbox(t)
	acctWriteCodexChatGPT(t, home)
	acctWriteCopilot(t, filepath.Join(home, ".config"))

	acc := Accounts()
	cx := acctFindProvider(acc, "codex")
	cp := acctFindProvider(acc, "copilot")
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
	for _, secret := range []string{acctCodexSecret, "refresh-secret", "gho_secret"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("provider JSON leaks token %q: %s", secret, b)
		}
	}
	if !strings.Contains(string(b), `"account":{"agent":"codex"`) {
		t.Errorf("JSON lacks account info: %s", b)
	}
}

func TestCodexFallbackUserWithoutIDToken_Acct(t *testing.T) {
	home := acctSandbox(t)
	acctWriteFile(t, filepath.Join(home, ".codex", "auth.json"),
		`{"tokens":{"access_token":"x","account_id":"a"}}`)
	cx := acctFindProvider(Accounts(), "codex")
	if cx == nil || cx.Account.User != "ChatGPT" || cx.Account.Plan != "" {
		t.Fatalf("codex account = %+v", cx)
	}
}

func TestCodexAPIKeyModeIsNotAccount_Acct(t *testing.T) {
	home := acctSandbox(t)
	acctWriteFile(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test"}`)
	if acctFindProvider(Accounts(), "codex") != nil {
		t.Fatal("API-key mode auth.json must not be an account")
	}
}

func TestSignedOutDropsAccount_Acct(t *testing.T) {
	home := acctSandbox(t)
	acctWriteCodexChatGPT(t, home)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if acctFindProvider(Accounts(), "codex") == nil {
		t.Fatal("codex should be listed while signed in")
	}
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if acctFindProvider(All(), "codex") != nil {
		t.Fatal("codex should disappear once signed out, even with a stored model choice")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Fatalf("catalog still lists %s after sign-out", e.ID)
		}
	}
}

func TestSaveStoresOnlyModelChoice_Acct(t *testing.T) {
	home := acctSandbox(t)
	acctWriteCodexChatGPT(t, home)
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

	cx := acctFindProvider(All(), "codex")
	if cx == nil || cx.Chat != "" || cx.Key != "" || len(cx.Models) != 1 || cx.Models[0] != "gpt-5.5" {
		t.Fatalf("reloaded codex = %+v", cx)
	}
	exposed := cx.Exposed()
	if len(exposed) != 1 || exposed[0].ID != "gpt-5.5" {
		t.Fatalf("exposed = %+v, want only gpt-5.5", exposed)
	}
}

func TestDeleteClearsAccountChoiceOnly_Acct(t *testing.T) {
	home := acctSandbox(t)
	acctWriteCodexChatGPT(t, home)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	cx := acctFindProvider(All(), "codex")
	if cx == nil {
		t.Fatal("account provider should remain after delete")
	}
	if len(cx.Models) != 0 {
		t.Fatalf("models after delete = %v, want none", cx.Models)
	}
}

func TestSelfBuiltProviderWins_Acct(t *testing.T) {
	home := acctSandbox(t)
	acctWriteCodexChatGPT(t, home)
	// a provider the user built themselves before (with an address and key)
	acctWriteFile(t, Path(), `{"providers":[{"id":"codex","name":"Codex","key":"k-own",`+
		`"chat":"https://relay.example.com/v1"}]}`)
	if acctFindProvider(Accounts(), "codex") != nil {
		t.Fatal("a self-built codex provider must suppress the account version")
	}
	cx := acctFindProvider(All(), "codex")
	if cx == nil || cx.Account != nil || cx.Chat != "https://relay.example.com/v1" || cx.Key != "k-own" {
		t.Fatalf("codex = %+v, want the self-built provider", cx)
	}
}

func TestClaudeIsExcludedNotOffered_Acct(t *testing.T) {
	home := acctSandbox(t)
	if len(Excluded()) != 0 {
		t.Fatal("no Claude sign-in, nothing excluded")
	}
	acctWriteFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"claude-secret"}}`)
	acctResetExcluded()
	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "claude" || ex[0].Why == "" {
		t.Fatalf("excluded = %+v", ex)
	}
	if strings.Contains(ex[0].Why, "claude-secret") {
		t.Fatal("exclusion text must not contain credentials")
	}
	if acctFindProvider(All(), "claude") != nil {
		t.Fatal("Claude must never be a provider")
	}
}

func TestClaudeConfigDirHonored_Acct(t *testing.T) {
	acctSandbox(t)
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	acctWriteFile(t, filepath.Join(dir, ".credentials.json"), `{}`)
	acctResetExcluded()
	if ex := Excluded(); len(ex) != 1 || ex[0].Agent != "claude" {
		t.Fatalf("excluded = %+v", ex)
	}
}

func TestCatalogHasAccountModels_Acct(t *testing.T) {
	home := acctSandbox(t)
	acctWriteCodexChatGPT(t, home)
	acctWriteCopilot(t, filepath.Join(home, ".config"))

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

func TestResolveWithoutSignInFails_Acct(t *testing.T) {
	acctSandbox(t)
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Fatal("codex/gpt-5.5 must not resolve without a sign-in")
	}
}

func TestSignCodexAccount_Acct(t *testing.T) {
	home := acctSandbox(t)
	acctWriteCodexChatGPT(t, home)
	old := CodexBase
	CodexBase = "https://chatgpt.test/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	cx := acctFindProvider(Accounts(), "codex")
	req, _ := http.NewRequest(http.MethodPost, "http://ignored.invalid/v1/responses", nil)
	if err := cx.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != CodexBase+"/responses" {
		t.Errorf("URL = %s", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+acctCodexSecret {
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

func TestSignOrdinaryProviderUsesKey_Acct(t *testing.T) {
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

func TestPrepareCodexBody_Acct(t *testing.T) {
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
