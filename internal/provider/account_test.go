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
	"testing"
	"time"
)

// setHome points HOME and the XDG dirs at a fresh temp tree.
func setHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeJWT builds an unsigned token whose payload is the given JSON.
func fakeJWT(payload string) string {
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
}

const codexLoginJSON = `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"%s","access_token":"ACCESS-SECRET","refresh_token":"R","account_id":"acct-1"}}`

func writeCodexLogin(t *testing.T, home string) {
	t.Helper()
	id := fakeJWT(`{"email":"dev@example.com","https://api.openai.com/auth":{"chatgpt_plan_type":"pro","chatgpt_account_id":"acct-1"}}`)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), strings.Replace(codexLoginJSON, "%s", id, 1))
}

func findProv(t *testing.T, ps []Provider, id string) (Provider, bool) {
	t.Helper()
	for _, p := range ps {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

func TestCodexAccountDiscovery(t *testing.T) {
	home := setHome(t)
	writeCodexLogin(t, home)
	acc := Accounts()
	p, ok := findProv(t, acc, "codex")
	if !ok {
		t.Fatalf("codex account missing: %+v", acc)
	}
	if p.Account == nil || p.Account.Agent != "codex" || p.Account.User != "dev@example.com" || p.Account.Plan != "pro" {
		t.Fatalf("bad account info: %+v", p.Account)
	}
	if !p.Ready() {
		t.Fatal("account provider should be ready without a key")
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "ACCESS-SECRET") {
		t.Fatal("token leaked into provider JSON")
	}
	if _, ok := findProv(t, All(), "codex"); !ok {
		t.Fatal("codex missing from All()")
	}
	if _, m, ok := Resolve("codex/gpt-5.5"); !ok || m != "gpt-5.5" {
		t.Fatalf("Resolve(codex/gpt-5.5) = %q, %v", m, ok)
	}
}

func TestCodexNoPlanOrEmail(t *testing.T) {
	home := setHome(t)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"),
		`{"tokens":{"id_token":"not-a-jwt","access_token":"A","account_id":"x"}}`)
	p, ok := findProv(t, Accounts(), "codex")
	if !ok || p.Account.User != "ChatGPT" || p.Account.Plan != "" {
		t.Fatalf("fallback identity wrong: %+v %v", p.Account, ok)
	}
}

func TestCodexAPIKeyModeIsNotAccount(t *testing.T) {
	home := setHome(t)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-x","tokens":{"access_token":"A"}}`)
	if _, ok := findProv(t, Accounts(), "codex"); ok {
		t.Fatal("API-key mode must not be an account")
	}
}

func TestCopilotDiscoveryAndLogout(t *testing.T) {
	home := setHome(t)
	writeFile(t, filepath.Join(home, "config", "github-copilot", "hosts.json"),
		`{"github.com":{"user":"octocat","oauth_token":"gho_x"}}`)
	p, ok := findProv(t, Accounts(), "copilot")
	if !ok || p.Account.User != "octocat" {
		t.Fatalf("copilot discovery: %+v %v", p.Account, ok)
	}
	writeFile(t, filepath.Join(home, "config", "github-copilot", "apps.json"),
		`{"github.com:Iv1.x":{"oauth_token":"gho_y"}}`)
	writeFile(t, filepath.Join(home, "config", "github-copilot", "hosts.json"), `{}`)
	p, ok = findProv(t, Accounts(), "copilot")
	if !ok || p.Account.User != "GitHub" {
		t.Fatalf("copilot fallback name: %+v %v", p.Account, ok)
	}
	if err := os.Remove(filepath.Join(home, "config", "github-copilot", "apps.json")); err != nil {
		t.Fatal(err)
	}
	if _, ok := findProv(t, All(), "copilot"); ok {
		t.Fatal("copilot must vanish after sign-out")
	}
}

func TestSelectionStoredWithoutSecrets(t *testing.T) {
	home := setHome(t)
	writeCodexLogin(t, home)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}, Chat: "http://evil.example/v1", Key: "sk-SECRET"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(Path())
	s := string(raw)
	if strings.Contains(s, "evil.example") || strings.Contains(s, "sk-SECRET") || strings.Contains(s, "ACCESS-SECRET") {
		t.Fatalf("config holds address/key/token:\n%s", s)
	}
	p, ok := findProv(t, All(), "codex")
	if !ok || len(p.Models) != 1 || p.Models[0] != "gpt-5.5" || len(p.Exposed()) != 1 {
		t.Fatalf("selection not applied: %+v", p.Models)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p, ok = findProv(t, All(), "codex")
	if !ok || len(p.Models) != 0 {
		t.Fatalf("delete should clear selection and keep provider: %+v %v", p.Models, ok)
	}
	// sign-out: the leftover selection must not produce a provider
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(home, ".codex"))
	if _, ok := findProv(t, All(), "codex"); ok {
		t.Fatal("codex must vanish after logout")
	}
	if err := Save(Provider{ID: "codex", Models: []string{"x"}}); err == nil {
		t.Fatal("saving a selection for a signed-out account should fail")
	}
}

func TestUserProviderShadowsAccount(t *testing.T) {
	home := setHome(t)
	// the user's own provider exists before the sign-in
	if err := Save(Provider{ID: "codex", Name: "codex", Chat: "http://localhost:9999/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	writeCodexLogin(t, home)
	if len(Accounts()) != 0 {
		t.Fatal("user-built codex must shadow the account")
	}
	ps := All()
	n := 0
	for _, p := range ps {
		if p.ID == "codex" {
			n++
			if p.Account != nil {
				t.Fatal("shadowed id must be the user's provider")
			}
		}
	}
	if n != 1 {
		t.Fatalf("expected one codex provider, got %d", n)
	}
}

func TestClaudeExcludedNotAccount(t *testing.T) {
	home := setHome(t)
	if len(Excluded()) != 0 {
		t.Fatal("no claude login, no exclusion")
	}
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"C"}}`)
	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "claude" || ex[0].Why == "" {
		t.Fatalf("exclusion wrong: %+v", ex)
	}
	if _, ok := findProv(t, All(), "claude"); ok {
		t.Fatal("claude must never be a provider")
	}
}

func TestCodexPrepareAndSign(t *testing.T) {
	home := setHome(t)
	writeCodexLogin(t, home)
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r }))
	defer srv.Close()
	old := CodexBase
	CodexBase = srv.URL + "/codex"
	defer func() { CodexBase = old }()

	p, _ := findProv(t, Accounts(), "codex")
	body := p.Prepare([]byte(`{"model":"gpt-5.5","input":"hi","max_output_tokens":16,"temperature":0.2,"reasoning":{"effort":"low"}}`))
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["max_output_tokens"]; ok {
		t.Fatal("max_output_tokens kept")
	}
	if _, ok := m["temperature"]; ok {
		t.Fatal("temperature kept")
	}
	if m["stream"] != true || m["store"] != false {
		t.Fatalf("stream/store wrong: %v", m)
	}
	if _, ok := m["reasoning"]; !ok {
		t.Fatal("reasoning must be kept")
	}
	if _, ok := m["input"].([]any); !ok {
		t.Fatalf("input not a list: %v", m["input"])
	}

	req, _ := http.NewRequest(http.MethodPost, "http://dial.invalid/v1/responses", nil)
	if err := p.Sign(context.Background(), req, Responses, body); err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != srv.URL+"/codex/responses" {
		t.Fatalf("url = %s", req.URL)
	}
	if req.Header.Get("Authorization") != "Bearer ACCESS-SECRET" || req.Header.Get("chatgpt-account-id") != "acct-1" {
		t.Fatalf("headers: %v", req.Header)
	}
	_ = got
}

func TestCopilotRefreshSignAndFilter(t *testing.T) {
	home := setHome(t)
	writeFile(t, filepath.Join(home, "config", "github-copilot", "hosts.json"),
		`{"github.com":{"user":"octocat","oauth_token":"gho_x"}}`)

	var srv *httptest.Server
	var chatHeaders http.Header
	var chatBody string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.Header.Get("Authorization") != "token gho_x" {
				http.Error(w, "bad auth", 401)
				return
			}
			w.Write([]byte(`{"token":"sess","expires_at":` + itoa(time.Now().Add(time.Hour).Unix()) +
				`,"endpoints":{"api":"` + srv.URL + `/api"}}`))
		case "/api/models":
			if r.Header.Get("Authorization") != "Bearer sess" || r.Header.Get("Copilot-Integration-Id") == "" {
				http.Error(w, "bad", 401)
				return
			}
			w.Write([]byte(`{"data":[
 {"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}},"policy":{"state":"enabled"}},
 {"id":"copilot-search-x","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"gpt-5-secondary","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"off-model","model_picker_enabled":false,"capabilities":{"type":"chat"},"policy":{"state":"disabled"}},
 {"id":"embed","model_picker_enabled":true,"capabilities":{"type":"embeddings"}}
]}`))
		case "/api/chat/completions":
			chatHeaders = r.Header.Clone()
			b := make([]byte, 4096)
			n, _ := r.Body.Read(b)
			chatBody = string(b[:n])
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldURL := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	defer func() { CopilotTokenURL = oldURL }()

	p, ok := findProv(t, Accounts(), "copilot")
	if !ok {
		t.Fatal("copilot missing")
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != "claude-sonnet-4.5" || len(ms[0].Efforts) != 2 {
		t.Fatalf("filtered models wrong: %+v", ms)
	}
	av := p.Available()
	if len(av) != 1 || av[0].ID != "claude-sonnet-4.5" {
		t.Fatalf("available after refresh: %+v", av)
	}

	// builtin list before refresh: no auto
	// Only ever remove inside this test's own cache dir: an empty
	// XDG_CACHE_HOME would make the path relative to the package directory.
	if cache := os.Getenv("XDG_CACHE_HOME"); cache != "" {
		os.RemoveAll(filepath.Join(cache, "dial", "models"))
	}
	for _, m := range p.Available() {
		if m.ID == "auto" {
			t.Fatal("builtin copilot list must not include auto")
		}
	}

	req, _ := http.NewRequest(http.MethodPost, "http://dial.invalid/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`))
	body := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`)
	if err := p.Sign(context.Background(), req, Chat, body); err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != srv.URL+"/api/chat/completions" {
		t.Fatalf("url = %s", req.URL)
	}
	if req.Header.Get("X-Initiator") != "agent" || req.Header.Get("Authorization") != "Bearer sess" {
		t.Fatalf("headers: %v", req.Header)
	}
	_ = chatHeaders
	_ = chatBody
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
