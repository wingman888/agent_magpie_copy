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
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

func envHome(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

func writeCodex(t *testing.T, home string, v map[string]any) string {
	dir := filepath.Join(home, ".codex")
	os.MkdirAll(dir, 0o755)
	b, _ := json.Marshal(v)
	p := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func codexAuth(access string) map[string]any {
	return map[string]any{
		"auth_mode": "chatgpt", "extra": "keep",
		"tokens": map[string]any{
			"access_token": access, "refresh_token": "r1", "account_id": "acc-1",
			"id_token": jwt(map[string]any{"email": "a@x.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}}),
		},
	}
}

func TestCodexAccount(t *testing.T) {
	home := envHome(t)
	writeCodex(t, home, codexAuth(jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})))
	all := All()
	if len(all) != 1 || all[0].ID != "codex" || all[0].Account.User != "a@x.com" || all[0].Account.Plan != "pro" || !all[0].Ready() {
		t.Fatalf("%+v", all)
	}
	var found bool
	for _, e := range Catalog() {
		if e.ID == "codex/gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Fatal("no codex/gpt-5.5")
	}
	if p, m, ok := Resolve("codex/gpt-5.5"); !ok || p.ID != "codex" || m != "gpt-5.5" {
		t.Fatal("resolve")
	}
	// selection
	if err := Save(Provider{ID: "codex", Chat: "http://evil", Key: "sk-secret", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "evil") || strings.Contains(string(b), "secret") {
		t.Fatal(string(b))
	}
	if p, _ := Find("codex"); len(p.Exposed()) != 1 {
		t.Fatal("exposed")
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("codex"); err != nil || len(p.Models) != 0 {
		t.Fatal("delete")
	}
	// leftover selection, then logout
	Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}})
	os.Remove(filepath.Join(home, ".codex", "auth.json"))
	if len(All()) != 0 {
		t.Fatal("still there after logout")
	}
	// api key mode
	writeCodex(t, home, map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "sk-x"})
	if len(All()) != 0 {
		t.Fatal("apikey counted")
	}
}

func TestPrepareCodex(t *testing.T) {
	p := Provider{Account: &Account{Agent: "codex"}}
	out := p.Prepare([]byte(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":5,"temperature":1,"reasoning":{"effort":"low"}}`))
	var m map[string]any
	json.Unmarshal(out, &m)
	if m["model"] != "gpt-5.5" || m["stream"] != true || m["store"] != false || m["temperature"] != nil || m["max_output_tokens"] != nil || m["reasoning"] == nil {
		t.Fatal(string(out))
	}
	if in, ok := m["input"].([]any); !ok || len(in) != 1 {
		t.Fatal(string(out))
	}
}

func TestCodexRefresh(t *testing.T) {
	home := envHome(t)
	path := writeCodex(t, home, codexAuth(jwt(map[string]any{"exp": time.Now().Add(time.Minute).Unix()})))
	newTok := jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"access_token": newTok, "refresh_token": "r2"})
	}))
	defer srv.Close()
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	defer func() { CodexRefreshURL = old }()
	p, _ := Find("codex")
	req, _ := http.NewRequest("POST", "http://x/y", nil)
	if err := p.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer "+newTok || req.Header.Get("chatgpt-account-id") != "acc-1" {
		t.Fatal(req.Header)
	}
	b, _ := os.ReadFile(path)
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["extra"] != "keep" || m["tokens"].(map[string]any)["refresh_token"] != "r2" || m["tokens"].(map[string]any)["account_id"] != "acc-1" {
		t.Fatal(string(b))
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
	// failure
	writeCodex(t, home, codexAuth(jwt(map[string]any{"exp": time.Now().Add(time.Minute).Unix()})))
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv2.Close()
	CodexRefreshURL = srv2.URL
	err := p.Sign(context.Background(), req, Responses, nil)
	if err == nil || !strings.Contains(err.Error(), "signed out") {
		t.Fatal(err)
	}
}

func TestCopilot(t *testing.T) {
	home := envHome(t)
	dir := filepath.Join(home, ".config", "github-copilot")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "apps.json"), []byte(`{"github.com:Iv1.x":{"user":"alice","oauth_token":"gho_abc"}}`), 0o600)
	var api string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.Header.Get("Authorization") != "token gho_abc" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api}})
		case "/models":
			if r.Header.Get("Authorization") != "Bearer sess" || r.Header.Get("Copilot-Integration-Id") == "" {
				w.WriteHeader(401)
				return
			}
			io := `{"data":[
			{"id":"gpt-5","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}}},
			{"id":"emb","model_picker_enabled":true,"capabilities":{"type":"embeddings"}},
			{"id":"x-secondary","model_picker_enabled":true,"capabilities":{"type":"chat"}},
			{"id":"off","model_picker_enabled":false,"policy":{"state":"disabled"},"capabilities":{"type":"chat"}},
			{"id":"pol","policy":{"state":"enabled"},"capabilities":{"type":"chat"}}]}`
			w.Write([]byte(io))
		}
	}))
	defer srv.Close()
	api = srv.URL
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	defer func() { CopilotTokenURL = old }()
	p, err := Find("copilot")
	if err != nil || p.Account.User != "alice" {
		t.Fatal(err)
	}
	for _, m := range p.Available() {
		if m.ID == "auto" {
			t.Fatal("auto")
		}
	}
	ms, err := p.Fetch(context.Background())
	if err != nil || len(ms) != 2 || ms[0].ID != "gpt-5" || len(ms[0].Efforts) != 2 || ms[1].ID != "pol" {
		t.Fatal(ms, err)
	}
	if got := p.Available(); len(got) != 2 {
		t.Fatal(got)
	}
	req, _ := http.NewRequest("POST", "http://placeholder/x", nil)
	body := `{"messages":[{"role":"user","content":"a"},{"role":"tool","content":"b"}]}`
	if err := p.Sign(context.Background(), req, Chat, []byte(body)); err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != api+"/chat/completions" || req.Header.Get("X-Initiator") != "agent" || req.Header.Get("Authorization") != "Bearer sess" {
		t.Fatal(req.URL, req.Header)
	}
}

func TestExcluded(t *testing.T) {
	home := envHome(t)
	if len(Excluded()) != 0 {
		t.Fatal("unexpected")
	}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"x":1}`), 0o600)
	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "claude" || ex[0].Why == "" {
		t.Fatal(ex)
	}
}
