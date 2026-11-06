package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// setupEnv points HOME, XDG_CONFIG_HOME and CLAUDE_CONFIG_DIR at a fresh
// temporary tree, so no test ever sees the real machine's sign-ins.
func setupEnv(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	copilotMu.Lock()
	copilotSessions = map[string]copilotSess{}
	copilotMu.Unlock()
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func writeCodex(t *testing.T, authMode string, claims map[string]any) {
	t.Helper()
	body := map[string]any{
		"auth_mode":      authMode,
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      fakeJWT(t, claims),
			"access_token":  "codex-access-SECRET",
			"refresh_token": "codex-refresh-SECRET",
			"account_id":    "acct-123",
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, homeFile(".codex", "auth.json"), string(b))
}

func writeCopilot(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", name)
	writeFile(t, p, body)
	return p
}

func findAccount(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func newReq(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://dial.invalid/v1/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestCodexChatGPTLoginIsAccount(t *testing.T) {
	setupEnv(t)
	writeCodex(t, "chatgpt", map[string]any{
		"email":                       "dev@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
	})
	p := findAccount(Accounts(), "codex")
	if p == nil {
		t.Fatal("codex account not offered")
	}
	if p.Name != "Codex" || p.Account == nil || p.Account.Agent != "codex" ||
		p.Account.User != "dev@example.com" || p.Account.Plan != "pro" {
		t.Fatalf("unexpected codex account: %+v", p)
	}
	if !p.Ready() || p.Key != "" {
		t.Errorf("account provider should be ready with no key")
	}
	b, _ := json.Marshal(p)
	for _, secret := range []string{"codex-access-SECRET", "codex-refresh-SECRET"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("provider JSON leaks token %q: %s", secret, b)
		}
	}
}

func TestCodexAPIKeyModeIsNotAccount(t *testing.T) {
	setupEnv(t)
	writeCodex(t, "apikey", map[string]any{"email": "dev@example.com"})
	if p := findAccount(Accounts(), "codex"); p != nil {
		t.Fatalf("API-key mode must not be an account: %+v", p)
	}
}

func TestCodexWithoutEmailFallsBack(t *testing.T) {
	setupEnv(t)
	writeCodex(t, "chatgpt", map[string]any{})
	p := findAccount(Accounts(), "codex")
	if p == nil || p.Account.User != "ChatGPT" || p.Account.Plan != "" {
		t.Fatalf("want ChatGPT fallback with empty plan, got %+v", p)
	}
}

func TestNoLoginsNoAccountsNoExclusions(t *testing.T) {
	setupEnv(t)
	if got := Accounts(); len(got) != 0 {
		t.Errorf("Accounts() = %+v, want none", got)
	}
	if got := Excluded(); len(got) != 0 {
		t.Errorf("Excluded() = %+v, want none", got)
	}
}

func TestCopilotAppsJSONUser(t *testing.T) {
	setupEnv(t)
	writeCopilot(t, "apps.json", `{"github.com:Iv1.abc":{"user":"octocat","oauth_token":"gho_apps"}}`)
	p := findAccount(Accounts(), "copilot")
	if p == nil || p.Name != "Copilot" || p.Account.Agent != "copilot" || p.Account.User != "octocat" {
		t.Fatalf("unexpected copilot account: %+v", p)
	}
	if !p.Ready() {
		t.Error("copilot account should be ready")
	}
}

func TestCopilotHostsJSONWithoutUser(t *testing.T) {
	setupEnv(t)
	writeCopilot(t, "hosts.json", `{"github.com":{"oauth_token":"gho_hosts"}}`)
	p := findAccount(Accounts(), "copilot")
	if p == nil || p.Account.User != "GitHub" {
		t.Fatalf("want GitHub fallback name, got %+v", p)
	}
}

func TestCopilotIgnoresOtherHosts(t *testing.T) {
	setupEnv(t)
	writeCopilot(t, "apps.json", `{"gitlab.com:x":{"user":"a","oauth_token":"t"}}`)
	if p := findAccount(Accounts(), "copilot"); p != nil {
		t.Fatalf("non-github entry must not be an account: %+v", p)
	}
}

func TestClaudeIsExcludedAndNeverRead(t *testing.T) {
	setupEnv(t)
	writeFile(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json"), `{"token":"claude-SECRET"}`)
	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "claude" || ex[0].Why == "" {
		t.Fatalf("want one claude exclusion with a reason, got %+v", ex)
	}
	if strings.Contains(ex[0].Why, "claude-SECRET") {
		t.Error("exclusion text must not contain credentials")
	}
	if findAccount(Accounts(), "claude") != nil {
		t.Error("claude must never be offered as a provider")
	}
}

func TestAccountsVanishOnSignOut(t *testing.T) {
	setupEnv(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})
	writeCopilot(t, "apps.json", `{"github.com:x":{"user":"octocat","oauth_token":"gho_out"}}`)
	// A leftover model pick under the codex id must not turn into a provider.
	if err := store(file{Providers: []Provider{{ID: "codex", Models: []string{"gpt-5.5"}}}}); err != nil {
		t.Fatal(err)
	}
	if p := findAccount(Accounts(), "codex"); p == nil || !reflect.DeepEqual(p.Models, []string{"gpt-5.5"}) {
		t.Fatalf("codex should carry its saved picks, got %+v", p)
	}
	if err := os.Remove(homeFile(".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if p := findAccount(Accounts(), "codex"); p != nil {
		t.Fatalf("codex still offered after sign-out: %+v", p)
	}
	if findAccount(Accounts(), "copilot") == nil {
		t.Error("copilot should be unaffected by codex sign-out")
	}
}

func TestStoredPicksOnlyModels(t *testing.T) {
	setupEnv(t)
	writeCopilot(t, "apps.json", `{"github.com:x":{"user":"octocat","oauth_token":"gho_picks"}}`)
	if err := store(file{Providers: []Provider{{ID: "copilot", Models: []string{" claude-sonnet-4.5 ", "claude-sonnet-4.5"}}}}); err != nil {
		t.Fatal(err)
	}
	p := findAccount(Accounts(), "copilot")
	if p == nil || !reflect.DeepEqual(p.Models, []string{"claude-sonnet-4.5"}) {
		t.Fatalf("want cleaned picks, got %+v", p)
	}
}

func TestOwnProviderWinsOverAccount(t *testing.T) {
	setupEnv(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})
	// The user's own addressed provider is written straight into the config, as
	// the user would have left it; Save is not used to create it.
	writeFile(t, Path(), `{"providers":[{"id":"codex","name":"My Codex","chat":"https://relay.example.com/v1","key":"sk-own"}]}`)
	if p := findAccount(Accounts(), "codex"); p != nil {
		t.Fatalf("user's own codex provider should win, got account %+v", p)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if p.Account != nil || p.Name != "My Codex" || p.Chat != "https://relay.example.com/v1" || p.Key != "sk-own" {
		t.Errorf("Find should return the user's own record, got %+v", p)
	}
	if n := len(All()); n != 1 || findAccount(All(), "codex").Chat != "https://relay.example.com/v1" {
		t.Errorf("All should hold only the own record, got %+v", All())
	}
}

func TestSignCodexUsesTokenAndBackend(t *testing.T) {
	setupEnv(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})
	old := CodexBase
	CodexBase = "https://chatgpt.test/backend-api/codex/"
	t.Cleanup(func() { CodexBase = old })

	p := findAccount(Accounts(), "codex")
	req := newReq(t)
	if err := p.Sign(context.Background(), req, Responses, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "https://chatgpt.test/backend-api/codex/responses" {
		t.Errorf("URL = %s", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer codex-access-SECRET" {
		t.Errorf("Authorization = %q", got)
	}
	if got := req.Header.Get("chatgpt-account-id"); got != "acct-123" {
		t.Errorf("chatgpt-account-id = %q", got)
	}
}

func TestSignCodexAfterSignOutFails(t *testing.T) {
	setupEnv(t)
	writeCodex(t, "chatgpt", map[string]any{"email": "dev@example.com"})
	p := findAccount(Accounts(), "codex")
	if err := os.Remove(homeFile(".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	err := p.Sign(context.Background(), newReq(t), Responses, nil)
	if !errors.Is(err, ErrSignedOut) {
		t.Fatalf("want ErrSignedOut, got %v", err)
	}
}

func TestSignCopilotExchangesCachesAndLabels(t *testing.T) {
	setupEnv(t)
	writeCopilot(t, "apps.json", `{"github.com:x":{"user":"octocat","oauth_token":"gho_sign_ok"}}`)
	var exchanges atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/copilot_internal/v2/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		exchanges.Add(1)
		if r.Header.Get("Authorization") != "token gho_sign_ok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "sess-1",
			"expires_at": time.Now().Add(time.Hour).Unix(),
			"endpoints":  map[string]any{"api": srv.URL + "/api"},
		})
	}))
	defer srv.Close()
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { CopilotTokenURL = old })

	p := findAccount(Accounts(), "copilot")
	ctx := context.Background()

	req := newReq(t)
	body := `{"model":"claude-sonnet-4.5","messages":[{"role":"system","content":"s"},` +
		`{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`
	if err := p.Sign(ctx, req, Chat, []byte(body)); err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != srv.URL+"/api/chat/completions" {
		t.Errorf("URL = %s", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sess-1" {
		t.Errorf("Authorization = %q", got)
	}
	if req.Header.Get("Copilot-Integration-Id") == "" {
		t.Error("Copilot-Integration-Id must be set")
	}
	if got := req.Header.Get("X-Initiator"); got != "user" {
		t.Errorf("X-Initiator = %q, want user", got)
	}
	if got := req.Header.Get("Copilot-Vision-Request"); got != "true" {
		t.Errorf("Copilot-Vision-Request = %q, want true", got)
	}

	req2 := newReq(t)
	tool := `{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"result"}]}`
	if err := p.Sign(ctx, req2, Chat, []byte(tool)); err != nil {
		t.Fatal(err)
	}
	if got := req2.Header.Get("X-Initiator"); got != "agent" {
		t.Errorf("X-Initiator = %q, want agent", got)
	}
	if req2.Header.Get("Copilot-Vision-Request") != "" {
		t.Error("text-only request must not be marked vision")
	}
	if n := exchanges.Load(); n != 1 {
		t.Errorf("token exchanges = %d, want 1 (session should be cached)", n)
	}
}

func TestSignCopilotRejectedTokenIsSignedOut(t *testing.T) {
	setupEnv(t)
	writeCopilot(t, "apps.json", `{"github.com:x":{"user":"octocat","oauth_token":"gho_rejected"}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	t.Cleanup(func() { CopilotTokenURL = old })

	p := findAccount(Accounts(), "copilot")
	err := p.Sign(context.Background(), newReq(t), Chat, []byte(`{"messages":[]}`))
	if !errors.Is(err, ErrSignedOut) {
		t.Fatalf("want ErrSignedOut, got %v", err)
	}
}

func TestSignPlainProviderUsesKey(t *testing.T) {
	relay := Provider{ID: "relay", Key: "sk-plain", Chat: "https://relay.example.com/v1"}
	req := newReq(t)
	if err := relay.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-plain" {
		t.Errorf("Authorization = %q", got)
	}

	direct := Provider{ID: "anthropic", Key: "sk-ant", Anthropic: "https://api.anthropic.com"}
	req = newReq(t)
	if err := direct.Sign(context.Background(), req, Anthropic, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("x-api-key"); got != "sk-ant" {
		t.Errorf("x-api-key = %q", got)
	}
}

func TestPrepareReturnsBodyUnchanged(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5"}`)
	for _, p := range []Provider{{ID: "relay"}} {
		if got := p.Prepare(body); string(got) != string(body) {
			t.Errorf("%s: Prepare changed body to %s", p.ID, got)
		}
	}
}
