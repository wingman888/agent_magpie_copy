package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sandbox points every path dial and the agents read at a fresh temp dir and
// forgets any Copilot session kept from an earlier test.
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	copilotCache.mu.Lock()
	copilotCache.oauth, copilotCache.token, copilotCache.api, copilotCache.exp = "", "", "", time.Time{}
	copilotCache.mu.Unlock()
	return home
}

func fakeJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func freshToken() string {
	return fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func codexAuthFile(home string) string { return filepath.Join(home, ".codex", "auth.json") }

// signInCodex writes a ChatGPT login the way codex login does.
func signInCodex(t *testing.T, home, access string) {
	t.Helper()
	id := fakeJWT(map[string]any{"email": "dev@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
	writeJSONFile(t, codexAuthFile(home), map[string]any{
		"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "last_refresh": "2026-01-01T00:00:00Z", "extra": "keep",
		"tokens": map[string]any{"id_token": id, "access_token": access, "refresh_token": "rt-old", "account_id": "acct-1"},
	})
}

func signInCopilot(t *testing.T) {
	t.Helper()
	writeJSONFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json"),
		map[string]any{"github.com:Iv1.abc": map[string]any{"user": "octo", "oauth_token": "gho_test"}})
}

func mustFind(t *testing.T, ps []Provider, id string) Provider {
	t.Helper()
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no provider %q in %v", id, ps)
	return Provider{}
}

func catalogHas(id string) bool {
	for _, e := range Catalog() {
		if e.ID == id {
			return true
		}
	}
	return false
}

func TestCodexSignedInIsServed(t *testing.T) {
	home := sandbox(t)
	signInCodex(t, home, freshToken())
	p := mustFind(t, All(), "codex")
	if p.User != "dev@example.com" || p.Plan != "plus" || p.Source != "Codex" {
		t.Fatalf("account = %+v", p)
	}
	if !p.Ready() {
		t.Error("a signed-in account needs no key")
	}
	if p.Key != "" {
		t.Error("the listed provider must not carry a token")
	}
	if p.Host() != "chatgpt.com" {
		t.Errorf("host = %q", p.Host())
	}
	if !catalogHas("codex/gpt-5.5") {
		t.Error("codex/gpt-5.5 should be in the catalog without any pick or refresh")
	}
}

func TestCodexAPIKeyLoginIsNotAnAccount(t *testing.T) {
	home := sandbox(t)
	writeJSONFile(t, codexAuthFile(home), map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "sk-x"})
	for _, p := range All() {
		if p.ID == "codex" {
			t.Fatal("an API-key login must not be served as an account")
		}
	}
}

func TestLoggedOutAccountDisappearsButPicksStay(t *testing.T) {
	home := sandbox(t)
	signInCodex(t, home, freshToken())
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(codexAuthFile(home)); err != nil {
		t.Fatal(err)
	}
	for _, p := range All() {
		if p.ID == "codex" {
			t.Fatal("a logged-out agent must not be a provider")
		}
	}
	if catalogHas("codex/gpt-5.5") {
		t.Fatal("a residual pick must not reach the catalog")
	}
	if len(load().Providers) != 1 {
		t.Fatal("the pick record should remain in the file")
	}
}

func TestSaveAccountWritesOnlyModels(t *testing.T) {
	home := sandbox(t)
	signInCodex(t, home, freshToken())
	err := Save(Provider{ID: "codex", Name: "Mine", Key: "sk-secret", Chat: "https://x.example/v1", Models: []string{"gpt-5.5"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, bad := range []string{"sk-secret", "Mine", "x.example", `"key"`, `"name"`, "dev@example.com", "rt-old"} {
		if strings.Contains(s, bad) {
			t.Errorf("provider file contains %q: %s", bad, s)
		}
	}
	if !strings.Contains(s, "gpt-5.5") {
		t.Errorf("the pick was not written: %s", s)
	}
	if got := mustFind(t, All(), "codex").Models; len(got) != 1 || got[0] != "gpt-5.5" {
		t.Errorf("models = %v", got)
	}
}

func TestDeleteAccountOnlyClearsPicks(t *testing.T) {
	home := sandbox(t)
	signInCodex(t, home, freshToken())
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t, All(), "codex")
	if len(p.Models) != 0 {
		t.Errorf("picks survived delete: %v", p.Models)
	}
	if _, err := os.Stat(codexAuthFile(home)); err != nil {
		t.Errorf("delete touched the sign-in: %v", err)
	}
}

func TestDeleteUnknownAccountFails(t *testing.T) {
	sandbox(t)
	if err := Delete("copilot"); err == nil {
		t.Error("deleting a copilot that is neither recorded nor signed in should fail")
	}
}

func TestClaudeIsNeverServed(t *testing.T) {
	sandbox(t)
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	writeJSONFile(t, filepath.Join(dir, ".credentials.json"),
		map[string]any{"claudeAiOauth": map[string]any{"accessToken": "SECRET-CLAUDE-TOKEN"}})
	// a file entry named claude must not surface either
	writeJSONFile(t, Path(), map[string]any{"providers": []any{map[string]any{"id": "claude", "models": []string{"m"}}}})
	for _, p := range All() {
		if p.ID == "claude" {
			t.Fatal("claude listed as a provider")
		}
	}
	for _, e := range Catalog() {
		if e.Provider.ID == "claude" {
			t.Fatal("claude in the catalog")
		}
	}
	ns := Notices()
	if len(ns) != 1 || ns[0].Agent != "Claude Code" || !strings.Contains(ns[0].Text, "已登录") {
		t.Fatalf("notices = %+v", ns)
	}
	if strings.Contains(ns[0].Text, "SECRET") {
		t.Error("the notice leaks a credential")
	}
	if err := Save(Provider{ID: "claude", Anthropic: "https://x.example", Key: "k"}); err == nil {
		t.Error("saving a claude provider should fail")
	}
}

func TestNoClaudeNoticeWithoutSignIn(t *testing.T) {
	sandbox(t)
	if ns := Notices(); len(ns) != 0 {
		t.Fatalf("notices without a Claude sign-in: %+v", ns)
	}
}

func TestCodexBodyRewrite(t *testing.T) {
	in := `{"model":"codex/gpt-5.5","input":"hi","temperature":0.2,"max_output_tokens":10,"store":true,"stream":false,"top_k":3}`
	var m map[string]any
	if err := json.Unmarshal(CodexBody([]byte(in)), &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "gpt-5.5" {
		t.Errorf("model = %v", m["model"])
	}
	if m["stream"] != true || m["store"] != false {
		t.Errorf("stream/store = %v/%v", m["stream"], m["store"])
	}
	for _, k := range []string{"temperature", "max_output_tokens"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s was not removed", k)
		}
	}
	items, ok := m["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input = %#v", m["input"])
	}
	item := items[0].(map[string]any)
	content := item["content"].([]any)[0].(map[string]any)
	if item["role"] != "user" || content["text"] != "hi" {
		t.Errorf("input item = %v", item)
	}
}

func TestCodexFinalReturnsCompletedObject(t *testing.T) {
	ok := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\"}}\n\n"
	b, err := CodexFinal(strings.NewReader(ok))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if json.Unmarshal(b, &v) != nil || v["id"] != "r1" || v["status"] != "completed" {
		t.Fatalf("final = %s", b)
	}
	_, err = CodexFinal(strings.NewReader("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"boom\"}}}\n\n"))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("failure err = %v", err)
	}
	if _, err := CodexFinal(strings.NewReader("")); err == nil {
		t.Fatal("an empty reply should be an error")
	}
}

func TestFixEventStreamWithoutContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Content-Type"] = nil // no type at all, not even a sniffed one
		io.WriteString(w, "event: response.created\ndata: {}\n\n")
	}))
	defer srv.Close()
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	FixEventStream(res)
	if got := res.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q", got)
	}
	b, _ := io.ReadAll(res.Body)
	if !strings.HasPrefix(string(b), "event: response.created") {
		t.Fatalf("body = %q", b)
	}
}

func TestCodexDefaultListHasGpt55(t *testing.T) {
	home := sandbox(t)
	signInCodex(t, home, freshToken())
	if !hasModel(mustFind(t, All(), "codex").Available(), "gpt-5.5") {
		t.Fatal("the default Codex list must include gpt-5.5")
	}
}

func TestCodexRefreshWritesBack(t *testing.T) {
	home := sandbox(t)
	signInCodex(t, home, fakeJWT(map[string]any{"exp": time.Now().Add(time.Minute).Unix()})) // inside the 5 minute window
	newAccess := freshToken()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"rt-new"}`)
	}))
	defer srv.Close()
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	defer func() { CodexRefreshURL = old }()

	s, err := Session(context.Background(), mustFind(t, All(), "codex"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Key != newAccess || s.Responses != CodexBase || s.Headers["chatgpt-account-id"] != "acct-1" {
		t.Fatalf("session = key %q base %q headers %v", s.Key, s.Responses, s.Headers)
	}
	if got["refresh_token"] != "rt-old" || got["grant_type"] != "refresh_token" {
		t.Errorf("refresh request = %v", got)
	}
	var doc map[string]any
	b, _ := os.ReadFile(codexAuthFile(home))
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	tok := doc["tokens"].(map[string]any)
	if tok["access_token"] != newAccess || tok["refresh_token"] != "rt-new" || tok["account_id"] != "acct-1" {
		t.Errorf("tokens written back = %v", tok)
	}
	if doc["extra"] != "keep" || doc["auth_mode"] != "chatgpt" {
		t.Errorf("other fields lost: %v", doc)
	}
	if _, ok := doc["OPENAI_API_KEY"]; !ok {
		t.Error("OPENAI_API_KEY field dropped")
	}
}

func TestCodexRefreshFailureSaysRelogin(t *testing.T) {
	home := sandbox(t)
	signInCodex(t, home, fakeJWT(map[string]any{"exp": time.Now().Add(time.Minute).Unix()}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	defer func() { CodexRefreshURL = old }()

	_, err := Session(context.Background(), mustFind(t, All(), "codex"))
	if err == nil || !strings.Contains(err.Error(), "已登出，请运行 `codex login`") {
		t.Fatalf("err = %v", err)
	}
}

// copilotFake serves a Copilot session endpoint and a model list.
func copilotFake(t *testing.T, models string) (api *httptest.Server, tokenURL string) {
	t.Helper()
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer sess-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, models)
	}))
	t.Cleanup(api.Close)
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"token":"sess-1","expires_at":`+strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)+
			`,"endpoints":{"api":"`+api.URL+`"}}`)
	}))
	t.Cleanup(tok.Close)
	old := CopilotTokenURL
	CopilotTokenURL = tok.URL
	t.Cleanup(func() { CopilotTokenURL = old })
	return api, tok.URL
}

func TestCopilotListFiltersAndSavesLive(t *testing.T) {
	sandbox(t)
	signInCopilot(t)
	copilotFake(t, `{"data":[
		{"id":"auto","name":"Auto","model_picker_enabled":true,"capabilities":{"type":"chat"}},
		{"id":"gpt-5.5","name":"GPT-5.5","model_picker_enabled":true,"capabilities":{"type":"chat"},"policy":{"state":"enabled"}},
		{"id":"internal-x","name":"Internal","model_picker_enabled":false,"capabilities":{"type":"chat"}},
		{"id":"off-model","name":"Off","model_picker_enabled":true,"capabilities":{"type":"chat"},"policy":{"state":"disabled"}},
		{"id":"text-embedding-3","name":"Embed","model_picker_enabled":true,"capabilities":{"type":"embeddings"}}
	]}`)
	p := mustFind(t, All(), "copilot")
	if p.User != "octo" || p.Source != "Copilot" {
		t.Fatalf("account = %+v", p)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != "gpt-5.5" {
		t.Fatalf("fetched = %+v", ms)
	}
	if _, ok := mustFind(t, All(), "copilot").Fetched(); !ok {
		t.Error("the live list was not saved")
	}
	av := mustFind(t, All(), "copilot").Available()
	if len(av) != 1 || av[0].ID != "gpt-5.5" {
		t.Errorf("available after refresh = %+v", av)
	}
}

func TestCopilotBuiltinListHasNoAuto(t *testing.T) {
	sandbox(t)
	signInCopilot(t)
	av := mustFind(t, All(), "copilot").Available()
	if len(av) == 0 {
		t.Fatal("the built-in Copilot list is empty")
	}
	for _, m := range av {
		if m.ID == "auto" {
			t.Fatal("auto in the built-in Copilot list")
		}
	}
	if !catalogHas("copilot/gpt-5.5") {
		t.Error("copilot models should be in /v1/models before any refresh")
	}
}

func TestCopilotSessionFailureSaysRelogin(t *testing.T) {
	sandbox(t)
	signInCopilot(t)
	copilotFake(t, `{"data":[]}`)
	// the token endpoint rejects this token
	writeJSONFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json"),
		map[string]any{"github.com:Iv1.abc": map[string]any{"user": "octo", "oauth_token": "gho_wrong"}})
	_, err := Session(context.Background(), mustFind(t, All(), "copilot"))
	if err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Fatalf("err = %v", err)
	}
}

func TestCopilotHeadersMarkTurnsAndVision(t *testing.T) {
	h := CopilotHeaders([]byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"x"},{"role":"tool","content":"r"}]}`))
	if h["X-Initiator"] != "agent" {
		t.Errorf("tool turn X-Initiator = %q", h["X-Initiator"])
	}
	if _, ok := h["Copilot-Vision-Request"]; ok {
		t.Error("text-only request marked as vision")
	}
	h = CopilotHeaders([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`))
	if h["X-Initiator"] != "user" || h["Copilot-Vision-Request"] != "true" {
		t.Errorf("headers = %v", h)
	}
}
