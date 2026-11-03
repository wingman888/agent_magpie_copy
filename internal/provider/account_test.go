package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// isolate points HOME, the XDG dirs and CLAUDE_CONFIG_DIR at a fresh temp dir.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fakeJWT builds an unsigned three-part JWT with the given payload.
func fakeJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func idToken(email string) string {
	return fakeJWT(map[string]any{
		"email": email,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type":  "plus",
			"chatgpt_account_id": "acct-1",
		},
	})
}

// codexLoginJSON is an auth.json of a ChatGPT login whose access token expires at exp.
func codexLoginJSON(t *testing.T, exp time.Time, refresh string) string {
	t.Helper()
	doc := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      idToken("me@example.com"),
			"access_token":  fakeJWT(map[string]any{"exp": exp.Unix()}),
			"refresh_token": refresh,
			"account_id":    "acct-1",
		},
		"last_refresh": "2026-01-01T00:00:00Z",
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func codexAuthPathFor(home string) string { return filepath.Join(home, ".codex", "auth.json") }

func copilotAppsPathFor(home string) string {
	return filepath.Join(home, "xdg-config", "github-copilot", "apps.json")
}

func providerIDs(ps []Provider) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

func hasID(ps []Provider, id string) bool {
	for _, p := range ps {
		if p.ID == id {
			return true
		}
	}
	return false
}

func catalogHas(id string) bool {
	for _, e := range Catalog() {
		if e.ID == id {
			return true
		}
	}
	return false
}

func TestCodexSignedInIsAccountProvider(t *testing.T) {
	home := isolate(t)
	writeFile(t, codexAuthPathFor(home), codexLoginJSON(t, time.Now().Add(time.Hour), "rt-1"))

	if !hasID(All(), "codex") {
		t.Fatalf("codex missing from All(): %v", providerIDs(All()))
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Ready() || p.Key != "" {
		t.Errorf("account provider should be ready without a key: ready=%v key=%q", p.Ready(), p.Key)
	}
	if p.Responses != CodexBase {
		t.Errorf("codex Responses base = %q, want %q", p.Responses, CodexBase)
	}
	accts := Accounts()
	if len(accts) != 1 || accts[0].Provider != "codex" || accts[0].User != "me@example.com" || accts[0].Plan != "plus" {
		t.Errorf("Accounts() = %+v", accts)
	}
	if a, ok := AccountOf(*p); !ok || a.User != "me@example.com" {
		t.Errorf("AccountOf(codex) = %+v, %v", a, ok)
	}
	if !catalogHas("codex/gpt-5.5") {
		t.Errorf("codex/gpt-5.5 not in the catalog without any picks")
	}
}

func TestCodexAbsentOrAPIKeyIsNotAccount(t *testing.T) {
	home := isolate(t)
	if hasID(All(), "codex") {
		t.Fatal("codex listed with no login file")
	}
	writeFile(t, codexAuthPathFor(home), `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test"}`)
	if hasID(All(), "codex") {
		t.Fatal("codex listed for an API-key login")
	}
	if len(Accounts()) != 0 {
		t.Fatalf("Accounts() = %+v, want none", Accounts())
	}
}

func TestClaudeNeverProvidedButNoticed(t *testing.T) {
	isolate(t)
	dir := filepath.Join(t.TempDir(), "claude-cfg")
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if len(Notices()) != 0 {
		t.Fatal("notice shown without a Claude login")
	}
	writeFile(t, filepath.Join(dir, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"SECRET-CLAUDE-TOKEN"}}`)

	for _, p := range All() {
		if p.ID == "claude" {
			t.Fatal("claude listed in All()")
		}
	}
	if _, err := Find("claude"); err == nil {
		t.Fatal("Find(claude) succeeded")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "claude/") {
			t.Fatalf("claude model %q in catalog", e.ID)
		}
	}
	ns := Notices()
	if len(ns) != 1 || ns[0].Agent != "Claude Code" || ns[0].Reason == "" {
		t.Fatalf("Notices() = %+v", ns)
	}
	if b, _ := json.Marshal(ns); strings.Contains(string(b), "SECRET") {
		t.Fatal("notice leaks the Claude token")
	}
	if err := Save(Provider{ID: "claude", Name: "Claude", Anthropic: "https://api.anthropic.com"}); err == nil {
		t.Fatal("Save(claude) should fail")
	}
}

func TestStrayClaudeRecordIgnored(t *testing.T) {
	home := isolate(t)
	writeFile(t, filepath.Join(home, "xdg-config", "dial", "providers.json"),
		`{"providers":[{"id":"claude","models":["x"]}]}`)
	if hasID(All(), "claude") {
		t.Fatal("stored claude entry became a provider")
	}
}

func TestSaveAccountWritesOnlyModels(t *testing.T) {
	home := isolate(t)
	writeFile(t, codexAuthPathFor(home), codexLoginJSON(t, time.Now().Add(time.Hour), "rt-secret"))

	err := Save(Provider{ID: "codex", Name: "Whatever", Key: "typed-key", Models: []string{"gpt-5.5", "gpt-5.5"}})
	if err != nil {
		t.Fatal(err)
	}
	data := readFile(t, filepath.Join(home, "xdg-config", "dial", "providers.json"))
	for _, bad := range []string{"typed-key", "Whatever", `"name"`, `"key"`, "rt-secret", "eyJ"} {
		if strings.Contains(data, bad) {
			t.Errorf("providers.json contains %q:\n%s", bad, data)
		}
	}
	if !strings.Contains(data, `"gpt-5.5"`) {
		t.Errorf("picks not saved:\n%s", data)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Errorf("Models = %v", p.Models)
	}
}

func TestDeleteAccountOnlyClearsPicks(t *testing.T) {
	home := isolate(t)
	authPath := codexAuthPathFor(home)
	writeFile(t, authPath, codexLoginJSON(t, time.Now().Add(time.Hour), "rt-1"))
	before := readFile(t, authPath)

	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.3-codex"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatalf("Delete(codex): %v", err)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatalf("codex vanished after Delete while still signed in: %v", err)
	}
	if len(p.Models) != 0 {
		t.Errorf("picks not cleared: %v", p.Models)
	}
	if readFile(t, authPath) != before {
		t.Error("Delete touched the Codex login file")
	}
}

func TestStalePicksIgnoredWhenLoggedOut(t *testing.T) {
	home := isolate(t)
	writeFile(t, filepath.Join(home, "xdg-config", "dial", "providers.json"),
		`{"providers":[{"id":"codex","models":["gpt-5.5"]},{"id":"copilot","models":["gpt-4.1"]}]}`)
	if hasID(All(), "codex") || hasID(All(), "copilot") {
		t.Fatalf("logged-out account listed: %v", providerIDs(All()))
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Fatal("stale pick resolved to a provider")
	}
	if catalogHas("codex/gpt-5.5") || catalogHas("copilot/gpt-4.1") {
		t.Fatal("stale pick in the catalog")
	}
}

func TestCopilotBuiltinListHasNoAuto(t *testing.T) {
	ms := copilotBuiltin()
	if len(ms) == 0 {
		t.Fatal("empty built-in Copilot list")
	}
	for _, m := range ms {
		if m.ID == "auto" || strings.HasPrefix(m.ID, "auto") {
			t.Errorf("built-in Copilot list contains %q", m.ID)
		}
	}
}

func TestCopilotSignedInUsesBuiltinList(t *testing.T) {
	home := isolate(t)
	writeFile(t, copilotAppsPathFor(home), `{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_x"}}`)

	p, err := Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Ready() {
		t.Error("copilot should be ready")
	}
	if a, ok := AccountOf(*p); !ok || a.User != "octo" {
		t.Errorf("AccountOf(copilot) = %+v, %v", a, ok)
	}
	for _, m := range p.Available() {
		if m.ID == "auto" {
			t.Error("auto offered for Copilot")
		}
	}
	if !catalogHas("copilot/" + copilotBuiltin()[0].ID) {
		t.Error("built-in Copilot models missing from the catalog")
	}
}

// fakeCopilot serves the token exchange and the model list.
func fakeCopilot(t *testing.T, tokenStatus int) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "token gho_TOKEN" {
			t.Errorf("token exchange auth = %q", got)
		}
		if tokenStatus != 0 {
			w.WriteHeader(tokenStatus)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"token":     "sess-1",
			"endpoints": map[string]any{"api": srv.URL},
		})
	})
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sess-1" {
			t.Errorf("models auth = %q", got)
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": "gpt-4.1", "name": "GPT-4.1", "capabilities": map[string]any{"type": "chat"}, "model_picker_enabled": true, "policy": map[string]any{"state": "enabled"}},
			{"id": "auto", "capabilities": map[string]any{"type": "chat"}, "model_picker_enabled": true},
			{"id": "text-embedding-3-small", "capabilities": map[string]any{"type": "embeddings"}, "model_picker_enabled": true},
			{"id": "internal-router", "capabilities": map[string]any{"type": "chat"}, "model_picker_enabled": false},
			{"id": "claude-opus-x", "capabilities": map[string]any{"type": "chat"}, "model_picker_enabled": true, "policy": map[string]any{"state": "disabled"}},
			{"id": "gemini-2.5-pro", "name": "Gemini 2.5 Pro", "capabilities": map[string]any{"type": "chat"}, "model_picker_enabled": true},
		}})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := copilotTokenURL
	copilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { copilotTokenURL = old })
	return srv
}

func TestCopilotRefreshKeepsOnlyUsableChatModels(t *testing.T) {
	home := isolate(t)
	writeFile(t, copilotAppsPathFor(home), `{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_TOKEN"}}`)
	fakeCopilot(t, 0)

	p, err := Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "gpt-4.1,gemini-2.5-pro" {
		t.Fatalf("refreshed ids = %v", ids)
	}
	if _, _, ok := catalog.Live("copilot-octo"); !ok {
		t.Fatal("refreshed list not saved")
	}
	p, _ = Find("copilot")
	live := p.Available()
	if len(live) != 2 || live[0].ID != "gpt-4.1" {
		t.Errorf("Available after refresh = %+v", live)
	}
	if catalogHas("copilot/auto") || !catalogHas("copilot/gpt-4.1") {
		t.Errorf("catalog after refresh wrong: %v", Catalog())
	}
}

func TestCopilotRejectedSessionAsksToRelogin(t *testing.T) {
	home := isolate(t)
	writeFile(t, copilotAppsPathFor(home), `{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_TOKEN"}}`)
	fakeCopilot(t, http.StatusUnauthorized)

	p, _ := Find("copilot")
	_, err := p.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Fatalf("Fetch error = %v, want a request to sign in to Copilot again", err)
	}
}

func TestCodexRefreshWritesBackAndKeepsOtherFields(t *testing.T) {
	home := isolate(t)
	authPath := codexAuthPathFor(home)
	expired := time.Now().Add(-time.Hour)
	writeFile(t, authPath, codexLoginJSON(t, expired, "rt-old"))

	var calls int32
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "NEW-ACCESS",
			"id_token":      idToken("me@example.com"),
			"refresh_token": "rt-new",
		})
	}))
	t.Cleanup(srv.Close)
	old := codexTokenURL
	codexTokenURL = srv.URL + "/oauth/token"
	t.Cleanup(func() { codexTokenURL = old })

	tok, acct, err := CodexCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "NEW-ACCESS" || acct != "acct-1" {
		t.Errorf("got token %q account %q", tok, acct)
	}
	if gotBody["grant_type"] != "refresh_token" || gotBody["refresh_token"] != "rt-old" || gotBody["client_id"] != codexClientID {
		t.Errorf("refresh request body = %v", gotBody)
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(readFile(t, authPath)), &doc); err != nil {
		t.Fatal(err)
	}
	tokens := doc["tokens"].(map[string]any)
	if tokens["access_token"] != "NEW-ACCESS" || tokens["refresh_token"] != "rt-new" || tokens["account_id"] != "acct-1" {
		t.Errorf("tokens not written back: %v", tokens)
	}
	if _, ok := doc["OPENAI_API_KEY"]; !ok {
		t.Error("unrelated field OPENAI_API_KEY was dropped")
	}
	if doc["auth_mode"] != "chatgpt" {
		t.Errorf("auth_mode changed: %v", doc["auth_mode"])
	}

	// the fresh token is not refreshed again
	if _, _, err := CodexCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("token endpoint called %d times, want 1", n)
	}
}

func TestCodexRefreshFailureSaysSignedOut(t *testing.T) {
	home := isolate(t)
	authPath := codexAuthPathFor(home)
	expired := time.Now().Add(-time.Hour)
	writeFile(t, authPath, codexLoginJSON(t, expired, "rt-dead"))
	before := readFile(t, authPath)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	old := codexTokenURL
	codexTokenURL = srv.URL + "/oauth/token"
	t.Cleanup(func() { codexTokenURL = old })

	_, _, err := CodexCredentials(context.Background())
	if err == nil || !strings.Contains(err.Error(), "已登出") || !strings.Contains(err.Error(), "codex login") {
		t.Fatalf("error = %v", err)
	}
	if readFile(t, authPath) != before {
		t.Error("failed refresh overwrote the login file")
	}
}

func TestCodexBaseOverrideReachesProvider(t *testing.T) {
	home := isolate(t)
	writeFile(t, codexAuthPathFor(home), codexLoginJSON(t, time.Now().Add(time.Hour), "rt-1"))
	if CodexBase != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("default CodexBase = %q", CodexBase)
	}
	old := CodexBase
	CodexBase = "http://127.0.0.1:9/backend"
	t.Cleanup(func() { CodexBase = old })
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if p.Responses != "http://127.0.0.1:9/backend" {
		t.Errorf("Responses = %q", p.Responses)
	}
}
