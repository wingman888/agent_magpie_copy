package provider

// Black-box checks of the account-provider criteria, written against the
// exported surface (Find, All, Catalog, Save, Delete, Route, Fetch) with a
// temp HOME, temp config and fake Codex / Copilot servers.

import (
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
)

func bb_env(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

// bb_setVar swaps a package-level string for the test's lifetime.
func bb_setVar(t *testing.T, dst *string, v string) {
	t.Helper()
	old := *dst
	*dst = v
	t.Cleanup(func() { *dst = old })
}

func bb_writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func bb_jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// bb_codexLogin writes a ChatGPT login to ~/.codex/auth.json and returns its access token.
func bb_codexLogin(t *testing.T, home string, exp time.Time) string {
	t.Helper()
	access := bb_jwt(map[string]any{"exp": exp.Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
	id := bb_jwt(map[string]any{"email": "ada@example.com"})
	bb_writeJSON(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "other": "keep me",
		"tokens": map[string]any{"id_token": id, "access_token": access, "refresh_token": "rt-old", "account_id": "acct-1"},
	})
	return access
}

func bb_findIn(ps []Provider, id string) (Provider, bool) {
	for _, p := range ps {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// ---- acceptance 6: Codex login is offered with the email, routes, and renews ----

func TestBB_CodexSignedInIsOfferedAsAccount(t *testing.T) {
	home := bb_env(t)
	access := bb_codexLogin(t, home, time.Now().Add(time.Hour))
	ps := All()
	p, ok := bb_findIn(ps, "codex")
	if !ok {
		t.Fatalf("codex not in All(): %+v", ps)
	}
	if p.Login != "ada@example.com" || !p.Ready() || p.Agent != "codex" {
		t.Errorf("codex provider = %+v", p)
	}
	found := false
	for _, e := range Catalog() {
		if e.ID == "codex/gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Errorf("codex/gpt-5.5 missing from Catalog()")
	}

	bb_setVar(t, &CodexBase, "https://codex.test/backend-api/codex")
	up, err := Route(context.Background(), p, Responses, "/responses",
		[]byte(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":5,"temperature":1,"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if up.URL != "https://codex.test/backend-api/codex/responses" {
		t.Errorf("URL = %s", up.URL)
	}
	if up.Header["Authorization"] != "Bearer "+access || up.Header["chatgpt-account-id"] != "acct-1" {
		t.Errorf("headers = %v", up.Header)
	}
	var body map[string]any
	if err := json.Unmarshal(up.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["max_output_tokens"]; ok {
		t.Error("max_output_tokens was sent")
	}
	if _, ok := body["temperature"]; ok {
		t.Error("temperature was sent")
	}
	if body["stream"] != true || body["store"] != false || body["model"] != "gpt-5.5" {
		t.Errorf("stream/store/model = %v/%v/%v", body["stream"], body["store"], body["model"])
	}
	if _, ok := body["input"].([]any); !ok {
		t.Errorf("string input not turned into a list: %v", body["input"])
	}
}

func TestBB_CodexRenewalKeepsOtherFieldsAndSaysLoggedOut(t *testing.T) {
	home := bb_env(t)
	bb_codexLogin(t, home, time.Now().Add(time.Minute)) // inside the renewal window
	authPath := filepath.Join(home, ".codex", "auth.json")
	newAccess := bb_jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "rt-old") {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": newAccess, "refresh_token": "rt-new"})
	}))
	defer srv.Close()
	bb_setVar(t, &codexRefreshURL, srv.URL)
	bb_setVar(t, &CodexBase, "https://codex.test/backend-api/codex")

	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	up, err := Route(context.Background(), *p, Responses, "/responses", []byte(`{"model":"codex/gpt-5.5","input":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if up.Header["Authorization"] != "Bearer "+newAccess {
		t.Errorf("renewed token not used: %v", up.Header)
	}
	raw, _ := os.ReadFile(authPath)
	if !strings.Contains(string(raw), "rt-new") || !strings.Contains(string(raw), "keep me") {
		t.Errorf("renewed auth.json = %s", raw)
	}

	// a refused renewal says the user is logged out and how to sign in again
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer refused.Close()
	bb_setVar(t, &codexRefreshURL, refused.URL)
	bb_codexLogin(t, home, time.Now().Add(time.Minute))
	_, err = Route(context.Background(), *p, Responses, "/responses", []byte(`{"input":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "已登出") || !strings.Contains(err.Error(), "codex login") {
		t.Errorf("refused renewal error = %v", err)
	}
}

func TestBB_CodexAPIKeyLoginIsNotAnAccount(t *testing.T) {
	home := bb_env(t)
	bb_writeJSON(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": "sk-x"})
	if _, ok := bb_findIn(All(), "codex"); ok {
		t.Error("API-key Codex login offered as an account")
	}
}

// ---- acceptance 8: Copilot list is filtered to usable chat models ---------------

const bb_copilotModels = `{"data":[
 {"id":"gpt-5.5","name":"GPT-5.5","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"auto","name":"Auto","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"internal-router","name":"Internal Router","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"text-embedding-3-small","name":"Embeddings","model_picker_enabled":true,"capabilities":{"type":"embeddings"}},
 {"id":"off-model","name":"Off","model_picker_enabled":false,"capabilities":{"type":"chat"}},
 {"id":"policy-model","name":"Policy","model_picker_enabled":true,"policy":{"state":"disabled"},"capabilities":{"type":"chat"}},
 {"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","model_picker_enabled":true,"capabilities":{"type":"chat"}}
]}`

// bb_copilotServer answers the token exchange (status tokenStatus) and the model list.
func bb_copilotServer(t *testing.T, tokenStatus int) string {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if tokenStatus != http.StatusOK {
			w.WriteHeader(tokenStatus)
			return
		}
		if r.Header.Get("Authorization") != "token gho_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "sess-tok", "endpoints": map[string]any{"api": srv.URL + "/api"}})
	})
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, bb_copilotModels)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	bb_setVar(t, &copilotTokenURL, srv.URL+"/token")
	return srv.URL + "/api"
}

func bb_copilotLogin(t *testing.T) {
	t.Helper()
	bb_writeJSON(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json"),
		map[string]any{"github.com:Iv1.abc": map[string]any{"user": "octo", "oauth_token": "gho_test"}})
}

func bb_idsOf(p Provider) []string {
	var out []string
	for _, m := range p.Available() {
		out = append(out, m.ID)
	}
	return out
}

func TestBB_CopilotSignedInListsOnlyUsableChatModels(t *testing.T) {
	bb_env(t)
	bb_copilotLogin(t)
	api := bb_copilotServer(t, http.StatusOK)

	p, err := Find("copilot")
	if err != nil {
		t.Fatalf("copilot not offered: %v", err)
	}
	if p.Login != "octo" {
		t.Errorf("login = %q", p.Login)
	}
	for _, id := range bb_idsOf(*p) {
		if id == "auto" {
			t.Error("built-in Copilot list contains auto")
		}
	}
	if len(bb_idsOf(*p)) == 0 {
		t.Error("built-in Copilot list is empty")
	}

	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	fresh, err := Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(bb_idsOf(*fresh), ",")
	if got != "gpt-5.5,claude-sonnet-4.5" {
		t.Errorf("after refresh = %s", got)
	}

	up, err := Route(context.Background(), *fresh, Chat, "/chat/completions",
		[]byte(`{"model":"copilot/gpt-5.5","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if up.URL != api+"/chat/completions" {
		t.Errorf("URL = %s", up.URL)
	}
	if up.Header["Authorization"] != "Bearer sess-tok" || up.Header["Copilot-Vision-Request"] != "true" || up.Header["X-Initiator"] != "user" {
		t.Errorf("headers = %v", up.Header)
	}
	var body map[string]any
	json.Unmarshal(up.Body, &body)
	if body["model"] != "gpt-5.5" {
		t.Errorf("model sent = %v", body["model"])
	}

	// a tool's reply sent back is billed as the agent's turn
	up, err = Route(context.Background(), *fresh, Chat, "/chat/completions",
		[]byte(`{"model":"copilot/gpt-5.5","messages":[{"role":"user","content":"hi"},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if up.Header["X-Initiator"] != "agent" {
		t.Errorf("tool reply X-Initiator = %q, want agent", up.Header["X-Initiator"])
	}
}

func TestBB_CopilotExpiredSessionSaysSignInAgain(t *testing.T) {
	bb_env(t)
	bb_copilotLogin(t)
	bb_copilotServer(t, http.StatusUnauthorized)
	p, err := Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Route(context.Background(), *p, Chat, "/chat/completions", []byte(`{"messages":[]}`)); err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Errorf("expired session error = %v", err)
	}
}

// ---- acceptance 9: saving and deleting only touch model picks ------------------

func TestBB_SaveAndDeleteOnlyTouchPicksAndLogoutHides(t *testing.T) {
	home := bb_env(t)
	access := bb_codexLogin(t, home, time.Now().Add(time.Hour))
	authPath := filepath.Join(home, ".codex", "auth.json")
	before, _ := os.ReadFile(authPath)

	if err := Save(Provider{ID: "codex", Name: "Codex", Key: "sk-should-not-be-stored", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{access, "sk-should-not-be-stored", "ada@example.com", "rt-old", "acct-1"} {
		if strings.Contains(string(cfg), leak) {
			t.Errorf("config file holds %q", leak)
		}
	}
	if p, _ := bb_findIn(All(), "codex"); len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Errorf("picks after save = %v", p.Models)
	}

	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p, ok := bb_findIn(All(), "codex")
	if !ok || len(p.Models) != 0 {
		t.Errorf("after delete: present=%v picks=%v; the login must still show it", ok, p.Models)
	}
	after, _ := os.ReadFile(authPath)
	if string(after) != string(before) {
		t.Error("the Codex login file was changed by save/delete")
	}

	// the agent signs out: the provider disappears, and so do its models
	if err := os.Remove(authPath); err != nil {
		t.Fatal(err)
	}
	if _, ok := bb_findIn(All(), "codex"); ok {
		t.Error("codex still offered after sign-out")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Errorf("catalog still lists %s after sign-out", e.ID)
		}
	}
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err == nil {
		t.Error("saving picks for a signed-out account should fail")
	}
}

// ---- acceptance 10: Claude Code is detected, never offered ----------------------

func TestBB_ClaudeLoginDetectedNeverOffered(t *testing.T) {
	bb_env(t)
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	bb_writeJSON(t, filepath.Join(dir, ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{"accessToken": "SECRET-TOKEN"}})

	us := UnavailableAgents()
	if len(us) != 1 || us[0].Name != "Claude Code" || !strings.Contains(us[0].Reason, "已登录但不提供") {
		t.Fatalf("UnavailableAgents() = %+v", us)
	}
	if _, ok := bb_findIn(All(), "claude"); ok {
		t.Error("claude offered by All()")
	}
	if _, err := Find("claude"); err == nil {
		t.Error("Find(claude) succeeded")
	}
	if err := Save(Provider{ID: "claude", Name: "Claude", Models: []string{"x"}}); err == nil {
		t.Error("Save(claude) succeeded")
	}
	// a stale config record must not come back as a provider either
	bb_writeJSON(t, Path(), map[string]any{"providers": []any{map[string]any{"id": "claude", "models": []string{"x"}}}})
	if _, ok := bb_findIn(All(), "claude"); ok {
		t.Error("stale claude record offered by All()")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "claude/") {
			t.Errorf("claude model in catalog: %s", e.ID)
		}
	}
}

func TestBB_NoClaudeNoNote(t *testing.T) {
	bb_env(t)
	if us := UnavailableAgents(); len(us) != 0 {
		t.Errorf("UnavailableAgents() without a Claude login = %+v", us)
	}
}
