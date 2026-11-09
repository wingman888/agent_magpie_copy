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

const defaultCodexBase = "https://chatgpt.com/backend-api/codex"

// acctEnv points every credential and config location at a fresh temp dir.
func acctEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

// acctJWT makes an unsigned token with the given payload, which is all dial reads.
func acctJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// acctWriteCodex writes Codex's auth.json with the given access-token expiry.
func acctWriteCodex(t *testing.T, home string, exp time.Time, extra map[string]any) {
	t.Helper()
	access := acctJWT(map[string]any{"exp": exp.Unix()})
	id := acctJWT(map[string]any{"email": "ada@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
	doc := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      id,
			"access_token":  access,
			"refresh_token": "rt-old",
			"account_id":    "acct-123",
		},
	}
	for k, v := range extra {
		doc[k] = v
	}
	b, _ := json.Marshal(doc)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func acctWriteCopilot(t *testing.T) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"github.com:Iv1.abc":{"user":"octocat","oauth_token":"gho_test"}}`
	if err := os.WriteFile(filepath.Join(dir, "apps.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAcctCodexBodyRewrite(t *testing.T) {
	in := `{"model":"codex/gpt-5.5","input":"hi","stream":false,"store":true,"temperature":0.2,"top_p":0.9,"max_output_tokens":64,"instructions":"be brief"}`
	out, err := CodexBody([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "gpt-5.5" {
		t.Errorf("model = %v, want gpt-5.5", m["model"])
	}
	if m["stream"] != true || m["store"] != false {
		t.Errorf("stream/store = %v/%v, want true/false", m["stream"], m["store"])
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s should be removed, body: %s", k, out)
		}
	}
	if m["instructions"] != "be brief" {
		t.Errorf("instructions lost: %s", out)
	}
	items, ok := m["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input not a one-item list: %s", out)
	}
	item := items[0].(map[string]any)
	part := item["content"].([]any)[0].(map[string]any)
	if item["role"] != "user" || part["type"] != "input_text" || part["text"] != "hi" {
		t.Errorf("bad input item: %s", out)
	}
}

func TestAcctEventStreamDetection(t *testing.T) {
	cases := []struct {
		ct   string
		head string
		want bool
	}{
		{"text/event-stream", "", true},
		{"text/event-stream; charset=utf-8", "{}", true},
		{"", "event: response.created\n", true},
		{"", "\ndata: {}", true},
		{"", `{"error":"x"}`, false},
		{"application/json", "data: x", false},
	}
	for _, c := range cases {
		if got := IsEventStream(c.ct, []byte(c.head)); got != c.want {
			t.Errorf("IsEventStream(%q, %q) = %v, want %v", c.ct, c.head, got, c.want)
		}
	}
}

func TestAcctCollectResponses(t *testing.T) {
	done := `data: {"type":"response.completed","response":{"id":"r1","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":1}}}`
	out, err := CollectResponses([]byte(done + "\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	if m["id"] != "r1" || m["status"] != "completed" {
		t.Errorf("completed response not returned: %s", out)
	}

	noDone := `data: {"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"hey"}]}}`
	out, err = CollectResponses([]byte(noDone + "\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	m = map[string]any{}
	json.Unmarshal(out, &m)
	if m["object"] != "response" || len(m["output"].([]any)) != 1 {
		t.Errorf("assembled response wrong: %s", out)
	}

	failed := `data: {"type":"response.failed","response":{"status":"failed"},"error":{"message":"quota gone"}}`
	if _, err := CollectResponses([]byte(failed + "\n\n")); err == nil || !strings.Contains(err.Error(), "quota gone") {
		t.Errorf("failure not reported: %v", err)
	}
}

func TestAcctCodexUpstreamHeadersAndSignedIn(t *testing.T) {
	home := acctEnv(t)
	acctWriteCodex(t, home, time.Now().Add(time.Hour), nil)

	CodexBase = "https://codex.example.test/backend-api/codex"
	t.Cleanup(func() { CodexBase = defaultCodexBase })

	var codex *Provider
	for _, p := range All() {
		if p.ID == CodexID {
			codex = &p
		}
		if p.ID == ClaudeID {
			t.Fatal("claude must never be listed")
		}
	}
	if codex == nil {
		t.Fatal("codex not listed while signed in")
	}
	if codex.Account == nil || codex.Account.User != "ada@example.com" || codex.Account.Plan != "plus" || codex.Account.Source != "Codex" {
		t.Errorf("account = %+v", codex.Account)
	}
	if !codex.Ready() {
		t.Error("account provider should be ready without a key")
	}

	base, headers, err := Upstream(context.Background(), *codex, Responses)
	if err != nil {
		t.Fatal(err)
	}
	if base != CodexBase {
		t.Errorf("base = %q, want %q", base, CodexBase)
	}
	if headers["chatgpt-account-id"] != "acct-123" || !strings.HasPrefix(headers["Authorization"], "Bearer ") {
		t.Errorf("headers = %v", headers)
	}
}

func TestAcctCodexRefreshWritesBack(t *testing.T) {
	home := acctEnv(t)
	acctWriteCodex(t, home, time.Now().Add(time.Minute), map[string]any{"keep_me": "yes"})

	var sawRefresh string
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		sawRefresh = body["refresh_token"] + "|" + body["grant_type"] + "|" + body["client_id"]
		json.NewEncoder(w).Encode(map[string]string{
			"access_token":  acctJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
			"refresh_token": "rt-new",
		})
	}))
	defer auth.Close()
	oldURL := codexRefreshURL
	codexRefreshURL = auth.URL
	t.Cleanup(func() { codexRefreshURL = oldURL })

	access, account, err := codexToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if account != "acct-123" || access == "" {
		t.Fatalf("token = %q, account = %q", access, account)
	}
	if sawRefresh != "rt-old|refresh_token|"+codexClientID {
		t.Errorf("refresh request = %q", sawRefresh)
	}
	b, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(b, &doc)
	tok := doc["tokens"].(map[string]any)
	if tok["refresh_token"] != "rt-new" || tok["access_token"] != access {
		t.Errorf("tokens not written back: %v", tok)
	}
	if tok["id_token"] == nil || tok["account_id"] != "acct-123" || doc["keep_me"] != "yes" || doc["auth_mode"] != "chatgpt" {
		t.Errorf("other fields lost: %s", b)
	}
}

func TestAcctCodexRefreshFailureSaysLoggedOut(t *testing.T) {
	home := acctEnv(t)
	acctWriteCodex(t, home, time.Now().Add(-time.Minute), nil)
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer auth.Close()
	oldURL := codexRefreshURL
	codexRefreshURL = auth.URL
	t.Cleanup(func() { codexRefreshURL = oldURL })

	_, _, err := codexToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "codex login") {
		t.Fatalf("want logged-out hint, got %v", err)
	}
}

func TestAcctSaveDeleteOnlyTouchModels(t *testing.T) {
	home := acctEnv(t)
	acctWriteCodex(t, home, time.Now().Add(time.Hour), nil)
	before, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))

	if err := Save(Provider{ID: CodexID, Name: "Codex", Key: "sk-should-not-store", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(Path())
	if strings.Contains(string(raw), "sk-should-not-store") || strings.Contains(string(raw), "chatgpt.com") {
		t.Errorf("config holds more than models: %s", raw)
	}
	if !strings.Contains(string(raw), "gpt-5.5") {
		t.Errorf("models not saved: %s", raw)
	}

	p, err := Find(CodexID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Models) != 1 || p.Models[0] != "gpt-5.5" || p.Key != "" {
		t.Errorf("provider = %+v", p)
	}

	if err := Delete(CodexID); err != nil {
		t.Fatal(err)
	}
	if _, err := Find(CodexID); err != nil {
		t.Errorf("signed-in account should stay listed after delete: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if string(before) != string(after) {
		t.Error("save/delete must not touch the agent's login file")
	}
}

func TestAcctSignedOutHidesProviderAndIgnoresPicks(t *testing.T) {
	home := acctEnv(t)
	acctWriteCodex(t, home, time.Now().Add(time.Hour), nil)
	if err := Save(Provider{ID: CodexID, Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	for _, p := range All() {
		if p.ID == CodexID {
			t.Fatal("codex listed after logout")
		}
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Fatalf("codex model %s offered after logout", e.ID)
		}
	}
}

func TestAcctAPIKeyCodexIsNotAnAccount(t *testing.T) {
	home := acctEnv(t)
	acctWriteCodex(t, home, time.Now().Add(time.Hour), nil)
	b, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	apiKey := strings.Replace(string(b), `"auth_mode":"chatgpt"`, `"auth_mode":"apikey"`, 1)
	apiKey = strings.Replace(apiKey, `"auth_mode": "chatgpt"`, `"auth_mode": "apikey"`, 1)
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(apiKey), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := AccountOf(CodexID); ok {
		t.Error("API-key Codex login must not be an account")
	}
}

func TestAcctCodexDefaultModelsAndClaudeHidden(t *testing.T) {
	home := acctEnv(t)
	acctWriteCodex(t, home, time.Now().Add(time.Hour), nil)
	if err := os.MkdirAll(filepath.Join(home, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "claude", ".credentials.json"), []byte(`{"secret":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !ClaudeSignedIn() {
		t.Error("claude login should be detected")
	}
	if err := Save(Provider{ID: ClaudeID, Chat: "https://example.com/v1", Key: "k"}); err == nil {
		t.Error("saving claude as a provider must fail")
	}
	found := false
	for _, m := range (Provider{ID: CodexID, Account: &Account{ID: CodexID}}).Available() {
		if m.ID == "gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Error("codex default list should include gpt-5.5")
	}
	for _, p := range All() {
		if p.ID == ClaudeID {
			t.Fatal("claude listed")
		}
	}
}

func TestAcctCopilotAccountAndBuiltinList(t *testing.T) {
	acctEnv(t)
	acctWriteCopilot(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_test" {
			http.Error(w, "bad", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"token":     "session-tok",
			"endpoints": map[string]string{"api": "https://api.example.test"},
		})
	}))
	defer srv.Close()
	oldURL := copilotTokenURL
	copilotTokenURL = srv.URL
	t.Cleanup(func() { copilotTokenURL = oldURL })

	var cp *Provider
	for _, p := range All() {
		if p.ID == CopilotID {
			cp = &p
		}
	}
	if cp == nil || cp.Account == nil || cp.Account.User != "octocat" {
		t.Fatalf("copilot not listed with its login: %+v", cp)
	}
	for _, m := range cp.Available() {
		if m.ID == "auto" || strings.Contains(m.ID, "embed") {
			t.Errorf("builtin copilot list must not offer %q", m.ID)
		}
	}
	if len(cp.Available()) == 0 {
		t.Error("builtin copilot list is empty")
	}

	base, headers, err := Upstream(context.Background(), *cp, Chat)
	if err != nil {
		t.Fatal(err)
	}
	if base != "https://api.example.test" || headers["Authorization"] != "Bearer session-tok" {
		t.Errorf("upstream = %q %v", base, headers)
	}
}

func TestAcctCopilotSessionFailure(t *testing.T) {
	acctEnv(t)
	acctWriteCopilot(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusForbidden)
	}))
	defer srv.Close()
	oldURL := copilotTokenURL
	copilotTokenURL = srv.URL
	t.Cleanup(func() { copilotTokenURL = oldURL })

	cp := accountProvider(CopilotID, Account{ID: CopilotID}, nil)
	_, _, err := Upstream(context.Background(), cp, Chat)
	if err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Fatalf("want re-login hint, got %v", err)
	}
}
