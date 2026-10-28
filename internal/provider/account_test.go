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

// fakeJWT is a token whose payload is the given claims; nobody checks the
// signature, so "sig" will do.
func fakeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func writeFile(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// signIn writes a Codex and a Copilot login into a temp home.
func signIn(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	exp := float64(time.Now().Add(time.Hour).Unix())
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-1"}}),
			"access_token":  fakeJWT(map[string]any{"exp": exp}),
			"refresh_token": "r", "account_id": "acct-1",
		},
	})
	writeFile(t, filepath.Join(home, ".config", "github-copilot", "apps.json"), map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "octocat", "oauth_token": "gho_x"},
	})
	return home
}

func TestAccountsAreProviders(t *testing.T) {
	signIn(t)
	all := All()
	codex, ok := find(all, "codex")
	if !ok || codex.Account == nil || codex.Account.User != "me@example.com" || codex.Account.Plan != "pro" || !codex.Ready() {
		t.Fatalf("codex: %+v", codex)
	}
	copilot, ok := find(all, "copilot")
	if !ok || copilot.Account == nil || copilot.Account.User != "octocat" || copilot.Chat == "" {
		t.Fatalf("copilot: %+v", copilot)
	}
	var ids []string
	for _, e := range Catalog() {
		ids = append(ids, e.ID)
	}
	if !contains(ids, "codex/gpt-5.5") || !contains(ids, "copilot/claude-sonnet-4.5") || contains(ids, "copilot/auto") {
		t.Fatalf("catalog: %v", ids)
	}
	p, model, ok := Resolve("codex/gpt-5.5")
	if !ok || p.ID != "codex" || model != "gpt-5.5" || p.Account == nil {
		t.Fatalf("resolve: %+v %q %v", p, model, ok)
	}

	// picks are the only thing saved; the file never holds the login
	if err := Save(Provider{ID: "codex", Name: "x", Chat: "https://ignored", Key: "ignored", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "ignored") || !strings.Contains(string(b), `"gpt-5.5"`) {
		t.Fatalf("stored: %s", b)
	}
	codex, _ = find(All(), "codex")
	if len(codex.Exposed()) != 1 || codex.Exposed()[0].ID != "gpt-5.5" || codex.Responses == "" {
		t.Fatalf("picks: %+v", codex.Exposed())
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if codex, _ = find(All(), "codex"); len(codex.Models) != 0 || codex.Account == nil {
		t.Fatalf("after delete: %+v", codex)
	}

	// signed out: gone, and a stale picks entry is not a provider
	Save(Provider{ID: "copilot", Models: []string{"gpt-5.5"}})
	os.Remove(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json"))
	if _, ok := find(All(), "copilot"); ok {
		t.Fatal("copilot still listed after sign-out")
	}
}

func TestCodexSignAndBody(t *testing.T) {
	signIn(t)
	p, _ := find(All(), "codex")
	req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
	if err := p.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer h.") || req.Header.Get("chatgpt-account-id") != "acct-1" || req.Header.Get("originator") != "dial" {
		t.Fatalf("headers: %v", req.Header)
	}
	out := p.Prepare([]byte(`{"model":"gpt-5.5","input":"hi","max_output_tokens":5,"temperature":0.1,"stream":false,"store":true,"reasoning":{"effort":"low"}}`))
	var m map[string]any
	json.Unmarshal(out, &m)
	if _, ok := m["max_output_tokens"]; ok || m["temperature"] != nil || m["stream"] != true || m["store"] != false || m["reasoning"] == nil {
		t.Fatalf("body: %s", out)
	}
	if in := m["input"].([]any)[0].(map[string]any); in["role"] != "user" || in["content"].([]any)[0].(map[string]any)["text"] != "hi" {
		t.Fatalf("input: %s", out)
	}
}

func TestCopilotSignAndModels(t *testing.T) {
	signIn(t)
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess" || r.Header.Get("Copilot-Integration-Id") == "" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/models" {
			io := `{"data":[
			  {"id":"gpt-5.5","name":"GPT-5.5","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}}},
			  {"id":"claude-sonnet-5","name":"Claude Sonnet 5","policy":{"state":"disabled"},"capabilities":{"type":"chat"}},
			  {"id":"gpt-4.1","name":"GPT-4.1","policy":{"state":"enabled"},"capabilities":{"type":"chat"}},
			  {"id":"exec-agent-a","name":"Exec","model_picker_enabled":true,"capabilities":{"type":"chat"}},
			  {"id":"text-embedding-3-small","name":"Emb","model_picker_enabled":true,"capabilities":{"type":"embeddings"}}]}`
			w.Write([]byte(io))
			return
		}
		w.Write([]byte(`{"path":"` + r.URL.Path + `","initiator":"` + r.Header.Get("X-Initiator") + `"}`))
	}))
	defer api.Close()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_x" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	defer tokens.Close()
	old := CopilotTokenURL
	CopilotTokenURL = tokens.URL
	defer func() { CopilotTokenURL = old }()
	copilotSessions = map[string]copilotSession{}

	p, _ := find(All(), "copilot")
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
	p, _ = find(All(), "copilot")
	if got := p.Available(); len(got) != 2 || got[0].ID != "gpt-5.5" {
		t.Fatalf("available after fetch: %+v", got)
	}

	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`)
	req, _ := http.NewRequest("POST", p.Chat+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, body); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(req.URL.String(), api.URL) || req.URL.Path != "/chat/completions" || req.Header.Get("X-Initiator") != "agent" {
		t.Fatalf("signed: %s %v", req.URL, req.Header)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	json.NewDecoder(res.Body).Decode(&got)
	if got["path"] != "/chat/completions" || got["initiator"] != "agent" {
		t.Fatalf("api saw %v", got)
	}
}
