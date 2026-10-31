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
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]any{"alg": "none"}) + "." + enc(claims) + ".sig"
}

// env gives every test its own HOME and config dirs.
func env(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

func writeCodex(t *testing.T, home string, auth map[string]any) string {
	dir := filepath.Join(home, ".codex")
	os.MkdirAll(dir, 0o755)
	b, _ := json.Marshal(auth)
	p := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func codexAuth(exp int64) map[string]any {
	return map[string]any{
		"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "extra": map[string]any{"keep": "me"},
		"tokens": map[string]any{
			"id_token": jwt(map[string]any{"email": "me@x.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}}),
			"access_token":  jwt(map[string]any{"exp": exp}),
			"refresh_token": "r1", "account_id": "acct-1",
		},
	}
}

func writeCopilot(t *testing.T, home string) {
	dir := filepath.Join(home, ".config", "github-copilot")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "apps.json"), []byte(`{"github.com:Iv1.x":{"user":"octocat","oauth_token":"gho_abc"}}`), 0o600)
}

func TestAccountsDetectAndLogout(t *testing.T) {
	home := env(t)
	if len(Accounts()) != 0 {
		t.Fatal("no accounts expected")
	}
	cp := writeCodex(t, home, codexAuth(time.Now().Add(time.Hour).Unix()))
	writeCopilot(t, home)
	all := All()
	if len(all) != 2 || all[0].ID != "codex" || all[1].ID != "copilot" {
		t.Fatalf("got %+v", all)
	}
	if a := all[0].Account; a.User != "me@x.com" || a.Plan != "pro" || a.Agent != "codex" {
		t.Fatalf("codex account %+v", a)
	}
	if all[1].Account.User != "octocat" || !all[0].Ready() || !all[1].Ready() {
		t.Fatalf("copilot %+v", all[1])
	}
	cat := Catalog()
	ids := map[string]bool{}
	for _, e := range cat {
		ids[e.ID] = true
	}
	if !ids["codex/gpt-5.5"] || !ids["copilot/claude-sonnet-4.5"] || ids["copilot/auto"] {
		t.Fatalf("catalog %v", IDs())
	}
	p, m, ok := Resolve("codex/gpt-5.5")
	if !ok || p.ID != "codex" || m != "gpt-5.5" {
		t.Fatal("resolve", p.ID, m, ok)
	}
	if b, _ := json.Marshal(all[0]); strings.Contains(string(b), "access") || strings.Contains(string(b), "r1") {
		t.Fatalf("token leaked: %s", b)
	}
	// logged out, with a stale selection record left in the config
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	os.Remove(cp)
	for _, p := range All() {
		if p.ID == "codex" {
			t.Fatal("codex survived logout")
		}
	}
	if _, err := Find("codex"); err == nil {
		t.Fatal("found logged-out codex")
	}
}

func TestCodexAPIKeyModeIsNotAccount(t *testing.T) {
	home := env(t)
	writeCodex(t, home, map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "sk-1"})
	writeCodex(t, home, map[string]any{"OPENAI_API_KEY": "sk-1"})
	if len(Accounts()) != 0 {
		t.Fatal("api key mode counted")
	}
	a := codexAuth(time.Now().Add(time.Hour).Unix())
	a["auth_mode"] = "apikey"
	writeCodex(t, home, a)
	if len(Accounts()) != 0 {
		t.Fatal("apikey auth_mode counted")
	}
}

func TestCodexNameFallback(t *testing.T) {
	home := env(t)
	writeCodex(t, home, map[string]any{"tokens": map[string]any{"access_token": "a", "account_id": "x"}})
	a := Accounts()
	if len(a) != 1 || a[0].Account.User != "ChatGPT" {
		t.Fatalf("%+v", a)
	}
}

func TestSaveAndDeleteAccount(t *testing.T) {
	home := env(t)
	writeCodex(t, home, codexAuth(time.Now().Add(time.Hour).Unix()))
	err := Save(Provider{ID: "codex", Name: "Evil", Key: "sk-secret", Chat: "http://evil.example", Models: []string{"gpt-5.5", "gpt-5.4"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "evil") || strings.Contains(string(b), "Evil") {
		t.Fatalf("config leaked: %s", b)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	json.Unmarshal(b, &f)
	if len(f.Providers) != 1 || len(f.Providers[0]) != 2 {
		t.Fatalf("config %s", b)
	}
	p, _ := Find("codex")
	if p.Chat != "" || p.Name != "Codex" || len(p.Exposed()) != 2 {
		t.Fatalf("%+v", p)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p, err = Find("codex")
	if err != nil || len(p.Models) != 0 || len(p.Exposed()) != len(p.Available()) {
		t.Fatalf("after delete %+v %v", p, err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
}

func TestUserProviderWinsOverAccount(t *testing.T) {
	home := env(t)
	writeCodex(t, home, codexAuth(time.Now().Add(time.Hour).Unix()))
	os.MkdirAll(filepath.Dir(Path()), 0o755)
	os.WriteFile(Path(), []byte(`{"providers":[{"id":"codex","name":"Mine","key":"k","chat":"https://my.example/v1"}]}`), 0o600)
	all := All()
	n := 0
	for _, p := range all {
		if p.ID == "codex" {
			n++
			if p.Account != nil || p.Name != "Mine" {
				t.Fatalf("%+v", p)
			}
		}
	}
	if n != 1 {
		t.Fatalf("want one codex, got %d", n)
	}
}

func TestPrepare(t *testing.T) {
	p := Provider{ID: "codex", Account: &Account{Agent: "codex"}}
	out := p.Prepare([]byte(`{"model":"m","input":"hello","max_output_tokens":9,"temperature":0.5,"top_p":1,"reasoning":{"effort":"high"}}`))
	var m map[string]any
	json.Unmarshal(out, &m)
	if m["stream"] != true || m["store"] != false {
		t.Fatalf("%s", out)
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := m[k]; ok {
			t.Fatalf("%s kept", k)
		}
	}
	if m["reasoning"] == nil {
		t.Fatal("reasoning dropped")
	}
	in := m["input"].([]any)
	it := in[0].(map[string]any)
	if it["role"] != "user" || it["content"].([]any)[0].(map[string]any)["text"] != "hello" {
		t.Fatalf("%s", out)
	}
	plain := []byte(`{"a":1}`)
	if string((Provider{}).Prepare(plain)) != string(plain) {
		t.Fatal("plain changed")
	}
}

func TestSignPlain(t *testing.T) {
	req, _ := http.NewRequest("POST", "http://x/v1/messages", nil)
	p := Provider{Key: "k", Anthropic: "https://proxy.example"}
	if err := p.Sign(context.Background(), req, Anthropic, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("x-api-key") != "k" || req.Header.Get("Authorization") != "Bearer k" {
		t.Fatal(req.Header)
	}
}

func TestCodexSign(t *testing.T) {
	home := env(t)
	writeCodex(t, home, codexAuth(time.Now().Add(time.Hour).Unix()))
	p := Accounts()[0]
	req, _ := http.NewRequest("POST", CodexBase+"/responses", nil)
	if err := p.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") || req.Header.Get("chatgpt-account-id") != "acct-1" {
		t.Fatal(req.Header)
	}
}

func TestCodexRefresh(t *testing.T) {
	home := env(t)
	path := writeCodex(t, home, codexAuth(time.Now().Add(2*time.Minute).Unix()))
	os.Chmod(path, 0o600)
	newAccess := jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["grant_type"] != "refresh_token" || in["refresh_token"] != "r1" || in["client_id"] == "" {
			t.Errorf("bad refresh body %v", in)
		}
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"r2"}`, newAccess)
	}))
	defer srv.Close()
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	defer func() { CodexRefreshURL = old }()
	p := Accounts()[0]
	req, _ := http.NewRequest("POST", "http://x/responses", nil)
	if err := p.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer "+newAccess {
		t.Fatal("old token used")
	}
	// second sign: no more refresh
	p.Sign(context.Background(), req, Responses, nil)
	if calls != 1 {
		t.Fatalf("refreshed %d times", calls)
	}
	b, _ := os.ReadFile(path)
	var f map[string]any
	json.Unmarshal(b, &f)
	tok := f["tokens"].(map[string]any)
	if tok["access_token"] != newAccess || tok["refresh_token"] != "r2" || tok["account_id"] != "acct-1" || tok["id_token"] == "" {
		t.Fatalf("tokens %v", tok)
	}
	if f["extra"] == nil || f["auth_mode"] != "chatgpt" || f["last_refresh"] == nil {
		t.Fatalf("fields lost: %s", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestCodexRefreshFails(t *testing.T) {
	home := env(t)
	writeCodex(t, home, codexAuth(time.Now().Add(time.Minute).Unix()))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	defer func() { CodexRefreshURL = old }()
	req, _ := http.NewRequest("POST", "http://x/responses", nil)
	err := Accounts()[0].Sign(context.Background(), req, Responses, nil)
	if err == nil || !strings.Contains(err.Error(), "signed out") || !strings.Contains(err.Error(), "codex login") {
		t.Fatalf("err %v", err)
	}
}

func copilotServer(t *testing.T, models string) (*httptest.Server, *int) {
	exchanges := new(int)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			*exchanges++
			if r.Header.Get("Authorization") != "token gho_abc" {
				t.Errorf("auth %q", r.Header.Get("Authorization"))
			}
			fmt.Fprintf(w, `{"token":"sess","expires_at":%d,"endpoints":{"api":%q}}`, time.Now().Add(time.Hour).Unix(), srv.URL+"/api")
		case "/api/models":
			if r.Header.Get("Authorization") != "Bearer sess" || r.Header.Get("Copilot-Integration-Id") == "" {
				t.Errorf("models headers %v", r.Header)
			}
			w.Write([]byte(models))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	t.Cleanup(func() { CopilotTokenURL = old })
	return srv, exchanges
}

func TestCopilotModelsAndSign(t *testing.T) {
	home := env(t)
	writeCopilot(t, home)
	_, exchanges := copilotServer(t, `{"data":[
	 {"id":"gpt-5","name":"GPT 5","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}}},
	 {"id":"emb","model_picker_enabled":true,"capabilities":{"type":"embeddings"}},
	 {"id":"gpt-5-secondary","model_picker_enabled":true,"capabilities":{"type":"chat"}},
	 {"id":"copilot-search-x","model_picker_enabled":true,"capabilities":{"type":"chat"}},
	 {"id":"dis","model_picker_enabled":false,"policy":{"state":"disabled"},"capabilities":{"type":"chat"}},
	 {"id":"pol","model_picker_enabled":false,"policy":{"state":"enabled"},"capabilities":{"type":"chat"}}]}`)
	p, err := Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].ID != "gpt-5" || ms[1].ID != "pol" || len(ms[0].Efforts) != 2 {
		t.Fatalf("%+v", ms)
	}
	av := p.Available()
	if len(av) != 2 {
		t.Fatalf("available %+v", av)
	}
	// signing
	body := []byte(`{"messages":[{"role":"user","content":"a"},{"role":"tool","content":"x"}]}`)
	req, _ := http.NewRequest("POST", "https://api.githubcopilot.com/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, body); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(req.URL.String(), "/api/chat/completions") || req.Header.Get("Authorization") != "Bearer sess" ||
		req.Header.Get("X-Initiator") != "agent" || req.Header.Get("Copilot-Integration-Id") == "" {
		t.Fatal(req.URL, req.Header)
	}
	req2, _ := http.NewRequest("POST", "https://api.githubcopilot.com/chat/completions", nil)
	p.Sign(context.Background(), req2, Chat, []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"x"}}]}]}`))
	if req2.Header.Get("X-Initiator") != "user" || req2.Header.Get("Copilot-Vision-Request") != "true" {
		t.Fatal(req2.Header)
	}
	if *exchanges != 1 {
		t.Fatalf("token exchanged %d times", *exchanges)
	}
}

func TestCopilotNoChatModels(t *testing.T) {
	home := env(t)
	writeCopilot(t, home)
	copilotServer(t, `{"data":[{"id":"emb","model_picker_enabled":true,"capabilities":{"type":"embeddings"}}]}`)
	p, _ := Find("copilot")
	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestExcludedClaude(t *testing.T) {
	home := env(t)
	if len(Excluded()) != 0 {
		t.Fatal("unexpected")
	}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"secret":"TOPSECRET"}`), 0o600)
	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "claude" || !strings.Contains(ex[0].Why, "Claude Code") || !strings.Contains(ex[0].Why, "API key") ||
		strings.Contains(ex[0].Why, "TOPSECRET") {
		t.Fatalf("%+v", ex)
	}
	for _, p := range All() {
		if p.ID == "claude" {
			t.Fatal("claude offered")
		}
	}
}
