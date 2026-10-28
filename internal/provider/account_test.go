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
)

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

func idToken() string {
	return jwt(map[string]any{"email": "a@b.c", "https://api.openai.com/auth": map[string]any{
		"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-claim"}})
}

// home isolates HOME, config and cache dirs.
func home(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return h
}

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func codexAuth(t *testing.T, h, mode, access string, extra string) {
	t.Helper()
	writeFile(t, filepath.Join(h, ".codex", "auth.json"), `{"auth_mode":"`+mode+`","OPENAI_API_KEY":null,"keep":"me"`+extra+`,"tokens":{"id_token":"`+idToken()+`","access_token":"`+access+`","refresh_token":"r1","account_id":"acct-1"}}`)
}

func copilotAuth(t *testing.T, h string) {
	t.Helper()
	writeFile(t, filepath.Join(h, ".config", "github-copilot", "apps.json"), `{"github.com:Iv1.x":{"user":"octo","oauth_token":"gho_abc"}}`)
}

func find(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func TestAccountsDiscovery(t *testing.T) {
	h := home(t)
	codexAuth(t, h, "chatgpt", jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}), "")
	copilotAuth(t, h)
	all := All()
	c, g := find(all, "codex"), find(all, "copilot")
	if c == nil || g == nil {
		t.Fatalf("missing accounts: %+v", all)
	}
	if c.Account.User != "a@b.c" || c.Account.Plan != "plus" || !c.Account.Stream || g.Account.User != "octo" {
		t.Fatalf("accounts: %+v %+v", c.Account, g.Account)
	}
	if !c.Ready() || !g.Ready() || g.Chat == "" || c.Responses == "" {
		t.Fatal("not ready")
	}
	if a := Accounts(); len(a) != 2 || a[0].ID != "codex" || a[1].ID != "copilot" {
		t.Fatalf("order: %+v", a)
	}

	codexAuth(t, h, "apikey", "tok", "")
	if find(All(), "codex") != nil {
		t.Fatal("apikey mode listed")
	}
	codexAuth(t, h, "chatgpt", "", "")
	if find(All(), "codex") != nil {
		t.Fatal("empty token listed")
	}
	// leftover model pick of a signed-out account
	if err := Save(*g); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(h, ".config", "github-copilot", "apps.json"))
	if find(All(), "copilot") != nil || len(All()) != 0 {
		t.Fatalf("signed-out account listed: %+v", All())
	}
}

func TestAccountCatalogSaveDelete(t *testing.T) {
	h := home(t)
	codexAuth(t, h, "chatgpt", jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}), "")
	copilotAuth(t, h)
	ids := map[string]bool{}
	for _, e := range Catalog() {
		ids[e.ID] = true
	}
	if !ids["codex/gpt-5.5"] || !ids["copilot/claude-sonnet-4.5"] || ids["copilot/auto"] {
		t.Fatalf("catalog: %v", ids)
	}
	p, model, ok := Resolve("codex/gpt-5.5")
	if !ok || p.ID != "codex" || model != "gpt-5.5" || p.Account == nil {
		t.Fatalf("resolve: %+v %q %v", p, model, ok)
	}

	if err := Save(Provider{ID: "codex", Name: "Evil", Chat: "https://evil.example", Key: "sk-secret", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if !strings.Contains(string(b), `"gpt-5.5"`) || strings.Contains(string(b), "evil") || strings.Contains(string(b), "sk-secret") || strings.Contains(string(b), "Evil") {
		t.Fatalf("store: %s", b)
	}
	c := find(All(), "codex")
	if ex := c.Exposed(); len(ex) != 1 || ex[0].ID != "gpt-5.5" || c.Responses == "" {
		t.Fatalf("exposed: %+v", ex)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	c = find(All(), "codex")
	if c == nil || len(c.Models) != 0 {
		t.Fatalf("after delete: %+v", c)
	}
}

func TestPlainProviderRules(t *testing.T) {
	home(t)
	if err := Save(Provider{ID: "x", Name: "X", Key: "k"}); err == nil {
		t.Fatal("no url accepted")
	}
	if err := Save(Provider{ID: "x", Name: "X", Chat: "https://x.example/v1"}); err == nil {
		t.Fatal("no key accepted")
	}
	if err := Save(Provider{ID: "x", Name: "X", Chat: "https://x.example/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", "https://x.example/v1/chat/completions", nil)
	p, _ := Find("x")
	if err := p.Sign(context.Background(), req, Chat, nil); err != nil || req.Header.Get("Authorization") != "Bearer k" {
		t.Fatal("plain sign")
	}
	if b := p.Prepare([]byte(`{"a":1}`)); string(b) != `{"a":1}` {
		t.Fatal("plain prepare")
	}
}

func TestCodexSignAndPrepare(t *testing.T) {
	h := home(t)
	codexAuth(t, h, "chatgpt", jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}), "")
	p := find(Accounts(), "codex")
	req, _ := http.NewRequest("POST", "https://x/responses", nil)
	if err := p.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") || req.Header.Get("chatgpt-account-id") != "acct-1" || req.Header.Get("originator") != "dial" {
		t.Fatalf("headers: %v", req.Header)
	}
	out := p.Prepare([]byte(`{"model":"gpt-5.5","input":"hi","max_output_tokens":5,"temperature":0.1,"stream":false,"store":true,"reasoning":{"effort":"low"}}`))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	in, _ := m["input"].([]any)
	if _, ok := m["max_output_tokens"]; ok || m["temperature"] != nil || m["stream"] != true || m["store"] != false || m["reasoning"] == nil || len(in) != 1 {
		t.Fatalf("prepare: %s", out)
	}
	msg := in[0].(map[string]any)
	txt := msg["content"].([]any)[0].(map[string]any)
	if msg["role"] != "user" || txt["text"] != "hi" || txt["type"] != "input_text" {
		t.Fatalf("input: %s", out)
	}
	if string(p.Prepare([]byte("nope"))) != "nope" {
		t.Fatal("unparsable body changed")
	}
	// signed out
	os.Remove(filepath.Join(h, ".codex", "auth.json"))
	if err := p.Sign(context.Background(), req, Responses, nil); err == nil || !strings.Contains(err.Error(), "Codex is signed out; run codex login") {
		t.Fatalf("err: %v", err)
	}
}

func TestCodexRefresh(t *testing.T) {
	h := home(t)
	codexAuth(t, h, "chatgpt", jwt(map[string]any{"exp": time.Now().Add(time.Minute).Unix()}), "")
	fresh := jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	var calls atomic.Int32
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]string{"access_token": fresh, "refresh_token": "r2"})
	}))
	defer srv.Close()
	old := CodexTokenURL
	CodexTokenURL = srv.URL
	defer func() { CodexTokenURL = old }()

	p := find(Accounts(), "codex")
	req, _ := http.NewRequest("POST", "https://x/responses", nil)
	if err := p.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer "+fresh || got["grant_type"] != "refresh_token" || got["refresh_token"] != "r1" || got["client_id"] == "" {
		t.Fatalf("refresh: %v %v", req.Header, got)
	}
	path := filepath.Join(h, ".codex", "auth.json")
	b, _ := os.ReadFile(path)
	var f map[string]any
	json.Unmarshal(b, &f)
	tok := f["tokens"].(map[string]any)
	if f["keep"] != "me" || tok["access_token"] != fresh || tok["refresh_token"] != "r2" || tok["account_id"] != "acct-1" {
		t.Fatalf("file: %s", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	// a second call finds a fresh token and does not refresh again
	if err := p.Sign(context.Background(), req, Responses, nil); err != nil || calls.Load() != 1 {
		t.Fatalf("calls %d err %v", calls.Load(), err)
	}

	// failing refresh
	codexAuth(t, h, "chatgpt", jwt(map[string]any{"exp": time.Now().Add(time.Minute).Unix()}), "")
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer bad.Close()
	CodexTokenURL = bad.URL
	if err := p.Sign(context.Background(), req, Responses, nil); err == nil || !strings.Contains(err.Error(), "Codex is signed out (token refresh failed); run codex login") {
		t.Fatalf("err: %v", err)
	}
}

// copilotServer fakes the token endpoint and the API it points at.
func copilotServer(t *testing.T, tokens *atomic.Int32) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokens.Add(1)
			if r.Header.Get("Authorization") != "token gho_abc" {
				w.WriteHeader(401)
				w.Write([]byte(`{"message":"Bad credentials"}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(),
				"endpoints": map[string]string{"api": srv.URL + "/api"}})
		case "/api/models":
			if r.Header.Get("Authorization") != "Bearer sess" {
				w.WriteHeader(401)
				return
			}
			io := `{"data":[
{"id":"gpt-x","name":"GPT X","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}}},
{"id":"emb","model_picker_enabled":true,"capabilities":{"type":"embeddings"}},
{"id":"exec-agent-1","model_picker_enabled":true,"capabilities":{"type":"chat"}},
{"id":"off","model_picker_enabled":false,"policy":{"state":"disabled"},"capabilities":{"type":"chat"}},
{"id":"pol","name":"Pol","model_picker_enabled":false,"policy":{"state":"enabled"},"capabilities":{"type":"chat"}},
{"id":"m-secondary","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`
			w.Write([]byte(io))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCopilotFetchAndSign(t *testing.T) {
	h := home(t)
	copilotAuth(t, h)
	var tokens atomic.Int32
	srv := copilotServer(t, &tokens)
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	defer func() { CopilotTokenURL = old }()

	p := find(Accounts(), "copilot")
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].ID != "gpt-x" || ms[1].ID != "pol" || len(ms[0].Efforts) != 2 {
		t.Fatalf("models: %+v", ms)
	}
	av := p.Available()
	if len(av) != 2 || av[0].ID != "gpt-x" || av[1].ID != "pol" {
		t.Fatalf("available: %+v", av)
	}

	for _, c := range []struct{ body, init string }{
		{`{"messages":[{"role":"user","content":"x"},{"role":"tool","content":"y"}]}`, "agent"},
		{`{"messages":[{"role":"user","content":"x"}]}`, "user"},
	} {
		req, _ := http.NewRequest("POST", p.Chat+"/chat/completions", nil)
		if err := p.Sign(context.Background(), req, Chat, []byte(c.body)); err != nil {
			t.Fatal(err)
		}
		if req.URL.Host != strings.TrimPrefix(srv.URL, "http://") || req.URL.Path != "/chat/completions" ||
			req.Header.Get("Authorization") != "Bearer sess" || req.Header.Get("Copilot-Integration-Id") == "" ||
			req.Header.Get("X-Initiator") != c.init || req.Header.Get("Copilot-Vision-Request") != "" {
			t.Fatalf("sign: %v %v", req.URL, req.Header)
		}
	}
	req, _ := http.NewRequest("POST", p.Chat+"/chat/completions", nil)
	p.Sign(context.Background(), req, Chat, []byte(`{"messages":[{"role":"user","content":[{"type":"image_url"}]}]}`))
	if req.Header.Get("Copilot-Vision-Request") != "true" {
		t.Fatal("vision header")
	}
	if tokens.Load() != 1 {
		t.Fatalf("session token fetched %d times, want cached", tokens.Load())
	}

	// bad credentials
	writeFile(t, filepath.Join(h, ".config", "github-copilot", "apps.json"), `{"github.com:x":{"user":"octo","oauth_token":"bad"}}`)
	if err := p.Sign(context.Background(), req, Chat, nil); err == nil || !strings.Contains(err.Error(), "Copilot is signed out") {
		t.Fatalf("err: %v", err)
	}
}

func TestExcluded(t *testing.T) {
	h := home(t)
	if len(Excluded()) != 0 {
		t.Fatal("excluded without login")
	}
	writeFile(t, filepath.Join(h, ".claude", ".credentials.json"), `{}`)
	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "claude" || !strings.Contains(ex[0].Why, "Anthropic") || !strings.Contains(ex[0].Why, "API key") {
		t.Fatalf("excluded: %+v", ex)
	}
	if find(All(), "claude") != nil {
		t.Fatal("claude offered")
	}
}
