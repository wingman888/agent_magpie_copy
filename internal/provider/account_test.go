package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// ---- helpers ---------------------------------------------------------------

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// fakeJWT builds an unsigned JWT with the given payload claims.
func fakeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return b64(`{"alg":"none"}`) + "." + b64(string(b)) + ".sig"
}

// sandbox points HOME, the XDG dirs and CLAUDE_CONFIG_DIR at a temp dir, so
// no test reads or writes the real user's files.
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// pin overrides a package variable for the rest of the test.
func pin(t *testing.T, v *string, val string) {
	t.Helper()
	old := *v
	*v = val
	t.Cleanup(func() { *v = old })
}

// codexLoginFile writes a ChatGPT login for Codex and returns its access token.
func codexLoginFile(t *testing.T, home string, exp time.Time) string {
	t.Helper()
	access := fakeJWT(map[string]any{
		"exp":                       exp.Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
	})
	b, _ := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": "ana@example.com"}),
			"access_token":  access,
			"refresh_token": "rt-1",
			"account_id":    "acct-1",
		},
		"last_refresh": "2026-01-01T00:00:00Z",
		"custom":       "keep",
	})
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), string(b))
	return access
}

func copilotLoginFile(t *testing.T, home string) {
	t.Helper()
	writeFile(t, filepath.Join(home, "config", "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_test"}}`)
}

// findProvider returns the listed provider with the id, failing the test if absent.
func findProvider(t *testing.T, id string) Provider {
	t.Helper()
	for _, p := range All() {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("provider %q is not listed", id)
	return Provider{}
}

// ---- listing and login -----------------------------------------------------

func TestCodexLoginIsAProviderAndReady(t *testing.T) {
	home := sandbox(t)
	codexLoginFile(t, home, time.Now().Add(time.Hour))

	p := findProvider(t, "codex")
	if !p.Ready() {
		t.Error("a signed-in Codex account should be ready without a key")
	}
	if p.Key != "" {
		t.Error("an account provider must not carry a key")
	}
	info, ok := p.Account()
	if !ok || info.User != "ana@example.com" || info.Plan != "plus" || info.Source != "Codex" {
		t.Errorf("account = %+v, %v", info, ok)
	}
	found := false
	for _, e := range Catalog() {
		if e.ID == "codex/gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Error("codex/gpt-5.5 should be in the catalog without any setup")
	}
}

func TestCodexAPIKeyLoginIsNotAnAccount(t *testing.T) {
	home := sandbox(t)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test"}`)
	for _, p := range All() {
		if p.ID == "codex" {
			t.Fatal("an API-key Codex login must not be offered as an account")
		}
	}
}

func TestSignedOutAccountLeavesNoProviderButKeepsPicks(t *testing.T) {
	home := sandbox(t)
	writeFile(t, Path(), `{"providers":[{"id":"codex","models":["gpt-5.5"]}]}`)

	for _, p := range All() {
		if p.ID == "codex" {
			t.Fatal("a signed-out codex must not be listed")
		}
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Fatalf("signed-out codex model %q leaked into the catalog", e.ID)
		}
	}

	codexLoginFile(t, home, time.Now().Add(time.Hour))
	p := findProvider(t, "codex")
	if len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Errorf("picks after sign-in = %v, want [gpt-5.5]", p.Models)
	}
}

// ---- save and delete -------------------------------------------------------

func TestSaveAndDeleteOnlyTouchModelPicks(t *testing.T) {
	home := sandbox(t)
	copilotLoginFile(t, home)
	appsPath := filepath.Join(home, "config", "github-copilot", "apps.json")
	before, _ := os.ReadFile(appsPath)

	if err := Save(Provider{ID: "copilot", Name: "Copilot", Key: "secret-key", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(Path())
	for _, bad := range []string{"secret-key", `"name"`, `"key"`, "gho_test"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("config file contains %q: %s", bad, raw)
		}
	}
	if !strings.Contains(string(raw), "gpt-5.5") {
		t.Errorf("config file lost the picks: %s", raw)
	}
	if got := findProvider(t, "copilot").Models; len(got) != 1 || got[0] != "gpt-5.5" {
		t.Errorf("picks = %v, want [gpt-5.5]", got)
	}

	if err := Delete("copilot"); err != nil {
		t.Fatal(err)
	}
	if got := findProvider(t, "copilot").Models; len(got) != 0 {
		t.Errorf("after delete picks = %v, want none", got)
	}
	raw, _ = os.ReadFile(Path())
	if strings.Contains(string(raw), "gpt-5.5") {
		t.Errorf("config still holds the deleted pick: %s", raw)
	}

	after, _ := os.ReadFile(appsPath)
	if !bytes.Equal(before, after) {
		t.Error("save/delete must not touch the agent's login file")
	}
}

// ---- Claude Code -----------------------------------------------------------

func TestClaudeLoginIsNotExposed(t *testing.T) {
	sandbox(t)
	writeFile(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"SECRET-CLAUDE-TOKEN"}}`)

	ns := Notices()
	if len(ns) != 1 || ns[0].Agent != "claude" || !strings.Contains(ns[0].Note, "不提供") {
		t.Fatalf("notices = %+v, want one claude notice saying it is not offered", ns)
	}
	all, _ := json.Marshal(All())
	if strings.Contains(string(all), "SECRET") {
		t.Error("the provider list leaks Claude's token")
	}
	for _, p := range All() {
		if p.ID == "claude" {
			t.Error("claude must never be a provider")
		}
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "claude/") {
			t.Errorf("claude model %q in the catalog", e.ID)
		}
	}
	if _, err := Find("claude"); err == nil {
		t.Error("Find(claude) should fail")
	}
}

func TestNoNoticeWhenClaudeIsSignedOut(t *testing.T) {
	sandbox(t)
	if ns := Notices(); len(ns) != 0 {
		t.Errorf("notices = %+v, want none", ns)
	}
}

// ---- Codex request and response --------------------------------------------

func TestCodexBodyDropsSamplingAndStreams(t *testing.T) {
	out, err := CodexBody([]byte(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":64,"temperature":0.2,"top_p":0.9,"store":true,"stream":false,"instructions":"be brief"}`))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "gpt-5.5" {
		t.Errorf("model = %v", m["model"])
	}
	if m["stream"] != true || m["store"] != false {
		t.Errorf("stream/store = %v/%v", m["stream"], m["store"])
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s must not reach the Codex backend", k)
		}
	}
	if m["instructions"] != "be brief" {
		t.Errorf("instructions = %v", m["instructions"])
	}
	items, ok := m["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input = %v, want a one-item list", m["input"])
	}
	item := items[0].(map[string]any)
	content := item["content"].([]any)[0].(map[string]any)
	if item["role"] != "user" || content["type"] != "input_text" || content["text"] != "hi" {
		t.Errorf("input item = %v", item)
	}
}

const codexSSE = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}]}}\n\n"

type codexSeen struct {
	head http.Header
	raw  string
	body map[string]any
}

// fakeCodex is a stand-in for the ChatGPT backend. ctype "" sends no Content-Type.
func fakeCodex(t *testing.T, ctype string, seen *codexSeen) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		seen.head = r.Header.Clone()
		seen.raw = string(b)
		json.Unmarshal(b, &seen.body)
		if ctype == "" {
			w.Header()["Content-Type"] = nil
		} else {
			w.Header().Set("Content-Type", ctype)
		}
		io.WriteString(w, codexSSE)
	}))
}

func TestCodexNonStreamIsGatheredFromTheStream(t *testing.T) {
	home := sandbox(t)
	access := codexLoginFile(t, home, time.Now().Add(time.Hour))
	seen := &codexSeen{}
	srv := fakeCodex(t, "", seen)
	defer srv.Close()
	pin(t, &CodexBase, srv.URL)

	p, err := Prepare(context.Background(), findProvider(t, "codex"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := CodexComplete(context.Background(), p, []byte(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":64,"temperature":0.2}`))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("not JSON: %s", out)
	}
	if resp.ID != "resp_1" || resp.Status != "completed" || resp.Output[0].Content[0].Text != "hi" {
		t.Errorf("gathered response = %s", out)
	}

	if got := seen.head.Get("Authorization"); got != "Bearer "+access {
		t.Errorf("Authorization = %q", got)
	}
	if got := seen.head.Get("chatgpt-account-id"); got != "acct-1" {
		t.Errorf("chatgpt-account-id = %q", got)
	}
	if seen.body["model"] != "gpt-5.5" || seen.body["stream"] != true || seen.body["store"] != false {
		t.Errorf("upstream body = %s", seen.raw)
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := seen.body[k]; ok {
			t.Errorf("%s reached upstream: %s", k, seen.raw)
		}
	}
}

func TestCodexStreamIsForwardedAsIs(t *testing.T) {
	home := sandbox(t)
	codexLoginFile(t, home, time.Now().Add(time.Hour))
	seen := &codexSeen{}
	srv := fakeCodex(t, "text/event-stream", seen)
	defer srv.Close()
	pin(t, &CodexBase, srv.URL)

	p, err := Prepare(context.Background(), findProvider(t, "codex"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := CodexCall(context.Background(), p, []byte(`{"model":"codex/gpt-5.5","input":[],"stream":true,"max_output_tokens":8}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	got, _ := io.ReadAll(res.Body)
	if string(got) != codexSSE {
		t.Errorf("forwarded body differs from upstream: %q", got)
	}
	if seen.body["stream"] != true {
		t.Errorf("upstream stream = %v", seen.body["stream"])
	}
	if _, ok := seen.body["max_output_tokens"]; ok {
		t.Errorf("max_output_tokens reached upstream: %s", seen.raw)
	}
}

func TestIsEventStreamSniffsWhenNoContentType(t *testing.T) {
	cases := []struct {
		ct   string
		body string
		want bool
	}{
		{"", "event: response.created\n", true},
		{"", "\n data: {}", true},
		{"", `{"id":"x"}`, false},
		{"text/event-stream; charset=utf-8", "", true},
		{"application/json", "data: x", false},
	}
	for _, c := range cases {
		if got := IsEventStream(c.ct, []byte(c.body)); got != c.want {
			t.Errorf("IsEventStream(%q, %q) = %v, want %v", c.ct, c.body, got, c.want)
		}
	}
}

func TestCodexRefreshWritesBackAndKeepsOtherFields(t *testing.T) {
	home := sandbox(t)
	codexLoginFile(t, home, time.Now().Add(time.Minute)) // inside the five-minute window
	newAccess := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})

	var form map[string]any
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &form)
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  newAccess,
			"id_token":      fakeJWT(map[string]any{"email": "ana@example.com"}),
			"refresh_token": "rt-2",
		})
	}))
	defer auth.Close()
	pin(t, &CodexAuthBase, auth.URL)
	pin(t, &CodexBase, "http://unused.invalid")

	p, err := Prepare(context.Background(), findProvider(t, "codex"))
	if err != nil {
		t.Fatal(err)
	}
	if form["grant_type"] != "refresh_token" || form["refresh_token"] != "rt-1" {
		t.Errorf("refresh request = %v", form)
	}
	if got := AuthHeaders(p, Responses)["Authorization"]; got != "Bearer "+newAccess {
		t.Errorf("used token %q, want the refreshed one", got)
	}

	b, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	var top map[string]any
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	tokens, _ := top["tokens"].(map[string]any)
	if tokens["access_token"] != newAccess || tokens["refresh_token"] != "rt-2" {
		t.Errorf("tokens not written back: %v", tokens)
	}
	if tokens["account_id"] != "acct-1" || tokens["id_token"] == nil {
		t.Errorf("token fields lost: %v", tokens)
	}
	if top["custom"] != "keep" {
		t.Errorf("unrelated field lost: %v", top)
	}
	if _, ok := top["OPENAI_API_KEY"]; !ok {
		t.Errorf("OPENAI_API_KEY key lost: %v", top)
	}
}

func TestCodexRefreshFailureSaysLoggedOut(t *testing.T) {
	home := sandbox(t)
	codexLoginFile(t, home, time.Now().Add(time.Minute))
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusUnauthorized)
	}))
	defer auth.Close()
	pin(t, &CodexAuthBase, auth.URL)

	_, err := Prepare(context.Background(), findProvider(t, "codex"))
	if err == nil || !strings.Contains(err.Error(), "codex login") {
		t.Errorf("err = %v, want a logged-out hint naming `codex login`", err)
	}
}

// ---- Copilot ---------------------------------------------------------------

const copilotModelsJSON = `{"data":[
 {"id":"gpt-5.5","name":"GPT-5.5","model_picker_enabled":true,"capabilities":{"type":"chat"},"policy":{"state":"enabled"}},
 {"id":"auto","name":"Auto","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"text-embedding-3-small","name":"Embed","model_picker_enabled":true,"capabilities":{"type":"embeddings"}},
 {"id":"internal-router","name":"Router","model_picker_enabled":false,"capabilities":{"type":"chat"}},
 {"id":"claude-off","name":"Off","model_picker_enabled":true,"capabilities":{"type":"chat"},"policy":{"state":"disabled"}},
 {"id":"claude-sonnet-4.5","model_picker_enabled":true,"capabilities":{"type":"chat"}}
]}`

// fakeCopilot serves the GitHub token exchange and the Copilot model list.
// tokenStatus other than 200 makes the exchange fail with that status.
func fakeCopilot(t *testing.T, tokenStatus int) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if tokenStatus != http.StatusOK {
			w.WriteHeader(tokenStatus)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"token":     "sess-1",
			"endpoints": map[string]any{"api": srv.URL + "/api"},
		})
	})
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, copilotModelsJSON)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCopilotBuiltinListHasNoAuto(t *testing.T) {
	home := sandbox(t)
	copilotLoginFile(t, home)
	p := findProvider(t, "copilot")
	avail := p.Available()
	if len(avail) == 0 {
		t.Fatal("the built-in Copilot list must not be empty")
	}
	for _, m := range avail {
		if m.ID == "auto" {
			t.Error("auto must not be in the built-in Copilot list")
		}
	}
}

func TestCopilotRefreshKeepsOnlyUsableChatModels(t *testing.T) {
	home := sandbox(t)
	copilotLoginFile(t, home)
	srv := fakeCopilot(t, http.StatusOK)
	pin(t, &GitHubAPI, srv.URL)

	p := findProvider(t, "copilot")
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gpt-5.5", "claude-sonnet-4.5"}
	if got := modelIDs(ms); !equalStrings(got, want) {
		t.Errorf("refreshed list = %v, want %v", got, want)
	}
	if got := modelIDs(findProvider(t, "copilot").Available()); !equalStrings(got, want) {
		t.Errorf("after refresh the listed models = %v, want %v", got, want)
	}

	p2, err := Prepare(context.Background(), findProvider(t, "copilot"))
	if err != nil {
		t.Fatal(err)
	}
	if p2.Chat != srv.URL+"/api" {
		t.Errorf("chat base = %q, want the session's API address", p2.Chat)
	}
	if got := AuthHeaders(p2, Chat)["Authorization"]; got != "Bearer sess-1" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestCopilotExpiredSessionAsksForRelogin(t *testing.T) {
	home := sandbox(t)
	copilotLoginFile(t, home)
	srv := fakeCopilot(t, http.StatusUnauthorized)
	pin(t, &GitHubAPI, srv.URL)

	p := findProvider(t, "copilot")
	if _, err := p.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Errorf("Fetch err = %v, want a relogin hint", err)
	}
	if _, err := Prepare(context.Background(), p); err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Errorf("Prepare err = %v, want a relogin hint", err)
	}
}

func TestCopilotTurnAndVisionHeaders(t *testing.T) {
	cases := []struct {
		name, body, initiator, vision string
	}{
		{"user turn", `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`, "user", ""},
		{"tool result turn", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[]},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`, "agent", ""},
		{"responses tool output", `{"input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`, "agent", ""},
		{"vision", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`, "user", "true"},
	}
	for _, c := range cases {
		h := copilotTurnHeaders([]byte(c.body))
		if h["X-Initiator"] != c.initiator {
			t.Errorf("%s: X-Initiator = %q, want %q", c.name, h["X-Initiator"], c.initiator)
		}
		if h["Copilot-Vision-Request"] != c.vision {
			t.Errorf("%s: Copilot-Vision-Request = %q, want %q", c.name, h["Copilot-Vision-Request"], c.vision)
		}
	}
}

// ---- small helpers for the assertions above --------------------------------

func modelIDs(ms []catalog.Model) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
