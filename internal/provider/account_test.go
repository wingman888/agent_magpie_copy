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
	isolate(t)
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
	// Codex lists its models in its own cache; magpie reads that, nothing else.
	writeFile(t, filepath.Join(home, ".codex", "models_cache.json"), map[string]any{
		"models": []any{map[string]any{"slug": "gpt-5.5", "display_name": "GPT-5.5", "visibility": "list", "priority": 1}},
	})
	writeFile(t, filepath.Join(home, ".config", "github-copilot", "apps.json"), map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "octocat", "oauth_token": "gho_x"},
	})
	return home
}

// isolate keeps tests off the machine's own Claude Code Keychain login and
// forgets anything a previous test cached.
func isolate(t *testing.T) {
	t.Helper()
	oldKeychain, oldURL, oldBase := claudeKeychain, claudeTokenURL, claudeBase
	claudeKeychain = false
	forgetClaudeCredential()
	t.Cleanup(func() {
		claudeKeychain, claudeTokenURL, claudeBase = oldKeychain, oldURL, oldBase
		forgetClaudeCredential()
	})
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
	if !contains(ids, "codex/gpt-5.5") {
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

	// no Claude credential in this home: no claude provider
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("claude listed without credentials")
	}
}

// claudeSignIn writes a Claude Code OAuth login into a temp home.
func claudeSignIn(t *testing.T, home string, expiry time.Time) string {
	t.Helper()
	path := filepath.Join(home, ".claude", ".credentials.json")
	writeFile(t, path, map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken": "sk-ant-oat01-old", "refreshToken": "sk-ant-ort01-old",
			"expiresAt": expiry.UnixMilli(), "subscriptionType": "max",
			"scopes": []string{"user:inference", "user:profile"},
		},
		// what magpie must not clobber when it writes a refreshed token back
		"mcpOAuth": map[string]any{"plugin:slack:slack|x": map[string]any{"serverName": "Slack"}},
	})
	return path
}

// claudeHome sets up a temp home with the usual env; the caller adds credentials.
func claudeHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	forgetClaudeCredential()
	return home
}

func TestClaudeAccountIsProvider(t *testing.T) {
	home := claudeHome(t)
	creds := claudeSignIn(t, home, time.Now().Add(time.Hour))

	// Anthropic lists its models live; magpie trusts that answer, not the binary.
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer sk-ant-oat01-old" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"has_more": false, "data": []any{
			map[string]any{"id": "claude-sonnet-5", "display_name": "Claude Sonnet 5"},
			map[string]any{"id": "claude-opus-5-5", "display_name": "Claude Opus 5.5"},
		}})
	}))
	defer models.Close()
	claudeBase = models.URL

	p, ok := find(All(), "claude")
	if !ok || p.Account == nil || p.Account.User != "Claude Max" || p.Account.Plan != "max" || !p.Ready() {
		t.Fatalf("claude: %+v %v", p, ok)
	}
	if p.Anthropic != claudeBase || p.Chat != "" || p.Responses != "" || p.Host() == "" {
		t.Fatalf("endpoints: %+v", p)
	}

	// nothing is compiled in: before a fetch there is nothing to show, and
	// afterwards exactly what the vendor listed, "Opus 5.5" included
	if got := len(p.Available()); got != 0 {
		t.Fatalf("available before a fetch: %d", got)
	}
	if ms, err := p.Fetch(context.Background()); err != nil || len(ms) != 2 {
		t.Fatalf("fetch: %v %v", ms, err)
	}
	p, _ = find(All(), "claude")
	if at, ok := p.Fetched(); !ok || time.Since(at) > time.Minute {
		t.Fatalf("not marked fetched: %v %v", at, ok)
	}
	var ids []string
	for _, e := range Catalog() {
		ids = append(ids, e.ID)
	}
	if !contains(ids, "claude/claude-opus-5-5") {
		t.Fatalf("catalog: %v", ids)
	}
	if rp, model, ok := Resolve("claude/claude-opus-5-5"); !ok || rp.ID != "claude" || model != "claude-opus-5-5" {
		t.Fatalf("resolve: %+v %q %v", rp, model, ok)
	}

	// picks are saved, the login never is
	if err := Save(Provider{ID: "claude", Name: "x", Models: []string{"claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "sk-ant-") || !strings.Contains(string(b), `"claude-opus-5-5"`) {
		t.Fatalf("stored: %s", b)
	}
	p, _ = find(All(), "claude")
	if p.Account == nil || len(p.Exposed()) != 1 || p.Exposed()[0].ID != "claude-opus-5-5" {
		t.Fatalf("picks: %+v", p.Exposed())
	}

	req, _ := http.NewRequest("POST", p.Anthropic+"/v1/messages", nil)
	req.Header.Set("anthropic-beta", "fine-grained-tool-streaming-2025-05-14")
	if err := p.Sign(context.Background(), req, Anthropic, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer sk-ant-oat01-old" {
		t.Fatalf("auth: %q", req.Header.Get("Authorization"))
	}
	beta := req.Header.Get("anthropic-beta")
	for _, want := range []string{"interleaved-thinking-2025-05-14", "oauth-2025-04-20", "claude-code-20250219", "effort-2025-11-24"} {
		if !strings.Contains(beta, want) {
			t.Fatalf("betas: %q", beta)
		}
	}
	if ua := req.Header.Get("User-Agent"); !strings.HasPrefix(ua, "claude-cli/") || !strings.HasSuffix(ua, " (external, cli)") {
		t.Fatalf("user-agent: %q", ua)
	}
	for _, h := range []string{"X-Claude-Code-Session-Id", "X-Client-Request-Id", "X-Stainless-Package-Version", "X-Stainless-Runtime-Version"} {
		if req.Header.Get(h) == "" {
			t.Fatalf("missing %s: %v", h, req.Header)
		}
	}
	if req.Header.Get("x-app") != "cli" || req.Header.Get("anthropic-dangerous-direct-browser-access") != "true" {
		t.Fatalf("Claude Code headers: %v", req.Header)
	}
	if prepared := p.Prepare([]byte(`{"model":"claude-sonnet-5","messages":[]}`)); !strings.Contains(string(prepared), "x-anthropic-billing-header") {
		t.Fatalf("prepare: %s", prepared)
	}

	// signing out of Claude Code removes the provider
	os.Remove(creds)
	forgetClaudeCredential()
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("claude still listed after sign-out")
	}
}

func TestClaudeRefreshesToken(t *testing.T) {
	home := claudeHome(t)
	creds := claudeSignIn(t, home, time.Now().Add(-time.Minute))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "refresh_token" || body["client_id"] != claudeClientID || body["refresh_token"] != "sk-ant-ort01-old" {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "sk-ant-oat01-new", "refresh_token": "sk-ant-ort01-new", "expires_in": 3600})
	}))
	defer srv.Close()
	claudeTokenURL = srv.URL

	p, _ := find(All(), "claude")
	req, _ := http.NewRequest("POST", p.Anthropic+"/v1/messages", nil)
	if err := p.Sign(context.Background(), req, Anthropic, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer sk-ant-oat01-new" {
		t.Fatalf("auth: %q", req.Header.Get("Authorization"))
	}

	// the rotated pair is written back where Claude Code will find it, and
	// everything else in the file survives
	var raw map[string]any
	if !readJSON(creds, &raw) {
		t.Fatalf("credentials unreadable: %s", creds)
	}
	oauth, _ := raw["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "sk-ant-oat01-new" || oauth["refreshToken"] != "sk-ant-ort01-new" || oauth["subscriptionType"] != "max" {
		t.Fatalf("oauth: %v", oauth)
	}
	if exp, _ := oauth["expiresAt"].(float64); exp <= float64(time.Now().UnixMilli()) {
		t.Fatalf("expiry not advanced: %v", oauth["expiresAt"])
	}
	if mcp, _ := raw["mcpOAuth"].(map[string]any); mcp == nil {
		t.Fatalf("mcpOAuth lost: %v", raw)
	}
	if b, _ := os.ReadFile(creds); strings.Contains(string(b), "sk-ant-ort01-old") {
		t.Fatalf("old refresh token kept: %s", b)
	}
}

func TestCodexSignAndBody(t *testing.T) {
	signIn(t)
	p, _ := find(All(), "codex")
	req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
	if err := p.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer h.") || req.Header.Get("chatgpt-account-id") != "acct-1" || req.Header.Get("originator") != "magpie" {
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

func TestClaudeXXHash64(t *testing.T) {
	if got := fmt.Sprintf("%016x", xxHash64(nil, 0)); got != "ef46db3751d8e999" {
		t.Fatalf("xxhash empty vector: %s", got)
	}
}

func TestClaudeBillingBody(t *testing.T) {
	billingFirst := func(b []byte) bool {
		var m struct {
			System []struct {
				Text string `json:"text"`
			} `json:"system"`
		}
		if json.Unmarshal(b, &m) != nil || len(m.System) == 0 {
			return false
		}
		return strings.Contains(m.System[0].Text, "x-anthropic-billing-header")
	}

	out := claudeBody([]byte(`{"model":"claude-sonnet-5","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if !billingFirst(out) {
		t.Fatalf("no system: %s", out)
	}

	out = claudeBody([]byte(`{"model":"m","system":"you are helpful","messages":[]}`))
	if !billingFirst(out) || !strings.Contains(string(out), "you are helpful") {
		t.Fatalf("string system: %s", out)
	}

	out = claudeBody([]byte(`{"model":"m","system":[{"type":"text","text":"be nice"}],"messages":[]}`))
	if !billingFirst(out) || !strings.Contains(string(out), "be nice") {
		t.Fatalf("list system: %s", out)
	}

	// Claude Code's own stale block is normalized so UA, cc_version and CCH
	// remain mutually consistent.
	in := []byte(`{"model":"m","system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.100.7c4; cc_entrypoint=sdk-cli;"}],"messages":[]}`)
	out = claudeBody(in)
	if !strings.Contains(string(out), "cc_version="+claudeClaimedVersion()+".") || strings.Contains(string(out), "cch=00000") || !strings.Contains(string(out), "You are Claude Code") {
		t.Fatalf("stale identity not normalized: %s", out)
	}
	var cloaked map[string]any
	if json.Unmarshal(out, &cloaked) != nil {
		t.Fatalf("cloaked JSON: %s", out)
	}
	metadata, _ := cloaked["metadata"].(map[string]any)
	if user, _ := metadata["user_id"].(string); !strings.Contains(user, "device_id") || !strings.Contains(user, "session_id") {
		t.Fatalf("metadata identity: %v", metadata)
	}

	// unrelated fields and model names survive
	out = claudeBody([]byte(`{"model":"claude-sonnet-5","tools":[{"name":"x"}],"messages":[]}`))
	var m map[string]any
	if json.Unmarshal(out, &m) != nil || m["model"] != "claude-sonnet-5" || m["tools"] == nil {
		t.Fatalf("fields lost: %s", out)
	}

	// a body that is not JSON is passed through
	if out := claudeBody([]byte(`not json`)); string(out) != "not json" {
		t.Fatalf("non-json: %s", out)
	}
}
