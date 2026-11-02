package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

func writeCodex(t *testing.T, home string, exp time.Time, extra map[string]any) {
	m := map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "extra": "keep",
		"tokens": map[string]any{
			"id_token":      jwt(map[string]any{"email": "me@x.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}}),
			"access_token":  jwt(map[string]any{"exp": exp.Unix()}),
			"refresh_token": "r1", "account_id": "acc1"}}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600)
}

func writeCopilot(t *testing.T, home string) {
	d := filepath.Join(home, ".config", "github-copilot")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "apps.json"), []byte(`{"github.com:Iv1.x":{"user":"octo","oauth_token":"gho_abc"}}`), 0o600)
}

func TestAccountsAndStorage(t *testing.T) {
	home := env(t)
	writeCodex(t, home, time.Now().Add(time.Hour), nil)
	writeCopilot(t, home)
	ids := map[string]Provider{}
	for _, p := range All() {
		ids[p.ID] = p
	}
	c, k := ids["codex"], ids["copilot"]
	if c.Account == nil || c.Account.User != "me@x.com" || c.Account.Plan != "pro" || !c.Ready() {
		t.Fatalf("codex: %+v", c)
	}
	if k.Account == nil || k.Account.User != "octo" || !k.Ready() {
		t.Fatalf("copilot: %+v", k)
	}
	var haveAuto, haveSonnet bool
	for _, e := range Catalog() {
		haveAuto = haveAuto || e.ID == "copilot/auto"
		haveSonnet = haveSonnet || e.ID == "copilot/claude-sonnet-4.5"
	}
	if haveAuto || !haveSonnet {
		t.Fatal("copilot catalog wrong")
	}
	if p, m, ok := Resolve("codex/gpt-5.5"); !ok || p.ID != "codex" || m != "gpt-5.5" {
		t.Fatal("resolve")
	}
	// save: only models survive
	if err := Save(Provider{ID: "codex", Chat: "http://evil.example", Key: "sekret", Models: []string{"gpt-5.5"}, Account: c.Account}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "evil") || strings.Contains(string(b), "sekret") || strings.Contains(string(b), "me@x.com") {
		t.Fatalf("leaked: %s", b)
	}
	p, _ := Find("codex")
	if ex := p.Exposed(); len(ex) != 1 || ex[0].ID != "gpt-5.5" {
		t.Fatalf("exposed %v", ex)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil || len(p.Models) != 0 {
		t.Fatal("delete should only clear the choice")
	}
	// logout with a stale record
	Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}})
	os.Remove(filepath.Join(home, ".codex", "auth.json"))
	for _, p := range All() {
		if p.ID == "codex" {
			t.Fatal("codex should be gone")
		}
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Fatal("catalog has codex")
		}
	}
	// api key mode is no account
	writeCodex(t, home, time.Now().Add(time.Hour), map[string]any{"auth_mode": "apikey"})
	if _, err := Find("codex"); err == nil {
		t.Fatal("apikey mode counted")
	}
}

func TestCustomWins(t *testing.T) {
	home := env(t)
	writeCodex(t, home, time.Now().Add(time.Hour), nil)
	os.MkdirAll(filepath.Dir(Path()), 0o755)
	os.WriteFile(Path(), []byte(`{"providers":[{"id":"codex","name":"Mine","key":"k","chat":"http://x/v1"}]}`), 0o600)
	n := 0
	for _, p := range All() {
		if p.ID == "codex" {
			n++
			if p.Account != nil {
				t.Fatal("account shadowed custom")
			}
		}
	}
	if n != 1 {
		t.Fatal(n)
	}
}

func TestExcluded(t *testing.T) {
	home := env(t)
	if len(Excluded()) != 0 {
		t.Fatal("none expected")
	}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"x":1}`), 0o600)
	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "claude" || ex[0].Why == "" {
		t.Fatalf("%v", ex)
	}
	for _, p := range All() {
		if p.ID == "claude" {
			t.Fatal("claude offered")
		}
	}
}

func TestCodexSignAndRefresh(t *testing.T) {
	home := env(t)
	writeCodex(t, home, time.Now().Add(time.Minute), nil)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["grant_type"] != "refresh_token" || in["refresh_token"] != "r1" {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}), "refresh_token": "r2"})
	}))
	defer srv.Close()
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	defer func() { CodexRefreshURL = old }()
	oldBase := CodexBase
	CodexBase = "http://codex.test/backend"
	defer func() { CodexBase = oldBase }()
	p, _ := Find("codex")
	req, _ := http.NewRequest("POST", "http://placeholder/x", nil)
	if err := p.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || req.URL.String() != "http://codex.test/backend/responses" || req.Header.Get("chatgpt-account-id") != "acc1" {
		t.Fatalf("calls %d url %s", calls, req.URL)
	}
	if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") {
		t.Fatal("no bearer")
	}
	b, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["extra"] != "keep" || m["last_refresh"] == nil || m["tokens"].(map[string]any)["refresh_token"] != "r2" || m["tokens"].(map[string]any)["account_id"] != "acc1" {
		t.Fatalf("file: %s", b)
	}
	st, _ := os.Stat(filepath.Join(home, ".codex", "auth.json"))
	if st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
	// second sign: no refresh
	p.Sign(context.Background(), req, Responses, nil)
	if calls != 1 {
		t.Fatal("refreshed again")
	}
	// failing refresh
	writeCodex(t, home, time.Now().Add(time.Minute), map[string]any{"tokens": map[string]any{"access_token": jwt(map[string]any{"exp": time.Now().Add(time.Minute).Unix()}), "refresh_token": "bad", "account_id": "a"}})
	if err := p.Sign(context.Background(), req, Responses, nil); err == nil || !strings.Contains(err.Error(), "signed out") {
		t.Fatalf("err %v", err)
	}
}

func TestPrepareCodex(t *testing.T) {
	p := Provider{Account: &Account{Agent: "codex"}}
	out := p.Prepare([]byte(`{"model":"gpt-5.5","input":"hi","max_output_tokens":5,"temperature":1,"reasoning":{"effort":"high"},"stream":false}`))
	var m map[string]any
	json.Unmarshal(out, &m)
	if m["stream"] != true || m["store"] != false || m["max_output_tokens"] != nil || m["temperature"] != nil || m["reasoning"] == nil {
		t.Fatalf("%s", out)
	}
	if in, ok := m["input"].([]any); !ok || len(in) != 1 {
		t.Fatalf("%s", out)
	}
	if got := (Provider{}).Prepare([]byte(`x`)); string(got) != "x" {
		t.Fatal("plain changed")
	}
}

func TestCopilot(t *testing.T) {
	home := env(t)
	writeCopilot(t, home)
	exch := 0
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer sess" || r.Header.Get("Copilot-Integration-Id") == "" {
			w.WriteHeader(400)
			return
		}
		fmt.Fprint(w, `{"data":[
		 {"id":"gpt-5","name":"GPT 5","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}}},
		 {"id":"emb","capabilities":{"type":"embeddings"},"model_picker_enabled":true},
		 {"id":"x-secondary","capabilities":{"type":"chat"},"model_picker_enabled":true},
		 {"id":"off","capabilities":{"type":"chat"},"policy":{"state":"disabled"}},
		 {"id":"pol","capabilities":{"type":"chat"},"policy":{"state":"enabled"}}]}`)
	}))
	defer api.Close()
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exch++
		if r.Header.Get("Authorization") != "token gho_abc" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprintf(w, `{"token":"sess","expires_at":%d,"endpoints":{"api":%q}}`, time.Now().Add(time.Hour).Unix(), api.URL)
	}))
	defer tok.Close()
	old := CopilotTokenURL
	CopilotTokenURL = tok.URL
	defer func() { CopilotTokenURL = old }()
	p, _ := Find("copilot")
	ms, err := p.Fetch(context.Background())
	if err != nil || len(ms) != 2 || ms[0].ID != "gpt-5" || len(ms[0].Efforts) != 2 || ms[1].ID != "pol" {
		t.Fatalf("%v %v", ms, err)
	}
	av := p.Available()
	if len(av) != 2 || len(av[0].Efforts) != 2 {
		t.Fatalf("%v", av)
	}
	req, _ := http.NewRequest("POST", "http://placeholder/chat/completions", nil)
	body := []byte(`{"messages":[{"role":"user","content":"a"},{"role":"tool","content":"b"}]}`)
	if err := p.Sign(context.Background(), req, Chat, body); err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != api.URL+"/chat/completions" || req.Header.Get("X-Initiator") != "agent" || req.Header.Get("Authorization") != "Bearer sess" {
		t.Fatalf("%s %v", req.URL, req.Header)
	}
	req2, _ := http.NewRequest("POST", "http://p/", nil)
	p.Sign(context.Background(), req2, Chat, []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"u"}}]}]}`))
	if req2.Header.Get("X-Initiator") != "user" || req2.Header.Get("Copilot-Vision-Request") != "true" {
		t.Fatal(req2.Header)
	}
	if exch != 1 {
		t.Fatalf("exchanges %d", exch)
	}
}
