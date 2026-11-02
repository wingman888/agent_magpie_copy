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

func env(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, "cache"))
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return h
}

func write(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func codexLogin(t *testing.T, h string, exp time.Time) string {
	path := filepath.Join(h, ".codex", "auth.json")
	write(t, path, `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"extra":{"a":1},"tokens":{"id_token":"`+
		jwt(map[string]any{"email": "me@x.com"})+`","access_token":"`+jwt(map[string]any{"exp": exp.Unix()})+
		`","refresh_token":"rt","account_id":"acc1","keep":"me"}}`)
	return path
}

func TestCodexDiscovery(t *testing.T) {
	h := env(t)
	if _, err := Find("codex"); err == nil {
		t.Fatal("codex should not exist when signed out")
	}
	codexLogin(t, h, time.Now().Add(time.Hour))
	p, err := Find("codex")
	if err != nil || p.Account == nil || p.Account.User != "me@x.com" || !p.Ready() {
		t.Fatalf("%+v %v", p, err)
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
	// api key mode is not an account
	write(t, filepath.Join(h, ".codex", "auth.json"), `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-x"}`)
	if _, err := Find("codex"); err == nil {
		t.Fatal("apikey mode must not be a provider")
	}
}

func TestSaveDeleteAccount(t *testing.T) {
	h := env(t)
	codexLogin(t, h, time.Now().Add(time.Hour))
	if err := Save(Provider{ID: "codex", Name: "X", Key: "secret", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), `"name"`) || strings.Contains(string(b), "key") {
		t.Fatalf("file has extra fields: %s", b)
	}
	p, _ := Find("codex")
	if len(p.Models) != 1 {
		t.Fatalf("%+v", p.Models)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("codex"); err != nil || len(p.Models) != 0 {
		t.Fatalf("%v %+v", err, p)
	}
	// leftover picks without login are ignored
	Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}})
	os.Remove(filepath.Join(h, ".codex", "auth.json"))
	for _, p := range All() {
		if p.ID == "codex" {
			t.Fatal("signed-out codex must vanish")
		}
	}
	if err := Save(Provider{ID: "claude", Name: "Claude", Chat: "http://x"}); err == nil {
		t.Fatal("claude must be refused")
	}
}

func TestCopilotAndClaude(t *testing.T) {
	h := env(t)
	write(t, filepath.Join(h, "cfg", "github-copilot", "apps.json"), `{"github.com:Iv1.x":{"user":"octo","oauth_token":"gho_1"}}`)
	p, err := Find("copilot")
	if err != nil || p.Account.User != "octo" {
		t.Fatalf("%+v %v", p, err)
	}
	ms := p.Exposed()
	if len(ms) == 0 {
		t.Fatal("no builtin copilot models")
	}
	for _, m := range ms {
		if m.ID == "auto" {
			t.Fatal("auto listed")
		}
	}
	if len(WithheldAccounts()) != 0 {
		t.Fatal("claude not signed in")
	}
	write(t, filepath.Join(h, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"SECRET"}}`)
	w := WithheldAccounts()
	if len(w) != 1 || w[0].Reason == "" || strings.Contains(w[0].Reason, "SECRET") {
		t.Fatalf("%+v", w)
	}
	for _, p := range All() {
		if p.ID == "claude" {
			t.Fatal("claude listed")
		}
	}
}

func TestCopilotModelFilter(t *testing.T) {
	f := false
	_ = f
	ms := parseCopilotModels([]byte(`{"data":[
	 {"id":"gpt-4.1","name":"GPT 4.1","model_picker_enabled":true,"capabilities":{"type":"chat"},"policy":{"state":"enabled"}},
	 {"id":"auto","model_picker_enabled":true,"capabilities":{"type":"chat"}},
	 {"id":"text-embedding-3-small","capabilities":{"type":"embeddings"}},
	 {"id":"internal-x","model_picker_enabled":false,"capabilities":{"type":"chat"}},
	 {"id":"off","model_picker_enabled":true,"capabilities":{"type":"chat"},"policy":{"state":"disabled"}}]}`))
	if len(ms) != 1 || ms[0].ID != "gpt-4.1" {
		t.Fatalf("%+v", ms)
	}
}

func TestCodexBody(t *testing.T) {
	out := CodexBody([]byte(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":5,"temperature":0.2,"top_p":1,"stream":false}`))
	var m map[string]any
	json.Unmarshal(out, &m)
	if m["model"] != "gpt-5.5" || m["stream"] != true || m["store"] != false {
		t.Fatalf("%s", out)
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := m[k]; ok {
			t.Fatalf("%s kept", k)
		}
	}
	in := m["input"].([]any)[0].(map[string]any)
	if in["role"] != "user" || in["content"].([]any)[0].(map[string]any)["text"] != "hi" {
		t.Fatalf("%s", out)
	}
}

func TestCodexRefresh(t *testing.T) {
	h := env(t)
	path := codexLogin(t, h, time.Now().Add(time.Minute))
	newAccess := jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]string{"access_token": newAccess, "refresh_token": "rt2"})
	}))
	defer srv.Close()
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	defer func() { CodexRefreshURL = old }()
	p, _ := Find("codex")
	_, hdr, _, err := p.Upstream(context.Background(), Responses, "/responses", []byte(`{}`))
	if err != nil || hdr["Authorization"] != "Bearer "+newAccess || hdr["chatgpt-account-id"] != "acc1" {
		t.Fatalf("%v %v", hdr, err)
	}
	if gotBody["refresh_token"] != "rt" || gotBody["grant_type"] != "refresh_token" {
		t.Fatalf("%v", gotBody)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if !strings.Contains(s, `"extra"`) || !strings.Contains(s, `"keep"`) || !strings.Contains(s, "rt2") || !strings.Contains(s, "account_id") {
		t.Fatalf("file lost fields: %s", s)
	}
	// failing refresh reports logged out
	codexLogin(t, h, time.Now().Add(time.Minute))
	CodexRefreshURL = "http://127.0.0.1:1"
	if _, _, _, err := p.Upstream(context.Background(), Responses, "/responses", []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "codex login") {
		t.Fatalf("%v", err)
	}
}

func TestCopilotUpstream(t *testing.T) {
	h := env(t)
	write(t, filepath.Join(h, "cfg", "github-copilot", "apps.json"), `{"github.com":{"user":"octo","oauth_token":"gho_1"}}`)
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_1" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(),
			"endpoints": map[string]string{"api": "https://api.example.test"}})
	}))
	defer srv.Close()
	old := GithubAPI
	GithubAPI = srv.URL
	defer func() { GithubAPI = old }()
	sess = copilotSession{}
	p, _ := Find("copilot")
	url, hdr, _, err := p.Upstream(context.Background(), Chat, "/chat/completions",
		[]byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`))
	if err != nil || url != "https://api.example.test/chat/completions" || hdr["Authorization"] != "Bearer sess" ||
		hdr["X-Initiator"] != "user" || hdr["Copilot-Vision-Request"] != "true" {
		t.Fatalf("%s %v %v", url, hdr, err)
	}
	_, hdr, _, _ = p.Upstream(context.Background(), Chat, "/chat/completions", []byte(`{"messages":[{"role":"tool","content":"x"}]}`))
	if hdr["X-Initiator"] != "agent" || hdr["Copilot-Vision-Request"] != "" {
		t.Fatalf("%v", hdr)
	}
	sess = copilotSession{}
	status = 401
	if _, _, _, err := p.Upstream(context.Background(), Chat, "/chat/completions", nil); err == nil || !strings.Contains(err.Error(), "重新登录 Copilot") {
		t.Fatalf("%v", err)
	}
}
