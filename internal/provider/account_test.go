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
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return h
}

func writeFile(t *testing.T, path, s string) {
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func codexLogin(t *testing.T, h string, exp time.Time) {
	a := jwt(map[string]any{"exp": exp.Unix()})
	id := jwt(map[string]any{"email": "me@x.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
	b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "extra": "keep",
		"tokens": map[string]any{"id_token": id, "access_token": a, "refresh_token": "r1", "account_id": "acc"}})
	writeFile(t, filepath.Join(h, ".codex", "auth.json"), string(b))
}

func TestCodexAccount(t *testing.T) {
	h := env(t)
	if _, err := Find("codex"); err == nil {
		t.Fatal("codex present while signed out")
	}
	codexLogin(t, h, time.Now().Add(time.Hour))
	p, err := Find("codex")
	if err != nil || p.Account == nil || p.Account.User != "me@x.com" || p.Account.Plan != "plus" || !p.Ready() {
		t.Fatalf("%+v %v", p, err)
	}
	var found bool
	for _, e := range Catalog() {
		if e.ID == "codex/gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Fatal("codex/gpt-5.5 not in catalog")
	}
	// save keeps only models
	p.Key = "secret"
	p.Models = []string{"gpt-5.4"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "Codex") || !strings.Contains(string(b), "gpt-5.4") {
		t.Fatalf("stored: %s", b)
	}
	p, _ = Find("codex")
	if len(p.Exposed()) != 1 {
		t.Fatal("picks lost")
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if p, err = Find("codex"); err != nil || len(p.Models) != 0 {
		t.Fatal("delete should only clear picks")
	}
	// api key mode is not an account
	writeFile(t, filepath.Join(h, ".codex", "auth.json"), `{"auth_mode":"apikey","OPENAI_API_KEY":"sk"}`)
	if _, err := Find("codex"); err == nil {
		t.Fatal("apikey mode offered")
	}
}

func TestStaleRecordIgnored(t *testing.T) {
	env(t)
	writeFile(t, Path(), `{"providers":[{"id":"codex","models":["x"]},{"id":"claude","key":"tok","chat":"http://x"}]}`)
	if len(All()) != 0 {
		t.Fatalf("%+v", All())
	}
}

func TestCodexRefresh(t *testing.T) {
	h := env(t)
	codexLogin(t, h, time.Now().Add(time.Minute))
	newAccess := jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	var gotRefresh string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]string
		json.NewDecoder(r.Body).Decode(&m)
		gotRefresh = m["refresh_token"]
		json.NewEncoder(w).Encode(map[string]string{"access_token": newAccess, "refresh_token": "r2"})
	}))
	defer srv.Close()
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	defer func() { CodexRefreshURL = old }()
	p, _ := Find("codex")
	_, hdr, body, err := Prepare(context.Background(), *p, Responses, []byte(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":5,"temperature":1}`))
	if err != nil || gotRefresh != "r1" || hdr["Authorization"] != "Bearer "+newAccess || hdr["chatgpt-account-id"] != "acc" {
		t.Fatalf("%v %v %v", err, gotRefresh, hdr)
	}
	var m map[string]any
	json.Unmarshal(body, &m)
	if m["model"] != "gpt-5.5" || m["stream"] != true || m["store"] != false || m["max_output_tokens"] != nil || m["temperature"] != nil {
		t.Fatalf("%s", body)
	}
	if _, ok := m["input"].([]any); !ok {
		t.Fatalf("%s", body)
	}
	b, _ := os.ReadFile(filepath.Join(h, ".codex", "auth.json"))
	if !strings.Contains(string(b), `"extra"`) || !strings.Contains(string(b), "r2") {
		t.Fatalf("%s", b)
	}
	// refusal → clear message
	codexLogin(t, h, time.Now().Add(time.Minute))
	CodexRefreshURL = "http://127.0.0.1:1"
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv2.Close()
	CodexRefreshURL = srv2.URL
	if _, _, _, err := Prepare(context.Background(), *p, Responses, nil); err == nil || !strings.Contains(err.Error(), "codex login") {
		t.Fatalf("%v", err)
	}
}

func TestCopilot(t *testing.T) {
	h := env(t)
	writeFile(t, filepath.Join(h, "cfg", "github-copilot", "apps.json"), `{"github.com:Iv1":{"user":"octo","oauth_token":"gho_x"}}`)
	p, err := Find("copilot")
	if err != nil || p.Account.User != "octo" {
		t.Fatal(err)
	}
	for _, m := range p.Available() {
		if m.ID == "auto" {
			t.Fatal("auto in builtin")
		}
	}
	if len(p.Available()) == 0 {
		t.Fatal("empty builtin")
	}
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			io := `{"data":[
			{"id":"gpt-4.1","capabilities":{"type":"chat"},"model_picker_enabled":true},
			{"id":"auto","capabilities":{"type":"chat"}},
			{"id":"emb","capabilities":{"type":"embeddings"}},
			{"id":"hidden","capabilities":{"type":"chat"},"model_picker_enabled":false},
			{"id":"off","capabilities":{"type":"chat"},"policy":{"state":"disabled"}}]}`
			w.Write([]byte(io))
			return
		}
		if r.Header.Get("Authorization") != "token gho_x" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	defer api.Close()
	old := CopilotTokenURL
	CopilotTokenURL = api.URL + "/tok"
	defer func() { CopilotTokenURL = old }()
	ms, err := p.Fetch(context.Background())
	if err != nil || len(ms) != 1 || ms[0].ID != "gpt-4.1" {
		t.Fatalf("%v %v", ms, err)
	}
	p, _ = Find("copilot")
	if av := p.Available(); len(av) != 1 {
		t.Fatalf("%v", av)
	}
	base, hdr, _, err := Prepare(context.Background(), *p, Chat, []byte(`{"messages":[{"role":"user","content":"a"},{"role":"tool","content":"b"}]}`))
	if err != nil || base != api.URL || hdr["X-Initiator"] != "agent" || hdr["Authorization"] != "Bearer sess" {
		t.Fatalf("%v %v %v", err, base, hdr)
	}
	_, hdr, _, _ = Prepare(context.Background(), *p, Chat, []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`))
	if hdr["X-Initiator"] != "user" || hdr["Copilot-Vision-Request"] != "true" {
		t.Fatalf("%v", hdr)
	}
}

func TestClaudeNote(t *testing.T) {
	h := env(t)
	if _, ok := ClaudeNote(); ok {
		t.Fatal("not signed in")
	}
	writeFile(t, filepath.Join(h, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"TOPSECRET"}}`)
	note, ok := ClaudeNote()
	if !ok || strings.Contains(note, "TOPSECRET") {
		t.Fatal(note)
	}
	for _, p := range All() {
		if p.ID == "claude" {
			t.Fatal("claude offered")
		}
	}
	if err := Save(Provider{ID: "claude", Chat: "http://x", Key: "k"}); err == nil {
		t.Fatal("saved claude")
	}
}
