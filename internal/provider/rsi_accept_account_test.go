package provider_test

import (
	"context"
	"fmt"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func wfile(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func env(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

func codexLogin(t *testing.T, home string, mode, access string) {
	wfile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": mode,
		"tokens": map[string]any{
			"id_token":      jwt(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}}),
			"access_token":  access,
			"refresh_token": "r", "account_id": "acct-1",
		},
	})
}

// each login gets its own OAuth token so a cached session from an earlier
// test (whose servers are gone) is never reused
var (
	tokSeq int
	curTok string
)

func copilotLogin(home string, t *testing.T) {
	tokSeq++
	curTok = fmt.Sprintf("gho_%d", tokSeq)
	wfile(t, filepath.Join(home, ".config", "github-copilot", "apps.json"), map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "octocat", "oauth_token": curTok},
	})
}

func signIn(t *testing.T) string {
	home := env(t)
	codexLogin(t, home, "chatgpt", jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}))
	copilotLogin(home, t)
	return home
}

func get(ps []provider.Provider, id string) (provider.Provider, bool) {
	for _, p := range ps {
		if p.ID == id {
			return p, true
		}
	}
	return provider.Provider{}, false
}

func TestAcceptAccountsDiscovered(t *testing.T) {
	signIn(t)
	all := provider.All()
	c, ok := get(all, "codex")
	if !ok || c.Account == nil || c.Account.User != "me@example.com" || c.Account.Plan != "pro" || !c.Ready() || !c.Account.Stream || c.Responses == "" {
		t.Fatalf("codex: %+v", c)
	}
	k, ok := get(all, "copilot")
	if !ok || k.Account == nil || k.Account.User != "octocat" || !k.Ready() || k.Chat == "" {
		t.Fatalf("copilot: %+v", k)
	}
	acc := provider.Accounts()
	if len(acc) != 2 || acc[0].ID != "codex" || acc[1].ID != "copilot" {
		t.Fatalf("accounts: %+v", acc)
	}
}

func TestAcceptSignedOutAccounts(t *testing.T) {
	home := env(t)
	codexLogin(t, home, "apikey", jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}))
	if _, ok := get(provider.All(), "codex"); ok {
		t.Fatal("apikey mode should not list codex")
	}
	codexLogin(t, home, "chatgpt", "")
	if _, ok := get(provider.All(), "codex"); ok {
		t.Fatal("empty access token should not list codex")
	}
	// stale picks without a login are not providers
	copilotLogin(home, t)
	if err := provider.Save(provider.Provider{ID: "copilot", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := get(provider.All(), "copilot"); !ok {
		t.Fatal("copilot missing")
	}
	os.Remove(filepath.Join(home, ".config", "github-copilot", "apps.json"))
	if _, ok := get(provider.All(), "copilot"); ok {
		t.Fatal("copilot still listed after sign-out")
	}
}

func TestAcceptCatalogResolveSaveDelete(t *testing.T) {
	signIn(t)
	var ids []string
	for _, e := range provider.Catalog() {
		ids = append(ids, e.ID)
	}
	has := func(x string) bool {
		for _, i := range ids {
			if i == x {
				return true
			}
		}
		return false
	}
	if !has("codex/gpt-5.5") || !has("copilot/claude-sonnet-4.5") || has("copilot/auto") {
		t.Fatalf("catalog: %v", ids)
	}
	p, model, ok := provider.Resolve("codex/gpt-5.5")
	if !ok || p.ID != "codex" || model != "gpt-5.5" || p.Account == nil {
		t.Fatalf("resolve: %+v %q %v", p, model, ok)
	}

	if err := provider.Save(provider.Provider{ID: "codex", Name: "zzname", Chat: "https://ignored.example", Key: "ignoredkey", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(provider.Path())
	s := string(b)
	if strings.Contains(s, "ignored") || strings.Contains(s, "zzname") || strings.Contains(s, "me@example.com") || !strings.Contains(s, `"gpt-5.5"`) {
		t.Fatalf("stored: %s", s)
	}
	c, _ := get(provider.All(), "codex")
	if ex := c.Exposed(); len(ex) != 1 || ex[0].ID != "gpt-5.5" {
		t.Fatalf("exposed: %+v", ex)
	}
	// the account shows up once
	n := 0
	for _, q := range provider.All() {
		if q.ID == "codex" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("codex listed %d times", n)
	}
	if err := provider.Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if c, ok = get(provider.All(), "codex"); !ok || c.Account == nil || len(c.Models) != 0 {
		t.Fatalf("after delete: %+v", c)
	}
}

func TestAcceptPlainProviderStillValidated(t *testing.T) {
	env(t)
	if err := provider.Save(provider.Provider{ID: "plain", Name: "Plain", Key: "k"}); err == nil {
		t.Fatal("provider without base URL should be rejected")
	}
	if err := provider.Save(provider.Provider{ID: "plain", Name: "Plain", Chat: "https://x.example/v1", Key: "k", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	p, _ := get(provider.All(), "plain")
	if p.Account != nil || p.Key != "k" {
		t.Fatalf("plain: %+v", p)
	}
	req, _ := http.NewRequest("POST", "https://x.example/v1/chat/completions", nil)
	if err := p.Sign(context.Background(), req, provider.Chat, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer k" {
		t.Fatalf("headers: %v", req.Header)
	}
	body := []byte(`{"model":"m","max_tokens":5}`)
	if string(p.Prepare(body)) != string(body) {
		t.Fatal("plain Prepare should be identity")
	}
}

func TestAcceptCodexSignAndPrepare(t *testing.T) {
	signIn(t)
	p, _ := get(provider.All(), "codex")
	req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
	if err := p.Sign(context.Background(), req, provider.Responses, nil); err != nil {
		t.Fatal(err)
	}
	h := req.Header
	if !strings.HasPrefix(h.Get("Authorization"), "Bearer h.") || h.Get("chatgpt-account-id") != "acct-1" || h.Get("originator") != "dial" || h.Get("OpenAI-Beta") != "responses=experimental" {
		t.Fatalf("headers: %v", h)
	}
	out := p.Prepare([]byte(`{"model":"gpt-5.5","input":"hi","max_output_tokens":5,"max_completion_tokens":5,"temperature":0.1,"top_p":1,"user":"u","service_tier":"auto","previous_response_id":"x","stream":false,"store":true,"reasoning":{"effort":"low"}}`))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"max_output_tokens", "max_completion_tokens", "temperature", "top_p", "user", "service_tier", "previous_response_id"} {
		if _, ok := m[k]; ok {
			t.Fatalf("%s kept: %s", k, out)
		}
	}
	if m["stream"] != true || m["store"] != false || m["reasoning"] == nil {
		t.Fatalf("body: %s", out)
	}
	list, _ := m["input"].([]any)
	if len(list) != 1 {
		t.Fatalf("input: %s", out)
	}
	in := list[0].(map[string]any)
	c := in["content"].([]any)[0].(map[string]any)
	if in["type"] != "message" || in["role"] != "user" || c["type"] != "input_text" || c["text"] != "hi" {
		t.Fatalf("input: %s", out)
	}
	if bad := []byte(`not json`); string(p.Prepare(bad)) != string(bad) {
		t.Fatal("unparseable body should be returned as is")
	}
}

func TestAcceptCodexSignedOutErrors(t *testing.T) {
	home := signIn(t)
	p, _ := get(provider.All(), "codex")
	os.Remove(filepath.Join(home, ".codex", "auth.json"))
	req, _ := http.NewRequest("POST", "http://x/responses", nil)
	err := p.Sign(context.Background(), req, provider.Responses, nil)
	if err == nil || !strings.Contains(err.Error(), "Codex is signed out") {
		t.Fatalf("err: %v", err)
	}
}

func copilotServers(t *testing.T) (*httptest.Server, *int) {
	hits := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess" || r.Header.Get("Copilot-Integration-Id") == "" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"data":[
		  {"id":"gpt-5.5","name":"GPT-5.5","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}}},
		  {"id":"claude-sonnet-5","name":"Claude Sonnet 5","policy":{"state":"disabled"},"capabilities":{"type":"chat"}},
		  {"id":"gpt-4.1","name":"GPT-4.1","policy":{"state":"enabled"},"capabilities":{"type":"chat"}},
		  {"id":"exec-agent-a","name":"Exec","model_picker_enabled":true,"capabilities":{"type":"chat"}},
		  {"id":"text-embedding-3-small","name":"Emb","model_picker_enabled":true,"capabilities":{"type":"embeddings"}}]}`))
	}))
	t.Cleanup(api.Close)
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "token "+curTok {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	t.Cleanup(tokens.Close)
	old := provider.CopilotTokenURL
	provider.CopilotTokenURL = tokens.URL
	t.Cleanup(func() { provider.CopilotTokenURL = old })
	return api, &hits
}

func TestAcceptCopilotFetchModels(t *testing.T) {
	signIn(t)
	copilotServers(t)
	p, _ := get(provider.All(), "copilot")
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "gpt-5.5,gpt-4.1" || len(ms[0].Efforts) != 2 {
		t.Fatalf("models: %v %+v", ids, ms)
	}
	p, _ = get(provider.All(), "copilot")
	if got := p.Available(); len(got) != 2 || got[0].ID != "gpt-5.5" || got[1].ID != "gpt-4.1" {
		t.Fatalf("available: %+v", got)
	}
}

func TestAcceptCopilotSign(t *testing.T) {
	signIn(t)
	api, _ := copilotServers(t)
	p, _ := get(provider.All(), "copilot")
	sign := func(body string) *http.Request {
		req, _ := http.NewRequest("POST", p.Chat+"/chat/completions", nil)
		if err := p.Sign(context.Background(), req, provider.Chat, []byte(body)); err != nil {
			t.Fatal(err)
		}
		return req
	}
	req := sign(`{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`)
	if !strings.HasPrefix(req.URL.String(), api.URL) || req.URL.Path != "/chat/completions" ||
		req.Header.Get("Authorization") != "Bearer sess" || req.Header.Get("Copilot-Integration-Id") == "" ||
		req.Header.Get("X-Initiator") != "agent" || req.Header.Get("Copilot-Vision-Request") != "" {
		t.Fatalf("signed: %s %v", req.URL, req.Header)
	}
	req = sign(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:x"}}]}]}`)
	if req.Header.Get("X-Initiator") != "user" || req.Header.Get("Copilot-Vision-Request") != "true" {
		t.Fatalf("vision: %v", req.Header)
	}
}

func TestAcceptCopilotSignedOut(t *testing.T) {
	signIn(t)
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer tokens.Close()
	old := provider.CopilotTokenURL
	provider.CopilotTokenURL = tokens.URL
	defer func() { provider.CopilotTokenURL = old }()
	p, _ := get(provider.All(), "copilot")
	req, _ := http.NewRequest("POST", p.Chat+"/chat/completions", nil)
	err := p.Sign(context.Background(), req, provider.Chat, []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "Copilot is signed out") {
		t.Fatalf("err: %v", err)
	}
}

func TestAcceptClaudeExcluded(t *testing.T) {
	home := env(t)
	dir := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	wfile(t, filepath.Join(dir, ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{"accessToken": "x"}})
	var found *provider.Exclusion
	for _, e := range provider.Excluded() {
		if e.Agent == "claude" {
			e := e
			found = &e
		}
	}
	if found == nil || !strings.Contains(found.Why, "Anthropic") || !strings.Contains(found.Why, "API key") {
		t.Fatalf("excluded: %+v", provider.Excluded())
	}
	if _, ok := get(provider.All(), "claude"); ok {
		t.Fatal("claude must not be a provider")
	}
}
