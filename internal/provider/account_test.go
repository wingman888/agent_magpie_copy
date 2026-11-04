package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// resetAccountState forgets cached Claude detection and Copilot sessions.
func resetAccountState() {
	exclusions.mu.Lock()
	exclusions.at, exclusions.val = time.Time{}, nil
	exclusions.mu.Unlock()
	sessionMu.Lock()
	sessionByOAuth = map[string]copilotSession{}
	sessionMu.Unlock()
}

// sandbox points every credential, config and cache location at a fresh
// temporary directory, so the real ~/.codex and friends are never touched.
func sandbox(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	resetAccountState()
	oldCodex, oldToken := CodexBase, CopilotTokenURL
	t.Cleanup(func() {
		CodexBase, CopilotTokenURL = oldCodex, oldToken
		resetAccountState()
	})
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// signInCodex writes a ChatGPT-mode Codex auth.json into the sandbox.
func signInCodex(t *testing.T, email, plan string) {
	t.Helper()
	idTok := fakeJWT(t, map[string]any{
		"email":                       email,
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": plan},
	})
	auth := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      idTok,
			"access_token":  "ACCESS-SECRET",
			"refresh_token": "REFRESH-SECRET",
			"account_id":    "acct-1",
		},
	}
	b, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(os.Getenv("CODEX_HOME"), "auth.json"), string(b))
}

func codexAuthPath() string { return filepath.Join(os.Getenv("CODEX_HOME"), "auth.json") }

// signInCopilot writes a GitHub entry into the sandbox's Copilot hosts.json.
func signInCopilot(t *testing.T, oauth string) {
	t.Helper()
	entries := map[string]any{
		"github.com:Iv1.test": map[string]any{"user": "octocat", "oauth_token": oauth},
	}
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "hosts.json"), string(b))
}

func mustAccount(t *testing.T, id string) Provider {
	t.Helper()
	for _, p := range Accounts() {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("account %q not listed; accounts = %+v", id, Accounts())
	return Provider{}
}

func hasProvider(ps []Provider, id string) bool {
	for _, p := range ps {
		if p.ID == id {
			return true
		}
	}
	return false
}

const copilotModelsJSON = `{"data":[
 {"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}},"model_picker_enabled":true,"policy":{"state":"enabled"}},
 {"id":"copilot-search-v2","name":"Search","capabilities":{"type":"chat"},"model_picker_enabled":true,"policy":{"state":"enabled"}},
 {"id":"gpt-5.5-secondary","name":"Helper","capabilities":{"type":"chat"},"model_picker_enabled":true,"policy":{"state":"enabled"}},
 {"id":"text-embedding-3","name":"Embed","capabilities":{"type":"embeddings"},"model_picker_enabled":true,"policy":{"state":"enabled"}},
 {"id":"gemini-off","name":"Gemini Off","capabilities":{"type":"chat"},"model_picker_enabled":false,"policy":{"state":"disabled"}},
 {"id":"gpt-policy-only","name":"GPT Policy","capabilities":{"type":"chat"},"model_picker_enabled":false,"policy":{"state":"enabled"}}
]}`

// fakeCopilot stands in for GitHub's token exchange and Copilot's API. It
// returns the server and a counter of token exchanges.
func fakeCopilot(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var exchanges int32
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_TOKEN" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		atomic.AddInt32(&exchanges, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "sess-TOKEN",
			"expires_at": time.Now().Add(time.Hour).Unix(),
			"endpoints":  map[string]any{"api": srv.URL + "/api"},
		})
	})
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess-TOKEN" || r.Header.Get("Copilot-Integration-Id") == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, copilotModelsJSON)
	})
	mux.HandleFunc("/api/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x"}`)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	CopilotTokenURL = srv.URL + "/token"
	return srv, &exchanges
}

// ---- Codex ------------------------------------------------------------------

func TestCodexLoginIsAccount(t *testing.T) {
	sandbox(t)
	signInCodex(t, "dev@example.com", "pro")

	p := mustAccount(t, "codex")
	if p.Name != "Codex" || p.Account == nil {
		t.Fatalf("want Codex account provider, got %+v", p)
	}
	if p.Account.Agent != "codex" || p.Account.User != "dev@example.com" || p.Account.Plan != "pro" {
		t.Fatalf("account = %+v", *p.Account)
	}
	if !p.Ready() {
		t.Fatal("account provider should be ready without a key")
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "ACCESS-SECRET") || strings.Contains(string(b), "REFRESH-SECRET") {
		t.Fatalf("token leaked into provider JSON: %s", b)
	}
	if _, err := Find("codex"); err != nil {
		t.Fatalf("Find(codex): %v", err)
	}
	if !hasProvider(All(), "codex") {
		t.Fatal("codex missing from All()")
	}
	found := false
	for _, e := range Catalog() {
		if e.ID == "codex/gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Fatal("codex/gpt-5.5 missing from catalog")
	}
	rp, model, ok := Resolve("codex/gpt-5.5")
	if !ok || rp.ID != "codex" || model != "gpt-5.5" {
		t.Fatalf("Resolve(codex/gpt-5.5) = %v %q %v", rp.ID, model, ok)
	}
}

func TestCodexAPIKeyModeIsNotAccount(t *testing.T) {
	sandbox(t)
	writeTestFile(t, codexAuthPath(), `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test"}`)
	if len(Accounts()) != 0 {
		t.Fatalf("API key mode must not be an account: %+v", Accounts())
	}
	if hasProvider(All(), "codex") {
		t.Fatal("codex listed in API key mode")
	}
}

func TestCodexWithoutEmailFallsBack(t *testing.T) {
	sandbox(t)
	writeTestFile(t, codexAuthPath(), `{"auth_mode":"chatgpt","tokens":{"access_token":"a","account_id":"x"}}`)
	p := mustAccount(t, "codex")
	if p.Account.User != "ChatGPT" || p.Account.Plan != "" {
		t.Fatalf("account = %+v", *p.Account)
	}
}

func TestLogoutRemovesAccountAndLeavesNoRemnant(t *testing.T) {
	sandbox(t)
	signInCodex(t, "dev@example.com", "plus")
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if p := mustAccount(t, "codex"); len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Fatalf("stored models not applied: %+v", p.Models)
	}

	if err := os.Remove(codexAuthPath()); err != nil {
		t.Fatal(err)
	}
	if len(Accounts()) != 0 {
		t.Fatalf("logged out but still listed: %+v", Accounts())
	}
	if hasProvider(All(), "codex") {
		t.Fatal("codex still in All() after logout")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Fatalf("catalog still has %s after logout", e.ID)
		}
	}
	if _, err := Find("codex"); err == nil {
		t.Fatal("Find(codex) should fail after logout")
	}
}

func TestSaveAccountKeepsOnlyModels(t *testing.T) {
	sandbox(t)
	signInCodex(t, "dev@example.com", "pro")
	err := Save(Provider{
		ID:     "codex",
		Key:    "SECRET-KEY",
		Chat:   "https://evil.example/v1",
		Models: []string{"gpt-5.5", " gpt-5.5 ", "gpt-5.5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, bad := range []string{"SECRET-KEY", "evil.example", `"key"`, `"chat"`} {
		if strings.Contains(s, bad) {
			t.Fatalf("config contains %q:\n%s", bad, s)
		}
	}
	if !strings.Contains(s, "gpt-5.5") {
		t.Fatalf("model selection not stored:\n%s", s)
	}
	p := mustAccount(t, "codex")
	if len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Fatalf("models = %v", p.Models)
	}
	ex := p.Exposed()
	if len(ex) != 1 || ex[0].ID != "gpt-5.5" {
		t.Fatalf("exposed = %+v; want only gpt-5.5", ex)
	}
}

func TestDeleteAccountClearsSelectionOnly(t *testing.T) {
	sandbox(t)
	signInCodex(t, "dev@example.com", "pro")
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatalf("Delete(codex): %v", err)
	}
	p := mustAccount(t, "codex")
	if len(p.Models) != 0 {
		t.Fatalf("selection not cleared: %v", p.Models)
	}
}

func TestDeleteAccountNotSignedInFails(t *testing.T) {
	sandbox(t)
	if err := Delete("codex"); err == nil {
		t.Fatal("deleting an absent account with no record should fail")
	}
}

func TestCustomProviderWinsOverLogin(t *testing.T) {
	sandbox(t)
	signInCodex(t, "dev@example.com", "pro")
	writeTestFile(t, Path(), `{"providers":[{"id":"codex","name":"My Codex","key":"k","chat":"https://relay.example/v1"}]}`)

	ps := All()
	n := 0
	for _, p := range ps {
		if p.ID == "codex" {
			n++
			if p.Account != nil || p.Chat != "https://relay.example/v1" {
				t.Fatalf("custom codex not used: %+v", p)
			}
		}
	}
	if n != 1 {
		t.Fatalf("codex listed %d times", n)
	}
	if len(Accounts()) != 0 {
		t.Fatalf("login should be shadowed by custom provider: %+v", Accounts())
	}
}

func TestCodexSignAndPrepare(t *testing.T) {
	sandbox(t)
	CodexBase = "https://codex.test/backend-api/codex"
	signInCodex(t, "dev@example.com", "pro")
	p := mustAccount(t, "codex")

	req, _ := http.NewRequest(http.MethodPost, "http://unused.invalid/", nil)
	if err := p.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != CodexBase+"/responses" {
		t.Fatalf("URL = %s", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer ACCESS-SECRET" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("chatgpt-account-id"); got != "acct-1" {
		t.Fatalf("chatgpt-account-id = %q", got)
	}

	req2, _ := http.NewRequest(http.MethodPost, "http://unused.invalid/", nil)
	if err := p.Sign(context.Background(), req2, Chat, nil); err == nil {
		t.Fatal("codex must refuse Chat requests")
	}

	in := []byte(`{"model":"gpt-5.5","input":"hello","max_output_tokens":16,"temperature":0.2,"reasoning":{"effort":"high"},"stream":false,"store":true}`)
	var out map[string]any
	if err := json.Unmarshal(p.Prepare(in), &out); err != nil {
		t.Fatal(err)
	}
	if out["stream"] != true || out["store"] != false {
		t.Fatalf("stream/store = %v/%v", out["stream"], out["store"])
	}
	items, ok := out["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input = %#v; want one item", out["input"])
	}
	item := items[0].(map[string]any)
	content := item["content"].([]any)[0].(map[string]any)
	if item["role"] != "user" || content["text"] != "hello" {
		t.Fatalf("input item = %#v", item)
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := out[k]; ok {
			t.Fatalf("%s should be removed", k)
		}
	}
	if r, _ := out["reasoning"].(map[string]any); r["effort"] != "high" {
		t.Fatalf("reasoning dropped: %v", out["reasoning"])
	}

	// Once signed out, signing must fail with a clear error.
	if err := os.Remove(codexAuthPath()); err != nil {
		t.Fatal(err)
	}
	req3, _ := http.NewRequest(http.MethodPost, "http://unused.invalid/", nil)
	if err := p.Sign(context.Background(), req3, Responses, nil); !errors.Is(err, errCodexSignedOut) {
		t.Fatalf("signed out error = %v", err)
	}
}

func TestPlainProviderSignAndPrepareUnchanged(t *testing.T) {
	sandbox(t)
	p := Provider{ID: "openai", Name: "OpenAI", Key: "sk-1", Chat: "https://api.example/v1"}
	req, _ := http.NewRequest(http.MethodPost, "https://api.example/v1/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-1" {
		t.Fatalf("Authorization = %q", got)
	}
	in := []byte(`{"input":"x","max_output_tokens":4}`)
	if string(p.Prepare(in)) != string(in) {
		t.Fatal("plain provider body must be returned unchanged")
	}
}

// ---- Copilot ----------------------------------------------------------------

func TestCopilotRefreshKeepsChatModels(t *testing.T) {
	sandbox(t)
	fakeCopilot(t)
	signInCopilot(t, "gho_TOKEN")

	p := mustAccount(t, "copilot")
	if p.Account.User != "octocat" || p.Name != "Copilot" {
		t.Fatalf("copilot account = %+v", p)
	}

	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].ID != "claude-sonnet-4.5" || ms[1].ID != "gpt-policy-only" {
		t.Fatalf("refreshed models = %+v", ms)
	}
	if strings.Join(ms[0].Efforts, ",") != "low,high" {
		t.Fatalf("efforts = %v", ms[0].Efforts)
	}

	// After a refresh the available list is the refreshed one.
	avail := mustAccount(t, "copilot").Available()
	if len(avail) != 2 || avail[0].ID != "claude-sonnet-4.5" {
		t.Fatalf("available after refresh = %+v", avail)
	}
	rp, model, ok := Resolve("copilot/claude-sonnet-4.5")
	if !ok || rp.ID != "copilot" || model != "claude-sonnet-4.5" {
		t.Fatalf("Resolve = %v %q %v", rp.ID, model, ok)
	}
}

func TestCopilotBuiltinListHasNoAuto(t *testing.T) {
	sandbox(t)
	signInCopilot(t, "gho_TOKEN")
	avail := mustAccount(t, "copilot").Available()
	hasSonnet := false
	for _, m := range avail {
		if m.ID == "auto" {
			t.Fatal("built-in copilot list must not contain auto")
		}
		if m.ID == "claude-sonnet-4.5" {
			hasSonnet = true
		}
	}
	if !hasSonnet {
		t.Fatal("built-in copilot list lacks claude-sonnet-4.5")
	}
}

func TestCopilotSignHeadersInitiatorAndSessionCache(t *testing.T) {
	sandbox(t)
	_, exchanges := fakeCopilot(t)
	signInCopilot(t, "gho_TOKEN")
	p := mustAccount(t, "copilot")
	ctx := context.Background()

	withImage := []byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"system","content":"x"},{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`)
	req, _ := http.NewRequest(http.MethodPost, "http://unused.invalid/", nil)
	if err := p.Sign(ctx, req, Chat, withImage); err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); !strings.HasSuffix(got, "/api/chat/completions") {
		t.Fatalf("URL = %s", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sess-TOKEN" {
		t.Fatalf("Authorization = %q", got)
	}
	if req.Header.Get("Copilot-Integration-Id") == "" {
		t.Fatal("Copilot-Integration-Id missing")
	}
	if got := req.Header.Get("X-Initiator"); got != "user" {
		t.Fatalf("X-Initiator = %q; want user", got)
	}
	if got := req.Header.Get("Copilot-Vision-Request"); got != "true" {
		t.Fatalf("Copilot-Vision-Request = %q", got)
	}
	sent, _ := io.ReadAll(req.Body)
	if string(sent) != string(withImage) {
		t.Fatal("request body changed by signing")
	}

	toolTurn := []byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"x"},{"role":"tool","content":"result"}]}`)
	req2, _ := http.NewRequest(http.MethodPost, "http://unused.invalid/", nil)
	if err := p.Sign(ctx, req2, Chat, toolTurn); err != nil {
		t.Fatal(err)
	}
	if got := req2.Header.Get("X-Initiator"); got != "agent" {
		t.Fatalf("tool turn X-Initiator = %q; want agent", got)
	}
	if got := req2.Header.Get("Copilot-Vision-Request"); got != "" {
		t.Fatalf("no image, but Copilot-Vision-Request = %q", got)
	}

	if n := atomic.LoadInt32(exchanges); n != 1 {
		t.Fatalf("token exchanges = %d; want 1 (session cached)", n)
	}
}

func TestCopilotRejectedLoginNeedsSignIn(t *testing.T) {
	sandbox(t)
	fakeCopilot(t)
	signInCopilot(t, "gho_REVOKED")
	p := mustAccount(t, "copilot")
	req, _ := http.NewRequest(http.MethodPost, "http://unused.invalid/", nil)
	err := p.Sign(context.Background(), req, Chat, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if !errors.Is(err, errCopilotSignedOut) {
		t.Fatalf("err = %v; want signed-out error", err)
	}
}

// ---- Claude Code ------------------------------------------------------------

func TestClaudeLoginIsExcludedNotOffered(t *testing.T) {
	sandbox(t)
	if n := len(Excluded()); n != 0 {
		t.Fatalf("excluded without a login: %+v", Excluded())
	}

	claudeCreds := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json")
	writeTestFile(t, claudeCreds, `{"claudeAiOauth":{"accessToken":"SECRET-CLAUDE-TOKEN"}}`)
	resetAccountState() // the empty answer above is cached for a minute

	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "claude" || ex[0].Why == "" {
		t.Fatalf("Excluded() = %+v", ex)
	}
	if strings.Contains(ex[0].Why, "SECRET") {
		t.Fatal("exclusion text must not contain credentials")
	}
	if hasProvider(All(), "claude") || len(Accounts()) != 0 {
		t.Fatal("claude login must never be offered as a provider")
	}

	// The answer is cached for about a minute, so removing the file is not
	// seen until the cache is cleared.
	if err := os.Remove(claudeCreds); err != nil {
		t.Fatal(err)
	}
	if len(Excluded()) != 1 {
		t.Fatal("Excluded should be cached")
	}
	resetAccountState()
	if len(Excluded()) != 0 {
		t.Fatal("Excluded should be empty after logout")
	}
}
