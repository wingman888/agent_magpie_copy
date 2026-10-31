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

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "x." + base64.RawURLEncoding.EncodeToString(b) + ".y"
}

func envSetup(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	resetExcluded()
	return home
}

func writeCodex(t *testing.T, home string, exp time.Time, extra map[string]any) {
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	m := map[string]any{"auth_mode": "chatgpt", "keep": "me", "tokens": map[string]any{
		"access_token": jwt(map[string]any{"exp": exp.Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acc1"}}),
		"id_token":     jwt(map[string]any{"email": "a@b.com"}), "refresh_token": "r1", "account_id": "acc1"}}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600)
}

func writeCopilot(t *testing.T, home string) {
	d := filepath.Join(home, ".config", "github-copilot")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "apps.json"), []byte(`{"github.com:Iv1.x":{"oauth_token":"gho_x","user":"octo"}}`), 0o600)
}

func TestAccountsAndStore(t *testing.T) {
	home := envSetup(t)
	writeCodex(t, home, time.Now().Add(time.Hour), nil)
	writeCopilot(t, home)
	all := All()
	if len(all) != 2 || all[0].ID != "codex" || all[0].Account.User != "a@b.com" || all[0].Account.Plan != "pro" || all[1].Account.User != "octo" {
		t.Fatalf("%+v", all)
	}
	if !all[0].Ready() || !all[1].Ready() {
		t.Fatal("not ready")
	}
	if p, m, ok := Resolve("codex/gpt-5.5"); !ok || p.ID != "codex" || m != "gpt-5.5" {
		t.Fatal("resolve")
	}
	ids := strings.Join(IDs(), " ")
	if !strings.Contains(ids, "copilot/claude-sonnet-4.5") || strings.Contains(ids, "copilot/auto") {
		t.Fatal(ids)
	}
	p := all[0]
	p.Models, p.Chat, p.Key = []string{"gpt-5.5"}, "http://x", "sekrit"
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "sekrit") || strings.Contains(string(b), "http://x") || strings.Contains(string(b), "key") {
		t.Fatal(string(b))
	}
	q, _ := Find("codex")
	if len(q.Exposed()) != 1 {
		t.Fatal("exposed")
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	q, err := Find("codex")
	if err != nil || len(q.Models) != 0 {
		t.Fatal("delete", err)
	}
	// logout with leftover record
	Save(*q)
	q.Models = []string{"gpt-5.5"}
	Save(*q)
	os.Remove(filepath.Join(home, ".codex", "auth.json"))
	if _, err := Find("codex"); err == nil {
		t.Fatal("still there")
	}
	// api key mode
	writeCodex(t, home, time.Now().Add(time.Hour), map[string]any{"auth_mode": "apikey"})
	if _, err := Find("codex"); err == nil {
		t.Fatal("apikey mode counted")
	}
}

func TestPrepare(t *testing.T) {
	envSetup(t)
	p := Provider{Account: &Account{Agent: "codex"}}
	out := string(p.Prepare([]byte(`{"model":"m","input":"hi","max_output_tokens":5,"temperature":1,"reasoning":{"effort":"low"}}`)))
	var m map[string]any
	json.Unmarshal([]byte(out), &m)
	if m["stream"] != true || m["store"] != false || m["max_output_tokens"] != nil || m["temperature"] != nil || m["reasoning"] == nil {
		t.Fatal(out)
	}
	if _, ok := m["input"].([]any); !ok {
		t.Fatal(out)
	}
	if string((Provider{}).Prepare([]byte("abc"))) != "abc" {
		t.Fatal()
	}
}

func TestCodexRefresh(t *testing.T) {
	home := envSetup(t)
	writeCodex(t, home, time.Now().Add(time.Minute), nil)
	newTok := jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"access_token": newTok, "refresh_token": "r2"})
	}))
	defer srv.Close()
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	defer func() { CodexRefreshURL = old }()
	req, _ := http.NewRequest("POST", "http://x", nil)
	p := Accounts()[0]
	if err := p.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer "+newTok || req.Header.Get("chatgpt-account-id") != "acc1" {
		t.Fatal(req.Header)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["keep"] != "me" || m["last_refresh"] == nil || m["tokens"].(map[string]any)["refresh_token"] != "r2" {
		t.Fatal(string(b))
	}
	if st, _ := os.Stat(filepath.Join(home, ".codex", "auth.json")); st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
	// failure
	writeCodex(t, home, time.Now().Add(time.Minute), nil)
	CodexRefreshURL = "http://127.0.0.1:1"
	err := p.Sign(context.Background(), req, Responses, nil)
	if _, ok := err.(*SignedOutError); !ok || !strings.Contains(err.Error(), "codex login") {
		t.Fatal(err)
	}
}

func TestCopilot(t *testing.T) {
	home := envSetup(t)
	writeCopilot(t, home)
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			if r.Header.Get("Authorization") != "Bearer sess" {
				w.WriteHeader(401)
			}
			w.Write([]byte(`{"data":[{"id":"a","name":"A","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}},"policy":{"state":"enabled"}},
			{"id":"auto","capabilities":{"type":"chat"}},{"id":"emb","capabilities":{"type":"embeddings"}},
			{"id":"hid","model_picker_enabled":false,"capabilities":{"type":"chat"}},{"id":"dis","capabilities":{"type":"chat"},"policy":{"state":"disabled"}}]}`))
		}
	}))
	defer api.Close()
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_x" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	defer tok.Close()
	old := CopilotTokenURL
	CopilotTokenURL = tok.URL
	defer func() { CopilotTokenURL = old }()
	p, _ := Find("copilot")
	ms, err := p.Fetch(context.Background())
	if err != nil || len(ms) != 1 || ms[0].ID != "a" || len(ms[0].Efforts) != 2 {
		t.Fatal(ms, err)
	}
	if av := p.Available(); len(av) != 1 {
		t.Fatal(av)
	}
	req, _ := http.NewRequest("POST", "https://api.githubcopilot.com/chat/completions", nil)
	body := `{"messages":[{"role":"user","content":"x"},{"role":"tool","content":"y"}]}`
	if err := p.Sign(context.Background(), req, Chat, []byte(body)); err != nil {
		t.Fatal(err)
	}
	if req.URL.Host != strings.TrimPrefix(api.URL, "http://") || req.Header.Get("X-Initiator") != "agent" || req.Header.Get("Editor-Version") == "" {
		t.Fatal(req.URL, req.Header)
	}
}

func TestExcluded(t *testing.T) {
	home := envSetup(t)
	if len(Excluded()) != 0 {
		t.Fatal()
	}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o600)
	resetExcluded()
	ex := Excluded()
	if len(ex) != 1 || ex[0].Why == "" {
		t.Fatal(ex)
	}
}
