package provider_test

import (
	"context"
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

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	return home
}

func codexLogin(t *testing.T, home string) {
	exp := float64(time.Now().Add(time.Hour).Unix())
	writeJSON(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      jwt(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-1"}}),
			"access_token":  jwt(map[string]any{"exp": exp}),
			"refresh_token": "r", "account_id": "acct-1",
		},
	})
}

func copilotLogin(t *testing.T, home string) { copilotLoginTok(t, home, "gho_x") }

func copilotLoginTok(t *testing.T, home, tok string) {
	writeJSON(t, filepath.Join(home, ".config", "github-copilot", "apps.json"), map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "octocat", "oauth_token": tok},
	})
}

func find(id string) (provider.Provider, bool) {
	for _, p := range provider.All() {
		if p.ID == id {
			return p, true
		}
	}
	return provider.Provider{}, false
}

func has(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestAcceptAccountsAppearAsProviders(t *testing.T) {
	home := isolate(t)
	codexLogin(t, home)
	copilotLogin(t, home)

	codex, ok := find("codex")
	if !ok || codex.Account == nil || codex.Account.User != "me@example.com" || codex.Account.Plan != "pro" || codex.Account.Agent != "codex" || !codex.Ready() {
		t.Fatalf("codex: %+v", codex)
	}
	cp, ok := find("copilot")
	if !ok || cp.Account == nil || cp.Account.User != "octocat" || cp.Account.Agent != "copilot" || !cp.Ready() {
		t.Fatalf("copilot: %+v", cp)
	}
	if len(provider.Accounts()) != 2 {
		t.Fatalf("Accounts: %+v", provider.Accounts())
	}
	var ids []string
	for _, e := range provider.Catalog() {
		ids = append(ids, e.ID)
	}
	if !has(ids, "codex/gpt-5.5") || !has(ids, "copilot/claude-sonnet-4.5") || has(ids, "copilot/auto") {
		t.Fatalf("catalog: %v", ids)
	}
	p, model, ok := provider.Resolve("codex/gpt-5.5")
	if !ok || p.ID != "codex" || model != "gpt-5.5" || p.Account == nil {
		t.Fatalf("resolve: %+v %q %v", p, model, ok)
	}
	// no tokens leak through the JSON form
	b, _ := json.Marshal(codex)
	if strings.Contains(string(b), ".sig") {
		t.Fatalf("json: %s", b)
	}
}

func TestAcceptAccountFallbackNames(t *testing.T) {
	home := isolate(t)
	exp := float64(time.Now().Add(time.Hour).Unix())
	writeJSON(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens":    map[string]any{"id_token": jwt(map[string]any{}), "access_token": jwt(map[string]any{"exp": exp}), "refresh_token": "r", "account_id": "a"},
	})
	writeJSON(t, filepath.Join(home, ".config", "github-copilot", "hosts.json"), map[string]any{
		"github.com": map[string]any{"oauth_token": "gho_x"},
	})
	if c, ok := find("codex"); !ok || c.Account == nil || c.Account.User != "ChatGPT" {
		t.Fatalf("codex: %+v", c)
	}
	if c, ok := find("copilot"); !ok || c.Account == nil || c.Account.User != "GitHub" {
		t.Fatalf("copilot: %+v", c)
	}
}

func TestAcceptAccountPicksStorage(t *testing.T) {
	home := isolate(t)
	codexLogin(t, home)
	copilotLogin(t, home)

	if err := provider.Save(provider.Provider{ID: "codex", Name: "x", Chat: "https://ignored.example", Key: "ignored-key", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(provider.Path())
	s := string(b)
	if strings.Contains(s, "ignored") || strings.Contains(s, "h.") || !strings.Contains(s, "gpt-5.5") {
		t.Fatalf("stored: %s", s)
	}
	c, _ := find("codex")
	if ex := c.Exposed(); len(ex) != 1 || ex[0].ID != "gpt-5.5" {
		t.Fatalf("exposed: %+v", ex)
	}
	if c.Account == nil || c.Account.User != "me@example.com" {
		t.Fatalf("account lost: %+v", c)
	}
	if err := provider.Delete("codex"); err != nil {
		t.Fatal(err)
	}
	c, ok := find("codex")
	if !ok || c.Account == nil || len(c.Models) != 0 {
		t.Fatalf("after delete: %+v %v", c, ok)
	}
}

func TestAcceptSignedOutAndAPIKeyMode(t *testing.T) {
	home := isolate(t)
	codexLogin(t, home)
	copilotLogin(t, home)
	provider.Save(provider.Provider{ID: "copilot", Models: []string{"gpt-5.5"}})
	provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5"}})

	os.Remove(filepath.Join(home, ".config", "github-copilot", "apps.json"))
	if _, ok := find("copilot"); ok {
		t.Fatal("copilot still listed after sign-out")
	}
	for _, e := range provider.Catalog() {
		if strings.HasPrefix(e.ID, "copilot/") {
			t.Fatalf("catalog still has %s", e.ID)
		}
	}
	// API-key mode is not an account
	writeJSON(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "sk-x"})
	if _, ok := find("codex"); ok {
		t.Fatal("codex api-key mode treated as account")
	}
	os.Remove(filepath.Join(home, ".codex", "auth.json"))
	if _, ok := find("codex"); ok {
		t.Fatal("codex listed after sign-out")
	}
}

func TestAcceptUserProviderWinsOverAccount(t *testing.T) {
	home := isolate(t)
	codexLogin(t, home)
	writeJSON(t, provider.Path(), map[string]any{"providers": []map[string]any{
		{"id": "codex", "name": "Mine", "key": "k", "chat": "https://my.example/v1", "models": []string{"m"}},
	}})
	n := 0
	for _, p := range provider.All() {
		if p.ID == "codex" {
			n++
			if p.Account != nil || p.Chat != "https://my.example/v1" {
				t.Fatalf("got %+v", p)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d codex providers", n)
	}
}

// Excluded is cached for about a minute, so this is the only test here that calls it.
func TestAcceptClaudeCodeExcluded(t *testing.T) {
	home := isolate(t)
	writeJSON(t, filepath.Join(home, ".claude", ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{"accessToken": "sk-ant-oat-secret"}})
	ex := provider.Excluded()
	if len(ex) != 1 || ex[0].Agent != "claude" || ex[0].Why == "" {
		t.Fatalf("excluded: %+v", ex)
	}
	if strings.Contains(ex[0].Why, "sk-ant-oat-secret") {
		t.Fatal("credential leaked into reason")
	}
	for _, p := range provider.All() {
		if p.ID == "claude" || p.ID == "anthropic-account" {
			t.Fatalf("claude offered as provider: %+v", p)
		}
	}
	if _, _, ok := provider.Resolve("claude/claude-sonnet-4.5"); ok {
		t.Fatal("claude resolves")
	}
}

func TestAcceptCodexSignAndPrepare(t *testing.T) {
	home := isolate(t)
	codexLogin(t, home)
	p, _ := find("codex")
	req, _ := http.NewRequest("POST", "http://example.invalid/responses", nil)
	if err := p.Sign(context.Background(), req, provider.Responses, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer h.") || req.Header.Get("chatgpt-account-id") != "acct-1" {
		t.Fatalf("headers: %v", req.Header)
	}
	out := p.Prepare([]byte(`{"model":"gpt-5.5","input":"hi","max_output_tokens":5,"temperature":0.1,"stream":false,"store":true,"reasoning":{"effort":"low"}}`))
	var m map[string]any
	json.Unmarshal(out, &m)
	if _, ok := m["max_output_tokens"]; ok || m["temperature"] != nil || m["stream"] != true || m["store"] != false || m["reasoning"] == nil {
		t.Fatalf("body: %s", out)
	}
	in, ok := m["input"].([]any)
	if !ok || len(in) == 0 || !strings.Contains(string(out), `"hi"`) || in[0].(map[string]any)["role"] != "user" {
		t.Fatalf("input: %s", out)
	}
	// plain providers: key auth, body untouched
	plain := provider.Provider{ID: "x", Key: "sk-1", Chat: "https://x/v1"}
	r2, _ := http.NewRequest("POST", "https://x/v1/chat/completions", nil)
	if err := plain.Sign(context.Background(), r2, provider.Chat, nil); err != nil || r2.Header.Get("Authorization") != "Bearer sk-1" {
		t.Fatalf("plain sign: %v %v", err, r2.Header)
	}
	raw := []byte(`{"a":1,"temperature":1}`)
	if string(plain.Prepare(raw)) != string(raw) {
		t.Fatal("plain prepare changed body")
	}
}

func TestAcceptCopilotModelsAndSign(t *testing.T) {
	home := isolate(t)
	copilotLoginTok(t, home, "gho_models")
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess" || r.Header.Get("Copilot-Integration-Id") == "" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[
			  {"id":"gpt-5.5","name":"GPT-5.5","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}}},
			  {"id":"claude-sonnet-5","name":"Claude Sonnet 5","policy":{"state":"disabled"},"capabilities":{"type":"chat"}},
			  {"id":"gpt-4.1","name":"GPT-4.1","policy":{"state":"enabled"},"capabilities":{"type":"chat"}},
			  {"id":"exec-agent-a","name":"Exec","model_picker_enabled":true,"capabilities":{"type":"chat"}},
			  {"id":"text-embedding-3-small","name":"Emb","model_picker_enabled":true,"capabilities":{"type":"embeddings"}}]}`))
			return
		}
		w.Write([]byte(`{"path":"` + r.URL.Path + `","initiator":"` + r.Header.Get("X-Initiator") + `"}`))
	}))
	defer api.Close()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_models" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	defer tokens.Close()
	old := provider.CopilotTokenURL
	provider.CopilotTokenURL = tokens.URL
	defer func() { provider.CopilotTokenURL = old }()

	p, _ := find("copilot")
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
	p, _ = find("copilot")
	if got := p.Available(); len(got) != 2 || got[0].ID != "gpt-5.5" {
		t.Fatalf("available: %+v", got)
	}

	sign := func(body string) (*http.Request, map[string]string) {
		req, _ := http.NewRequest("POST", p.Chat+"/chat/completions", nil)
		if err := p.Sign(context.Background(), req, provider.Chat, []byte(body)); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(req.URL.String(), api.URL) || req.URL.Path != "/chat/completions" {
			t.Fatalf("signed url: %s", req.URL)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var got map[string]string
		json.NewDecoder(res.Body).Decode(&got)
		return req, got
	}
	_, got := sign(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	if got["initiator"] != "user" {
		t.Fatalf("user turn: %v", got)
	}
	_, got = sign(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`)
	if got["initiator"] != "agent" {
		t.Fatalf("tool turn: %v", got)
	}
}

func TestAcceptCopilotNoChatModelsIsError(t *testing.T) {
	home := isolate(t)
	copilotLoginTok(t, home, "gho_nochat")
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"emb","model_picker_enabled":true,"capabilities":{"type":"embeddings"}}]}`))
	}))
	defer api.Close()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	defer tokens.Close()
	old := provider.CopilotTokenURL
	provider.CopilotTokenURL = tokens.URL
	defer func() { provider.CopilotTokenURL = old }()
	p, _ := find("copilot")
	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("expected error with no chat models")
	}
}
