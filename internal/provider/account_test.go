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

func fakeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(b) + ".sig"
}

// home points every agent and dial file at temp dirs.
func home(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(h, "claude-none"))
	accountLiveMu.Lock()
	accountLive = map[string]liveList{}
	accountLiveMu.Unlock()
	return h
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

func codexLogin(t *testing.T, h string, accessExp time.Time) {
	t.Helper()
	id := fakeJWT(map[string]any{"email": "me@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-from-jwt"}})
	access := fakeJWT(map[string]any{"exp": accessExp.Unix()})
	b, _ := json.Marshal(map[string]any{"OPENAI_API_KEY": nil, "auth_mode": "chatgpt", "extra": "keep me",
		"tokens":       map[string]any{"id_token": id, "access_token": access, "refresh_token": "rt-old", "account_id": "acct-1"},
		"last_refresh": "2026-01-01T00:00:00Z"})
	writeFile(t, filepath.Join(h, ".codex", "auth.json"), string(b))
}

func copilotLoginFile(t *testing.T, h string) {
	t.Helper()
	writeFile(t, filepath.Join(h, ".config", "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octocat","oauth_token":"gho_secret","githubAppId":"Iv1.abc"}}`)
}

func find(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func TestAccountsListed(t *testing.T) {
	h := home(t)
	codexLogin(t, h, time.Now().Add(time.Hour))
	copilotLoginFile(t, h)
	acc := Accounts()
	c, g := find(acc, "codex"), find(acc, "copilot")
	if c == nil || g == nil {
		t.Fatalf("accounts: %+v", acc)
	}
	if c.Account.User != "me@example.com" || c.Account.Plan != "pro" || c.Account.Agent != "codex" || !c.Ready() {
		t.Errorf("codex: %+v %+v", c, c.Account)
	}
	if g.Account.User != "octocat" || g.Account.Agent != "copilot" || !g.Ready() {
		t.Errorf("copilot: %+v", g.Account)
	}
	if find(All(), "codex") == nil || find(All(), "copilot") == nil {
		t.Error("All misses accounts")
	}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "rt-old") || !strings.Contains(string(b), `"account":{"agent":"codex","user":"me@example.com","plan":"pro"}`) {
		t.Errorf("json: %s", b)
	}
	ids := map[string]bool{}
	for _, e := range Catalog() {
		ids[e.ID] = true
	}
	if !ids["codex/gpt-5.5"] || !ids["copilot/claude-sonnet-4.5"] || ids["copilot/auto"] {
		t.Errorf("catalog: %v", ids)
	}
	p, m, ok := Resolve("codex/gpt-5.5")
	if !ok || p.ID != "codex" || m != "gpt-5.5" {
		t.Errorf("resolve: %v %v %v", p.ID, m, ok)
	}
}

func TestAccountDefaultNames(t *testing.T) {
	h := home(t)
	writeFile(t, filepath.Join(h, ".codex", "auth.json"), `{"tokens":{"access_token":"x"}}`)
	writeFile(t, filepath.Join(h, ".config", "github-copilot", "hosts.json"), `{"github.com":{"oauth_token":"gho"}}`)
	acc := Accounts()
	if c := find(acc, "codex"); c == nil || c.Account.User != "ChatGPT" || c.Account.Plan != "" {
		t.Errorf("codex: %+v", c)
	}
	if g := find(acc, "copilot"); g == nil || g.Account.User != "GitHub" {
		t.Errorf("copilot: %+v", g)
	}
}

func TestAccountSaveKeepsOnlyModels(t *testing.T) {
	h := home(t)
	codexLogin(t, h, time.Now().Add(time.Hour))
	if err := Save(Provider{ID: "codex", Name: "X", Chat: "https://evil.example.com/v1", Key: "sk-secret", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	s := string(b)
	if strings.Contains(s, "evil") || strings.Contains(s, "sk-secret") || strings.Contains(s, `"key"`) || strings.Contains(s, `"name"`) || !strings.Contains(s, "gpt-5.5") {
		t.Errorf("stored: %s", s)
	}
	p, err := Find("codex")
	if err != nil || p.Account == nil || p.Chat != "" {
		t.Fatalf("find: %+v %v", p, err)
	}
	if ex := p.Exposed(); len(ex) != 1 || ex[0].ID != "gpt-5.5" {
		t.Errorf("exposed: %+v", ex)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p, err = Find("codex")
	if err != nil || len(p.Models) != 0 || len(p.Exposed()) != len(p.Available()) {
		t.Errorf("after delete: %+v %v", p, err)
	}
}

func TestSignOutRemovesAccount(t *testing.T) {
	h := home(t)
	codexLogin(t, h, time.Now().Add(time.Hour))
	copilotLoginFile(t, h)
	Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}})
	Save(Provider{ID: "copilot", Models: []string{"gpt-5.5"}})
	os.Remove(filepath.Join(h, ".codex", "auth.json"))
	os.Remove(filepath.Join(h, ".config", "github-copilot", "apps.json"))
	if p := find(All(), "codex"); p != nil {
		t.Errorf("codex still listed: %+v", p)
	}
	if p := find(All(), "copilot"); p != nil {
		t.Errorf("copilot still listed: %+v", p)
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") || strings.HasPrefix(e.ID, "copilot/") {
			t.Errorf("catalog still has %s", e.ID)
		}
	}
	// API-key mode is not an account
	writeFile(t, filepath.Join(h, ".codex", "auth.json"), `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-x","tokens":{"access_token":"a"}}`)
	if len(Accounts()) != 0 {
		t.Errorf("api key mode counted: %+v", Accounts())
	}
}

func TestCustomProviderWins(t *testing.T) {
	h := home(t)
	if err := Save(Provider{ID: "codex", Name: "My Codex", Chat: "https://relay.example.com/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	codexLogin(t, h, time.Now().Add(time.Hour))
	n := 0
	for _, p := range All() {
		if p.ID == "codex" {
			n++
			if p.Account != nil || p.Chat != "https://relay.example.com/v1" {
				t.Errorf("wrong winner: %+v", p)
			}
		}
	}
	if n != 1 || len(Accounts()) != 0 {
		t.Errorf("n=%d accounts=%v", n, Accounts())
	}
}

func TestPrepareCodex(t *testing.T) {
	p := Provider{ID: "codex", Account: &Account{Agent: "codex"}}
	out := p.Prepare([]byte(`{"model":"gpt-5.5","input":"hi","max_output_tokens":16,"temperature":0.2,"top_p":1,"reasoning":{"effort":"high"},"tools":[]}`))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["stream"] != true || m["store"] != false || m["max_output_tokens"] != nil || m["temperature"] != nil || m["top_p"] != nil {
		t.Errorf("prepared: %s", out)
	}
	in, _ := m["input"].([]any)
	if len(in) != 1 || in[0].(map[string]any)["role"] != "user" ||
		in[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "hi" {
		t.Errorf("input: %s", out)
	}
	if m["reasoning"].(map[string]any)["effort"] != "high" || m["tools"] == nil {
		t.Errorf("kept fields: %s", out)
	}
	plain := []byte(`{"model":"x","input":"hi","temperature":1}`)
	if got := (Provider{ID: "x"}).Prepare(plain); string(got) != string(plain) {
		t.Errorf("plain changed: %s", got)
	}
}

func TestCodexSignAndRefresh(t *testing.T) {
	h := home(t)
	codexLogin(t, h, time.Now().Add(2*time.Minute))
	newAccess := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	var calls int32
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"rt-new","id_token":"`+fakeJWT(map[string]any{"email": "new@example.com"})+`"}`)
	}))
	defer srv.Close()
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	defer func() { CodexRefreshURL = old }()

	p := find(Accounts(), "codex")
	if p == nil {
		t.Fatal("no codex")
	}
	req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
	if err := p.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer "+newAccess || req.Header.Get("chatgpt-account-id") != "acct-1" {
		t.Errorf("headers: %v", req.Header)
	}
	if got["grant_type"] != "refresh_token" || got["refresh_token"] != "rt-old" || got["client_id"] == "" {
		t.Errorf("refresh request: %v", got)
	}
	path := filepath.Join(h, ".codex", "auth.json")
	b, _ := os.ReadFile(path)
	var f map[string]any
	json.Unmarshal(b, &f)
	tok := f["tokens"].(map[string]any)
	if tok["access_token"] != newAccess || tok["refresh_token"] != "rt-new" || tok["account_id"] != "acct-1" || f["extra"] != "keep me" || f["last_refresh"] == "2026-01-01T00:00:00Z" {
		t.Errorf("auth.json: %s", b)
	}
	if _, ok := f["OPENAI_API_KEY"]; !ok {
		t.Errorf("lost field: %s", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	// fresh now: no second refresh
	req, _ = http.NewRequest("POST", p.Responses+"/responses", nil)
	p.Sign(context.Background(), req, Responses, nil)
	if calls != 1 {
		t.Errorf("refreshed %d times", calls)
	}
}

func TestCodexRefreshFails(t *testing.T) {
	h := home(t)
	codexLogin(t, h, time.Now().Add(time.Minute))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"refresh_token_reused"}}`)
	}))
	defer srv.Close()
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	defer func() { CodexRefreshURL = old }()
	p := find(Accounts(), "codex")
	req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
	err := p.Sign(context.Background(), req, Responses, nil)
	if err == nil || !errors.Is(err, ErrSignedOut) || !strings.Contains(err.Error(), "codex login") {
		t.Errorf("err: %v", err)
	}
}

func copilotServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var exchanges int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.Header.Get("Authorization") != "token gho_secret" {
				w.WriteHeader(401)
				return
			}
			atomic.AddInt32(&exchanges, 1)
			json.NewEncoder(w).Encode(map[string]any{"token": "sess-1", "expires_at": time.Now().Add(time.Hour).Unix(),
				"endpoints": map[string]any{"api": srv.URL + "/api"}})
		case "/api/models":
			if r.Header.Get("Authorization") != "Bearer sess-1" || r.Header.Get("Copilot-Integration-Id") == "" {
				w.WriteHeader(401)
				return
			}
			io.WriteString(w, `{"data":[
			  {"id":"gpt-5.5","name":"GPT-5.5","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}}},
			  {"id":"text-embedding-3","model_picker_enabled":true,"capabilities":{"type":"embeddings"}},
			  {"id":"gpt-4o-secondary","model_picker_enabled":true,"capabilities":{"type":"chat"}},
			  {"id":"copilot-search-x","model_picker_enabled":true,"capabilities":{"type":"chat"}},
			  {"id":"claude-off","model_picker_enabled":false,"policy":{"state":"disabled"},"capabilities":{"type":"chat"}},
			  {"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","model_picker_enabled":false,"policy":{"state":"enabled"},"capabilities":{"type":"chat"}},
			  {"id":"hidden","capabilities":{"type":"chat"}}
			]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	t.Cleanup(func() { CopilotTokenURL = old })
	return srv, &exchanges
}

func TestCopilotModelsAndSign(t *testing.T) {
	h := home(t)
	copilotLoginFile(t, h)
	srv, exchanges := copilotServer(t)
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
	if strings.Join(ids, ",") != "gpt-5.5,claude-sonnet-4.5" || strings.Join(ms[0].Efforts, ",") != "low,high" {
		t.Errorf("models: %+v", ms)
	}
	p, _ = Find("copilot")
	if av := p.Available(); len(av) != 2 || av[1].ID != "claude-sonnet-4.5" {
		t.Errorf("available: %+v", av)
	}
	sign := func(body string) *http.Request {
		req, _ := http.NewRequest("POST", p.Chat+"/chat/completions", strings.NewReader(body))
		if err := p.Sign(context.Background(), req, Chat, []byte(body)); err != nil {
			t.Fatal(err)
		}
		return req
	}
	req := sign(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:"}}]}]}`)
	if req.URL.String() != srv.URL+"/api/chat/completions" || req.Host != strings.TrimPrefix(srv.URL, "http://") {
		t.Errorf("url: %s host %s", req.URL, req.Host)
	}
	if req.Header.Get("Authorization") != "Bearer sess-1" || req.Header.Get("Copilot-Integration-Id") == "" ||
		req.Header.Get("Editor-Version") == "" || req.Header.Get("X-Initiator") != "user" || req.Header.Get("Copilot-Vision-Request") != "true" {
		t.Errorf("headers: %v", req.Header)
	}
	req = sign(`{"messages":[{"role":"user","content":"hi"},{"role":"tool","tool_call_id":"c","content":"ok"}]}`)
	if req.Header.Get("X-Initiator") != "agent" || req.Header.Get("Copilot-Vision-Request") != "" {
		t.Errorf("headers: %v", req.Header)
	}
	if *exchanges != 1 {
		t.Errorf("token exchanged %d times", *exchanges)
	}
}

func TestCopilotNoChatModels(t *testing.T) {
	h := home(t)
	copilotLoginFile(t, h)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			json.NewEncoder(w).Encode(map[string]any{"token": "sess-2", "expires_at": time.Now().Add(time.Hour).Unix(),
				"endpoints": map[string]any{"api": srv.URL}})
			return
		}
		io.WriteString(w, `{"data":[{"id":"emb","capabilities":{"type":"embeddings"},"model_picker_enabled":true}]}`)
	}))
	defer srv.Close()
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	defer func() { CopilotTokenURL = old }()
	p, _ := Find("copilot")
	if _, err := p.Fetch(context.Background()); err == nil {
		t.Error("expected an error")
	}
	if av := p.Available(); len(av) == 0 || av[0].ID == "auto" {
		t.Errorf("builtin fallback: %+v", av)
	}
}

func TestProviderTestUsesAccount(t *testing.T) {
	h := home(t)
	codexLogin(t, h, time.Now().Add(time.Hour))
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {}\n\n")
	}))
	defer srv.Close()
	old := CodexBase
	CodexBase = srv.URL
	defer func() { CodexBase = old }()
	p, _ := Find("codex")
	rs := p.Test(context.Background())
	if len(rs) != 1 || !rs[0].OK {
		t.Fatalf("results: %+v", rs)
	}
	if got["stream"] != true || got["max_output_tokens"] != nil || !strings.HasPrefix(auth, "Bearer ") {
		t.Errorf("probe: %v %s", got, auth)
	}
}

func TestExcludedClaude(t *testing.T) {
	h := home(t)
	if x := Excluded(); len(x) != 0 {
		t.Errorf("unexpected: %+v", x)
	}
	dir := filepath.Join(h, "claude")
	writeFile(t, filepath.Join(dir, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"secret"}}`)
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	x := Excluded()
	if len(x) != 1 || x[0].Agent != "claude" || x[0].Why == "" || strings.Contains(x[0].Why, "secret") ||
		!strings.Contains(x[0].Why, "Claude Code") || !strings.Contains(x[0].Why, "API key") {
		t.Errorf("excluded: %+v", x)
	}
	for _, p := range All() {
		if p.ID == "claude" || (p.Account != nil && p.Account.Agent == "claude") {
			t.Errorf("claude offered: %+v", p)
		}
	}
}
