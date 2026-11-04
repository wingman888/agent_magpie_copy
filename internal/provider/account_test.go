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
	"sync/atomic"
	"testing"
	"time"
)

// acctFakeJWT builds an unsigned token with the given claims. dial only decodes
// the payload, so the signature is a placeholder.
func acctFakeJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// acctIsolate points HOME and the XDG directories at a fresh temp dir.
func acctIsolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "no-claude"))
	return home
}

func acctWriteTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// acctWriteCodexLogin writes a ChatGPT-mode auth.json whose access token expires at exp.
func acctWriteCodexLogin(t *testing.T, home string, exp time.Time) string {
	t.Helper()
	id := acctFakeJWT(map[string]any{
		"email":                       "me@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
	})
	access := acctFakeJWT(map[string]any{"exp": exp.Unix()})
	body, _ := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      id,
			"access_token":  access,
			"refresh_token": "rt-old",
			"account_id":    "acct-1",
		},
		"last_refresh": "2020-01-01T00:00:00Z",
		"extra":        "keep",
	})
	p := filepath.Join(home, ".codex", "auth.json")
	acctWriteTestFile(t, p, string(body))
	return access
}

func acctWriteCopilotLogin(t *testing.T, home string) {
	t.Helper()
	acctWriteTestFile(t, filepath.Join(home, ".config", "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_test"}}`)
}

func acctMustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func acctFindProvider(t *testing.T, id string) Provider {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatalf("Find(%q): %v", id, err)
	}
	return *p
}

func TestAccountDiscoveryAndLogout(t *testing.T) {
	home := acctIsolate(t)
	acctWriteCodexLogin(t, home, time.Now().Add(time.Hour))

	accs := Accounts()
	if len(accs) != 1 || accs[0].ID != "codex" {
		t.Fatalf("Accounts() = %+v, want one codex account", accs)
	}
	a := accs[0]
	if a.Account == nil || a.Account.User != "me@example.com" || a.Account.Plan != "pro" || a.Account.Agent != "codex" {
		t.Fatalf("account info = %+v", a.Account)
	}
	if a.Key != "" || !a.Ready() {
		t.Fatalf("account provider should be ready with no key")
	}
	if p := acctFindProvider(t, "codex"); p.Account == nil {
		t.Fatalf("Find(codex) lost its account")
	}

	// Logging out in the agent removes the provider at once.
	acctMustNoErr(t, os.Remove(filepath.Join(home, ".codex", "auth.json")))
	if n := len(Accounts()); n != 0 {
		t.Fatalf("after logout Accounts() has %d entries", n)
	}
	if _, err := Find("codex"); err == nil {
		t.Fatalf("codex still listed after logout")
	}
}

func TestAPIKeyModeIsNotAccount(t *testing.T) {
	home := acctIsolate(t)
	acctWriteTestFile(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test"}`)
	if n := len(Accounts()); n != 0 {
		t.Fatalf("API-key login counted as an account (%d)", n)
	}
}

func TestSelectionStoresOnlyModels(t *testing.T) {
	home := acctIsolate(t)
	acctWriteCodexLogin(t, home, time.Now().Add(time.Hour))

	acctMustNoErr(t, Save(Provider{ID: "codex", Chat: "https://evil.example/v1", Key: "sk-secret", Models: []string{"gpt-5.5"}}))
	raw, err := os.ReadFile(Path())
	acctMustNoErr(t, err)
	if strings.Contains(string(raw), "sk-secret") || strings.Contains(string(raw), "evil.example") {
		t.Fatalf("config kept a key or address: %s", raw)
	}

	p := acctFindProvider(t, "codex")
	if len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Fatalf("Models = %v", p.Models)
	}
	ex := p.Exposed()
	if len(ex) != 1 || ex[0].ID != "gpt-5.5" {
		t.Fatalf("Exposed() = %+v", ex)
	}

	// Delete clears the picks; the provider stays because the login does.
	acctMustNoErr(t, Delete("codex"))
	p = acctFindProvider(t, "codex")
	if len(p.Models) != 0 {
		t.Fatalf("Models after delete = %v", p.Models)
	}
}

func TestOrphanSelectionIsIgnored(t *testing.T) {
	home := acctIsolate(t)
	acctWriteTestFile(t, Path(), `{"providers":[{"id":"codex","models":["gpt-5.5"]}]}`)
	_ = home
	for _, p := range All() {
		if p.ID == "codex" {
			t.Fatalf("orphan selection became a provider: %+v", p)
		}
	}
}

func TestSelfBuiltProviderWins(t *testing.T) {
	home := acctIsolate(t)
	acctWriteCopilotLogin(t, home)
	// The user's own copilot provider, as it sits in their config.
	acctWriteTestFile(t, Path(), `{"providers":[{"id":"copilot","name":"Mine","chat":"https://relay.example/v1","key":"k"}]}`)

	n := 0
	for _, p := range All() {
		if p.ID == "copilot" {
			n++
			if p.Account != nil || p.Chat == "" {
				t.Fatalf("own provider replaced by account: %+v", p)
			}
		}
	}
	if n != 1 {
		t.Fatalf("copilot listed %d times", n)
	}
}

func TestClaudeExcludedNotOffered(t *testing.T) {
	home := acctIsolate(t)
	claude := filepath.Join(home, "no-claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	acctWriteTestFile(t, filepath.Join(claude, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"secret"}}`)

	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "claude" || ex[0].Why == "" {
		t.Fatalf("Excluded() = %+v", ex)
	}
	if strings.Contains(ex[0].Why, "secret") {
		t.Fatalf("exclusion text leaks a credential")
	}
	for _, a := range Accounts() {
		if a.ID == "claude" || (a.Account != nil && a.Account.Agent == "claude") {
			t.Fatalf("claude offered as a provider")
		}
	}

	acctMustNoErr(t, os.Remove(filepath.Join(claude, ".credentials.json")))
	if len(Excluded()) != 0 {
		t.Fatalf("claude still excluded after its login is gone")
	}
}

func TestCodexSignAndPrepare(t *testing.T) {
	home := acctIsolate(t)
	access := acctWriteCodexLogin(t, home, time.Now().Add(time.Hour))

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	oldBase := CodexBase
	CodexBase = srv.URL + "/backend"
	t.Cleanup(func() { CodexBase = oldBase })

	p := acctFindProvider(t, "codex")
	req, _ := http.NewRequest(http.MethodPost, "http://ignored.example/v1/responses", nil)
	acctMustNoErr(t, p.Sign(context.Background(), req, Responses, nil))
	if got := req.Header.Get("Authorization"); got != "Bearer "+access {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("chatgpt-account-id"); got != "acct-1" {
		t.Fatalf("chatgpt-account-id = %q", got)
	}
	if req.URL.String() != srv.URL+"/backend/responses" {
		t.Fatalf("URL = %s", req.URL)
	}
	_ = gotPath

	body := p.Prepare([]byte(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":16,"temperature":0.2,"reasoning":{"effort":"low"}}`))
	var m map[string]any
	acctMustNoErr(t, json.Unmarshal(body, &m))
	if m["model"] != "gpt-5.5" || m["stream"] != true || m["store"] != false {
		t.Fatalf("prepared body = %s", body)
	}
	if _, ok := m["max_output_tokens"]; ok {
		t.Fatalf("max_output_tokens kept: %s", body)
	}
	if _, ok := m["temperature"]; ok {
		t.Fatalf("temperature kept: %s", body)
	}
	if _, ok := m["reasoning"]; !ok {
		t.Fatalf("reasoning dropped: %s", body)
	}
	items, ok := m["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input not a item list: %s", body)
	}
	item := items[0].(map[string]any)
	if item["role"] != "user" || item["content"] != "hi" {
		t.Fatalf("input item = %v", item)
	}
}

func TestPlainProviderUnchanged(t *testing.T) {
	acctIsolate(t)
	p := Provider{ID: "x", Key: "sk-plain", Chat: "https://x.example/v1"}
	body := []byte(`{"model":"codex/gpt-5.5"}`)
	if string(p.Prepare(body)) != string(body) {
		t.Fatalf("plain provider body changed")
	}
	req, _ := http.NewRequest(http.MethodPost, "https://x.example/v1/chat/completions", nil)
	acctMustNoErr(t, p.Sign(context.Background(), req, Chat, body))
	if got := req.Header.Get("Authorization"); got != "Bearer sk-plain" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestCodexRefreshWritesBack(t *testing.T) {
	home := acctIsolate(t)
	acctWriteCodexLogin(t, home, time.Now().Add(2*time.Minute)) // inside the five-minute window

	newAccess := acctFakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	var calls int32
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "refresh_token") || !strings.Contains(string(b), "rt-old") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  newAccess,
			"refresh_token": "rt-new",
			"id_token":      acctFakeJWT(map[string]any{"email": "me@example.com"}),
		})
	}))
	defer tok.Close()
	oldURL := codexTokenURL
	codexTokenURL = tok.URL
	t.Cleanup(func() { codexTokenURL = oldURL })

	p := acctFindProvider(t, "codex")
	req, _ := http.NewRequest(http.MethodPost, "http://x", nil)
	acctMustNoErr(t, p.Sign(context.Background(), req, Responses, nil))
	if got := req.Header.Get("Authorization"); got != "Bearer "+newAccess {
		t.Fatalf("request did not use the refreshed token")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("token endpoint called %d times", calls)
	}

	path := filepath.Join(home, ".codex", "auth.json")
	raw, err := os.ReadFile(path)
	acctMustNoErr(t, err)
	var top map[string]any
	acctMustNoErr(t, json.Unmarshal(raw, &top))
	toks := top["tokens"].(map[string]any)
	if toks["access_token"] != newAccess || toks["refresh_token"] != "rt-new" {
		t.Fatalf("tokens not written back: %v", toks)
	}
	if toks["account_id"] != "acct-1" || top["extra"] != "keep" || top["auth_mode"] != "chatgpt" {
		t.Fatalf("other fields lost: %s", raw)
	}
	fi, err := os.Stat(path)
	acctMustNoErr(t, err)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json mode = %v", fi.Mode().Perm())
	}
}

func TestCodexRefreshFailureAsksForLogin(t *testing.T) {
	home := acctIsolate(t)
	acctWriteCodexLogin(t, home, time.Now().Add(time.Minute))

	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer tok.Close()
	oldURL := codexTokenURL
	codexTokenURL = tok.URL
	t.Cleanup(func() { codexTokenURL = oldURL })

	p := acctFindProvider(t, "codex")
	req, _ := http.NewRequest(http.MethodPost, "http://x", nil)
	err := p.Sign(context.Background(), req, Responses, nil)
	if err == nil || !strings.Contains(err.Error(), "已登出，请重新登录") {
		t.Fatalf("Sign error = %v", err)
	}
}

func TestCopilotExchangeModelsAndSign(t *testing.T) {
	home := acctIsolate(t)
	acctWriteCopilotLogin(t, home)

	var exchanges int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			atomic.AddInt32(&exchanges, 1)
			if r.Header.Get("Authorization") != "token gho_test" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"token":      "sess-1",
				"expires_at": time.Now().Add(30 * time.Minute).Unix(),
				"endpoints":  map[string]any{"api": srv.URL + "/api"},
			})
		case "/api/models":
			if r.Header.Get("Authorization") != "Bearer sess-1" || r.Header.Get("Copilot-Integration-Id") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": []any{
				map[string]any{"id": "claude-sonnet-4.5", "name": "Claude Sonnet 4.5", "model_picker_enabled": true,
					"capabilities": map[string]any{"type": "chat", "supports": map[string]any{"reasoning_effort": []string{"low", "high"}}}},
				map[string]any{"id": "auto", "model_picker_enabled": true, "capabilities": map[string]any{"type": "chat"}},
				map[string]any{"id": "copilot-search-x", "model_picker_enabled": true, "capabilities": map[string]any{"type": "chat"}},
				map[string]any{"id": "gpt-4o-secondary", "model_picker_enabled": true, "capabilities": map[string]any{"type": "chat"}},
				map[string]any{"id": "off-model", "model_picker_enabled": false, "policy": map[string]any{"state": "disabled"}, "capabilities": map[string]any{"type": "chat"}},
				map[string]any{"id": "embed-1", "model_picker_enabled": true, "capabilities": map[string]any{"type": "embeddings"}},
				map[string]any{"id": "gpt-5", "policy": map[string]any{"state": "enabled"}, "capabilities": map[string]any{"type": "chat"}},
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	oldURL := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	t.Cleanup(func() { CopilotTokenURL = oldURL })

	p := acctFindProvider(t, "copilot")
	ms, err := p.Fetch(context.Background())
	acctMustNoErr(t, err)
	if len(ms) != 2 || ms[0].ID != "claude-sonnet-4.5" || ms[1].ID != "gpt-5" {
		t.Fatalf("refreshed models = %+v", ms)
	}
	if len(ms[0].Efforts) != 2 || ms[0].Efforts[1] != "high" {
		t.Fatalf("efforts = %v", ms[0].Efforts)
	}
	if av := acctFindProvider(t, "copilot").Available(); len(av) != 2 {
		t.Fatalf("Available() after refresh = %+v", av)
	}

	// Signing reuses the cached session and targets its API.
	body := []byte(`{"model":"copilot/claude-sonnet-4.5","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, "http://x/chat/completions", nil)
	acctMustNoErr(t, p.Sign(context.Background(), req, Chat, body))
	if req.URL.String() != srv.URL+"/api/chat/completions" {
		t.Fatalf("URL = %s", req.URL)
	}
	if req.Header.Get("Authorization") != "Bearer sess-1" || req.Header.Get("X-Initiator") != "user" {
		t.Fatalf("headers = %v", req.Header)
	}
	if atomic.LoadInt32(&exchanges) != 1 {
		t.Fatalf("token exchanged %d times, want 1 (cached)", exchanges)
	}

	agentBody := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"r"}]}`)
	req2, _ := http.NewRequest(http.MethodPost, "http://x/chat/completions", nil)
	acctMustNoErr(t, p.Sign(context.Background(), req2, Chat, agentBody))
	if req2.Header.Get("X-Initiator") != "agent" {
		t.Fatalf("tool-last request initiator = %q", req2.Header.Get("X-Initiator"))
	}

	visionBody := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"x"},{"type":"image_url","image_url":{"url":"data:"}}]}]}`)
	req3, _ := http.NewRequest(http.MethodPost, "http://x/chat/completions", nil)
	acctMustNoErr(t, p.Sign(context.Background(), req3, Chat, visionBody))
	if req3.Header.Get("Copilot-Vision-Request") != "true" {
		t.Fatalf("vision flag missing")
	}

	if got := string(p.Prepare([]byte(`{"model":"copilot/claude-sonnet-4.5"}`))); !strings.Contains(got, `"model":"claude-sonnet-4.5"`) {
		t.Fatalf("Prepare did not strip prefix: %s", got)
	}
}

func TestCopilotBuiltinHasNoAuto(t *testing.T) {
	acctIsolate(t)
	p := Provider{ID: "copilot", Account: &Account{Agent: "copilot", User: "x"}}
	for _, m := range p.Available() {
		if m.ID == "auto" {
			t.Fatalf("built-in Copilot list offers auto")
		}
	}
}
