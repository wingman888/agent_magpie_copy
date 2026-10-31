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

func env(t *testing.T) (home, cfg string) {
	t.Helper()
	home, cfg = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return
}

func writeCodex(t *testing.T, home string, v map[string]any) string {
	t.Helper()
	p := filepath.Join(home, ".codex", "auth.json")
	os.MkdirAll(filepath.Dir(p), 0o700)
	b, _ := json.Marshal(v)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func codexFile(access string) map[string]any {
	return map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "extra": "keep",
		"tokens": map[string]any{"access_token": access, "refresh_token": "r1", "account_id": "acc-1",
			"id_token": jwt(map[string]any{"email": "a@b.c", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}})}}
}

func writeCopilot(t *testing.T, cfg string) {
	t.Helper()
	p := filepath.Join(cfg, "github-copilot", "apps.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(`{"github.com:Iv1.x":{"user":"octo","oauth_token":"gho_abc"}}`), 0o600)
}

func TestAccountsAppearAndVanish(t *testing.T) {
	home, cfg := env(t)
	if len(Accounts()) != 0 {
		t.Fatal("expected no accounts")
	}
	cp := writeCodex(t, home, codexFile("tok"))
	writeCopilot(t, cfg)
	var ids []string
	for _, p := range All() {
		ids = append(ids, p.ID)
		if !p.Ready() {
			t.Errorf("%s not ready", p.ID)
		}
	}
	if strings.Join(ids, ",") != "codex,copilot" {
		t.Fatalf("ids = %v", ids)
	}
	c, _ := Find("codex")
	if c.Account.User != "a@b.c" || c.Account.Plan != "pro" {
		t.Fatalf("codex account %+v", c.Account)
	}
	if cp, _ := Find("copilot"); cp.Account.User != "octo" {
		t.Fatalf("copilot account %+v", cp.Account)
	}
	var haveCodex, haveAuto bool
	for _, e := range Catalog() {
		haveCodex = haveCodex || e.ID == "codex/gpt-5.5"
		haveAuto = haveAuto || e.ID == "copilot/auto"
	}
	if !haveCodex || haveAuto {
		t.Fatalf("catalog codex=%v auto=%v", haveCodex, haveAuto)
	}
	if p, m, ok := Resolve("codex/gpt-5.5"); !ok || p.ID != "codex" || m != "gpt-5.5" {
		t.Fatal("resolve failed")
	}
	if _, _, ok := Resolve("copilot/claude-sonnet-4.5"); !ok {
		t.Fatal("resolve copilot failed")
	}

	// saved choice keeps only models
	if err := Save(Provider{ID: "codex", Chat: "http://evil", Key: "secret", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "evil") || strings.Contains(string(b), "secret") || strings.Contains(string(b), "tok") {
		t.Fatalf("leaked: %s", b)
	}
	c, _ = Find("codex")
	if len(c.Exposed()) != 1 || c.Exposed()[0].ID != "gpt-5.5" {
		t.Fatalf("exposed %+v", c.Exposed())
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	c, err := Find("codex")
	if err != nil || len(c.Models) != 0 {
		t.Fatalf("after delete: %v %+v", err, c)
	}

	// stale selection + sign out => gone
	Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}})
	os.Remove(cp)
	if _, err := Find("codex"); err == nil {
		t.Fatal("codex should be gone")
	}
	// API-key mode is no account
	writeCodex(t, home, map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "sk-x"})
	if _, err := Find("codex"); err == nil {
		t.Fatal("api key mode is not an account")
	}
}

func TestCustomWins(t *testing.T) {
	home, _ := env(t)
	writeCodex(t, home, codexFile("tok"))
	if err := Save(Provider{ID: "codex", Name: "Mine", Key: "k", Chat: "http://x/v1"}); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range All() {
		if p.ID == "codex" {
			n++
			if p.Account != nil {
				t.Fatal("account overlaid on custom")
			}
		}
	}
	if n != 1 {
		t.Fatalf("n=%d", n)
	}
}

func TestExcludedClaude(t *testing.T) {
	home, _ := env(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "cc"))
	if len(Excluded()) != 0 {
		t.Fatal("unexpected exclusion")
	}
	os.MkdirAll(filepath.Join(home, "cc"), 0o700)
	os.WriteFile(filepath.Join(home, "cc", ".credentials.json"), []byte(`{"secret":"SECRETVALUE"}`), 0o600)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "cc2"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "cc"))
	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "claude" || ex[0].Why == "" || strings.Contains(ex[0].Why, "SECRETVALUE") {
		t.Fatalf("%+v", ex)
	}
	for _, p := range All() {
		if p.ID == "claude" {
			t.Fatal("claude offered")
		}
	}
}

func TestPrepareCodex(t *testing.T) {
	p := Provider{Account: &Account{Agent: "codex"}}
	var m map[string]any
	json.Unmarshal(p.Prepare([]byte(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":9,"temperature":0.2,"reasoning":{"effort":"low"}}`)), &m)
	if m["stream"] != true || m["store"] != false || m["max_output_tokens"] != nil || m["temperature"] != nil || m["model"] != "gpt-5.5" {
		t.Fatalf("%v", m)
	}
	in := m["input"].([]any)[0].(map[string]any)
	if in["role"] != "user" || m["reasoning"] == nil {
		t.Fatalf("%v", m)
	}
	if string((Provider{}).Prepare([]byte("x"))) != "x" {
		t.Fatal("ordinary provider changed body")
	}
}

func TestCodexRefresh(t *testing.T) {
	home, _ := env(t)
	old := jwt(map[string]any{"exp": time.Now().Add(2 * time.Minute).Unix()})
	fresh := jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	path := writeCodex(t, home, codexFile(old))
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		if b["refresh_token"] != "r1" || b["grant_type"] != "refresh_token" {
			t.Errorf("bad refresh body %v", b)
		}
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"r2"}`, fresh)
	}))
	defer srv.Close()
	save := CodexRefreshURL
	CodexRefreshURL = srv.URL
	defer func() { CodexRefreshURL = save }()
	CodexBase = "http://example.invalid/codex"
	c, _ := Find("codex")
	req, _ := http.NewRequest("POST", c.Base(Responses)+"/responses", nil)
	if err := c.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer "+fresh || req.Header.Get("chatgpt-account-id") != "acc-1" {
		t.Fatalf("headers %v", req.Header)
	}
	req2, _ := http.NewRequest("POST", "http://x/responses", nil)
	c.Sign(context.Background(), req2, Responses, nil)
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	b, _ := os.ReadFile(path)
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["extra"] != "keep" || m["tokens"].(map[string]any)["refresh_token"] != "r2" || m["tokens"].(map[string]any)["account_id"] != "acc-1" {
		t.Fatalf("file %s", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}

	// failure => sign-in error
	writeCodex(t, home, codexFile(old))
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
	defer bad.Close()
	CodexRefreshURL = bad.URL
	err := c.Sign(context.Background(), req2, Responses, nil)
	if _, ok := err.(*SignedOutError); !ok || !strings.Contains(err.Error(), "codex login") {
		t.Fatalf("err %v", err)
	}
}

func TestCopilot(t *testing.T) {
	_, cfg := env(t)
	writeCopilot(t, cfg)
	exchanges := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			exchanges++
			if r.Header.Get("Authorization") != "token gho_abc" {
				t.Errorf("auth %q", r.Header.Get("Authorization"))
			}
			fmt.Fprintf(w, `{"token":"sess","expires_at":%d,"endpoints":{"api":%q}}`, time.Now().Add(time.Hour).Unix(), srv.URL+"/api")
		case "/api/models":
			if r.Header.Get("Authorization") != "Bearer sess" || r.Header.Get("Copilot-Integration-Id") == "" {
				t.Errorf("models headers %v", r.Header)
			}
			fmt.Fprint(w, `{"data":[
{"id":"a","capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}},"model_picker_enabled":true},
{"id":"b-secondary","capabilities":{"type":"chat"},"model_picker_enabled":true},
{"id":"c","capabilities":{"type":"embeddings"},"model_picker_enabled":true},
{"id":"d","capabilities":{"type":"chat"},"policy":{"state":"enabled"}},
{"id":"e","capabilities":{"type":"chat"},"policy":{"state":"disabled"}},
{"id":"copilot-search-x","capabilities":{"type":"chat"},"model_picker_enabled":true}]}`)
		}
	}))
	defer srv.Close()
	save := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	defer func() { CopilotTokenURL = save }()
	c, _ := Find("copilot")
	ms, err := c.Fetch(context.Background())
	if err != nil || len(ms) != 2 || ms[0].ID != "a" || ms[1].ID != "d" || len(ms[0].Efforts) != 2 {
		t.Fatalf("%v %+v", err, ms)
	}
	if av := c.Available(); len(av) != 2 {
		t.Fatalf("available %+v", av)
	}
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]},{"role":"tool","content":"r"}]}`)
	req, _ := http.NewRequest("POST", c.Base(Chat)+"/chat/completions", nil)
	if err := c.Sign(context.Background(), req, Chat, body); err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != srv.URL+"/api/chat/completions" || req.Header.Get("X-Initiator") != "agent" ||
		req.Header.Get("Copilot-Vision-Request") != "true" || req.Header.Get("Authorization") != "Bearer sess" {
		t.Fatalf("%v %v", req.URL, req.Header)
	}
	req, _ = http.NewRequest("POST", "http://x/chat/completions", nil)
	c.Sign(context.Background(), req, Chat, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if req.Header.Get("X-Initiator") != "user" || exchanges != 1 {
		t.Fatalf("initiator %q exchanges %d", req.Header.Get("X-Initiator"), exchanges)
	}
}
