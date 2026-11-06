package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// s6Isolate points HOME and every config/cache dir at fresh temporary
// directories and forgets any cached Copilot state.
func s6Isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	s6ResetState()
	t.Cleanup(s6ResetState)
	return home
}

func s6ResetState() {
	copilotState.mu.Lock()
	copilotState.models = nil
	copilotState.mu.Unlock()
	copilotSessions.mu.Lock()
	copilotSessions.oauth = ""
	copilotSessions.sess = copilotSession{}
	copilotSessions.mu.Unlock()
}

func s6Write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// s6JWT builds an unsigned JWT carrying the given claims.
func s6JWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// s6Codex signs Codex in (ChatGPT mode) and returns the access token written.
// An empty email leaves the claim out.
func s6Codex(t *testing.T, home, email string, exp time.Time) string {
	t.Helper()
	id := map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}}
	if email != "" {
		id["email"] = email
	}
	access := s6JWT(map[string]any{"exp": exp.Unix()})
	doc := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      s6JWT(id),
			"access_token":  access,
			"refresh_token": "ref-s6",
			"account_id":    "acct-s6",
		},
	}
	b, _ := json.Marshal(doc)
	s6Write(t, filepath.Join(home, ".codex", "auth.json"), string(b))
	return access
}

// s6Copilot writes a GitHub Copilot apps.json entry. An empty user leaves the
// field out.
func s6Copilot(t *testing.T, home, user, oauth string) {
	t.Helper()
	entry := map[string]any{"oauth_token": oauth}
	if user != "" {
		entry["user"] = user
	}
	b, _ := json.Marshal(map[string]any{"github.com:s6": entry})
	s6Write(t, filepath.Join(home, "xdg", "github-copilot", "apps.json"), string(b))
}

func s6Find(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

// s6CopilotStub stands in for GitHub's token exchange and Copilot's model list.
// modelsJSON is the body returned from GET <api>/models.
func s6CopilotStub(t *testing.T, oauth, modelsJSON string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			if got := r.Header.Get("Authorization"); got != "token "+oauth {
				t.Errorf("token exchange Authorization = %q; want %q", got, "token "+oauth)
			}
			fmt.Fprintf(w, `{"token":"sess-s6","expires_at":%d,"endpoints":{"api":"http://%s/api"}}`,
				time.Now().Add(time.Hour).Unix(), r.Host)
		case "/api/models":
			if r.Method != http.MethodGet {
				t.Errorf("model list method = %s; want GET", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer sess-s6" {
				t.Errorf("model list Authorization = %q; want Bearer sess-s6", got)
			}
			if r.Header.Get("Copilot-Integration-Id") == "" {
				t.Errorf("model list lacks a Copilot-Integration-Id header")
			}
			io.WriteString(w, modelsJSON)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	t.Cleanup(func() { CopilotTokenURL = old })
	return srv
}

// s6CodexRefreshStub stands in for the OpenAI token endpoint. It records the
// request and answers with status and body.
func s6CodexRefreshStub(t *testing.T, status int, body string, sent *map[string]string, calls *int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		raw, _ := io.ReadAll(r.Body)
		if sent != nil {
			json.Unmarshal(raw, sent)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	t.Cleanup(func() { CodexRefreshURL = old })
}

const s6ModelsJSON = `{"data":[
 {"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}}},
 {"id":"copilot-search-s6","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"gpt-5.5-secondary","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"gpt-5.5-tertiary","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"gpt-5.5-4th","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"gpt-5.5-free-auto","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"exec-agent-s6","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"trajectory-s6","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"disabled-s6","model_picker_enabled":false,"policy":{"state":"disabled"},"capabilities":{"type":"chat"}},
 {"id":"embed-s6","model_picker_enabled":true,"capabilities":{"type":"embeddings"}},
 {"id":"policy-ok","model_picker_enabled":false,"policy":{"state":"enabled"},"capabilities":{"type":"chat"}},
 {"id":"gpt-5.5","name":"GPT-5.5","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","medium","high"]}}}
]}`

// ---- A1 -------------------------------------------------------------------

func TestS6A1AccountsListedAndCatalog(t *testing.T) {
	home := s6Isolate(t)
	s6Codex(t, home, "amy@example.com", time.Now().Add(time.Hour))
	s6Copilot(t, home, "octo", "gho_s6_a1")

	ps := Accounts()
	cx := s6Find(ps, "codex")
	cp := s6Find(ps, "copilot")
	if cx == nil || cp == nil {
		t.Fatalf("want codex and copilot providers, got %+v", ps)
	}
	if cx.Account == nil || cx.Account.User != "amy@example.com" || cx.Account.Plan != "pro" {
		t.Errorf("codex account = %+v; want amy@example.com with plan pro", cx.Account)
	}
	if cp.Account == nil || cp.Account.User != "octo" {
		t.Errorf("copilot account = %+v; want user octo", cp.Account)
	}
	if !cx.Ready() || !cp.Ready() {
		t.Errorf("account providers must be ready without a key")
	}

	ids := map[string]bool{}
	for _, e := range Catalog() {
		ids[e.ID] = true
	}
	for _, want := range []string{"codex/gpt-5.5", "copilot/claude-sonnet-4.5"} {
		if !ids[want] {
			t.Errorf("catalog lacks %s", want)
		}
	}
	if ids["copilot/auto"] {
		t.Errorf("catalog must not offer copilot/auto")
	}

	p, model, ok := Resolve("codex/gpt-5.5")
	if !ok || p.ID != "codex" || model != "gpt-5.5" {
		t.Errorf("Resolve(codex/gpt-5.5) = (%q, %q, %v); want (codex, gpt-5.5, true)", p.ID, model, ok)
	}
}

func TestS6A1DisplayNameFallbacks(t *testing.T) {
	home := s6Isolate(t)
	s6Codex(t, home, "", time.Now().Add(time.Hour))
	s6Copilot(t, home, "", "gho_s6_a1b")
	ps := Accounts()
	if cx := s6Find(ps, "codex"); cx == nil || cx.Account.User != "ChatGPT" {
		t.Errorf("codex without email should show ChatGPT, got %+v", cx)
	}
	if cp := s6Find(ps, "copilot"); cp == nil || cp.Account.User != "GitHub" {
		t.Errorf("copilot without user should show GitHub, got %+v", cp)
	}
}

func TestS6A1CopilotBuiltinWithoutAuto(t *testing.T) {
	s6Isolate(t)
	ids := map[string]bool{}
	for _, m := range copilotModels() {
		ids[m.ID] = true
	}
	if !ids["claude-sonnet-4.5"] {
		t.Errorf("built-in Copilot list lacks claude-sonnet-4.5")
	}
	if ids["auto"] {
		t.Errorf("built-in Copilot list must not include auto")
	}
}

// ---- A2 -------------------------------------------------------------------

func TestS6A2SaveKeepsOnlyModels(t *testing.T) {
	home := s6Isolate(t)
	s6Codex(t, home, "amy@example.com", time.Now().Add(time.Hour))

	err := Save(Provider{ID: "codex", Name: "junk", Chat: "https://evil.example.test/v1",
		Key: "sk-s6-secret", Models: []string{"gpt-5.5"}})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"evil.example.test", "sk-s6-secret"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("providers file holds %q: %s", bad, raw)
		}
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "" || p.Key != "" {
		t.Errorf("account provider kept address or key: chat=%q key=%q", p.Chat, p.Key)
	}
	if got := p.Exposed(); len(got) != 1 || got[0].ID != "gpt-5.5" {
		t.Errorf("Exposed = %v; want only gpt-5.5", got)
	}

	if err := Delete("codex"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	p, err = Find("codex")
	if err != nil {
		t.Fatalf("codex must remain after delete: %v", err)
	}
	if len(p.Models) != 0 {
		t.Errorf("Models = %v after delete; want the choice cleared", p.Models)
	}
}

// ---- A3 -------------------------------------------------------------------

func TestS6A3SignOutRemovesProviderEvenWithStoredChoice(t *testing.T) {
	home := s6Isolate(t)
	s6Codex(t, home, "amy@example.com", time.Now().Add(time.Hour))
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if s6Find(All(), "codex") != nil {
		t.Errorf("codex still listed after sign-out")
	}
	if s6Find(Accounts(), "codex") != nil {
		t.Errorf("codex still in Accounts() after sign-out")
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Errorf("codex/gpt-5.5 still resolves after sign-out")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Errorf("catalog still lists %s after sign-out", e.ID)
		}
	}
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err == nil {
		t.Errorf("saving a choice for a signed-out account should fail")
	}
}

func TestS6A3APIKeyModeIsNotAnAccount(t *testing.T) {
	for _, mode := range []string{"apikey", "api_key"} {
		home := s6Isolate(t)
		doc := map[string]any{
			"auth_mode":      mode,
			"OPENAI_API_KEY": "sk-s6-live",
			"tokens": map[string]any{
				"id_token":      s6JWT(map[string]any{"email": "amy@example.com"}),
				"access_token":  s6JWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
				"refresh_token": "ref",
				"account_id":    "acct-s6",
			},
		}
		b, _ := json.Marshal(doc)
		s6Write(t, filepath.Join(home, ".codex", "auth.json"), string(b))
		if s6Find(Accounts(), "codex") != nil {
			t.Errorf("auth_mode %q treated as a ChatGPT account", mode)
		}
		if s6Find(All(), "codex") != nil {
			t.Errorf("auth_mode %q listed as a provider", mode)
		}
	}
}

// ---- A6 -------------------------------------------------------------------

func TestS6A6ExpiringTokenRefreshedAndWrittenBack(t *testing.T) {
	home := s6Isolate(t)
	s6Codex(t, home, "amy@example.com", time.Now().Add(2*time.Minute))
	path := filepath.Join(home, ".codex", "auth.json")
	raw, _ := os.ReadFile(path)
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["custom_field"] = "keep-me"
	doc["tokens"].(map[string]any)["extra"] = "kept"
	b, _ := json.Marshal(doc)
	s6Write(t, path, string(b))

	var sent map[string]string
	var calls int
	s6CodexRefreshStub(t, http.StatusOK, `{"access_token":"acc-s6-new","refresh_token":"ref-s6-new"}`, &sent, &calls)

	p := s6Find(Accounts(), "codex")
	if p == nil {
		t.Fatal("codex account missing")
	}
	req := &http.Request{Header: http.Header{}}
	if err := p.Sign(context.Background(), req, Responses, []byte(`{"model":"gpt-5.5"}`)); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if calls != 1 {
		t.Errorf("refresh calls = %d; want 1", calls)
	}
	if sent["refresh_token"] != "ref-s6" || sent["grant_type"] != "refresh_token" {
		t.Errorf("refresh request = %v", sent)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer acc-s6-new" {
		t.Errorf("Authorization = %q; want the refreshed token", got)
	}
	if got := req.Header.Get("chatgpt-account-id"); got != "acct-s6" {
		t.Errorf("chatgpt-account-id = %q", got)
	}

	raw, _ = os.ReadFile(path)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	tokens, _ := out["tokens"].(map[string]any)
	if tokens["access_token"] != "acc-s6-new" || tokens["refresh_token"] != "ref-s6-new" {
		t.Errorf("auth.json tokens not updated: %v", tokens)
	}
	if out["custom_field"] != "keep-me" || tokens["extra"] != "kept" {
		t.Errorf("unrelated fields were not preserved: %v", out)
	}
	if tokens["account_id"] != "acct-s6" || tokens["id_token"] == nil || tokens["id_token"] == "" {
		t.Errorf("account or id token lost: %v", tokens)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("auth.json mode = %v; want 0600", st.Mode().Perm())
	}
}

func TestS6A6FreshTokenNotRefreshed(t *testing.T) {
	home := s6Isolate(t)
	access := s6Codex(t, home, "amy@example.com", time.Now().Add(time.Hour))
	var calls int
	s6CodexRefreshStub(t, http.StatusInternalServerError, `{}`, nil, &calls)

	p := s6Find(Accounts(), "codex")
	if p == nil {
		t.Fatal("codex account missing")
	}
	req := &http.Request{Header: http.Header{}}
	if err := p.Sign(context.Background(), req, Responses, []byte(`{}`)); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if calls != 0 {
		t.Errorf("fresh token refreshed %d times", calls)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+access {
		t.Errorf("Authorization = %q; want the current token", got)
	}
}

func TestS6A6RefreshFailureAsksToSignInAgain(t *testing.T) {
	home := s6Isolate(t)
	s6Codex(t, home, "amy@example.com", time.Now().Add(time.Minute))
	var calls int
	s6CodexRefreshStub(t, http.StatusBadRequest, `{"error":"invalid_grant"}`, nil, &calls)

	p := s6Find(Accounts(), "codex")
	if p == nil {
		t.Fatal("codex account missing")
	}
	req := &http.Request{Header: http.Header{}}
	err := p.Sign(context.Background(), req, Responses, []byte(`{}`))
	if err == nil {
		t.Fatal("Sign succeeded with a refresh the backend refused")
	}
	for _, want := range []string{"已登出", "请重新登录"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err.Error(), want)
		}
	}
}

// ---- A7 -------------------------------------------------------------------

func TestS6A7CopilotRefreshKeepsUsableChatModels(t *testing.T) {
	home := s6Isolate(t)
	const oauth = "gho_s6_a7"
	s6Copilot(t, home, "octo", oauth)
	s6CopilotStub(t, oauth, s6ModelsJSON)

	ms, err := RefreshCopilot(context.Background())
	if err != nil {
		t.Fatalf("RefreshCopilot: %v", err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if got, want := strings.Join(ids, ","), "claude-sonnet-4.5,policy-ok,gpt-5.5"; got != want {
		t.Fatalf("models = %s; want %s", got, want)
	}
	if got := strings.Join(ms[0].Efforts, ","); got != "low,high" {
		t.Errorf("claude-sonnet-4.5 efforts = %s; want low,high", got)
	}
	if got := strings.Join(ms[2].Efforts, ","); got != "low,medium,high" {
		t.Errorf("gpt-5.5 efforts = %s; want low,medium,high", got)
	}

	p := s6Find(Accounts(), "copilot")
	if p == nil {
		t.Fatal("copilot account missing")
	}
	var avail []string
	for _, m := range p.Available() {
		avail = append(avail, m.ID)
	}
	if strings.Join(avail, ",") != strings.Join(ids, ",") {
		t.Errorf("usable list after refresh = %v; want %v", avail, ids)
	}

	// Signed chat requests go to the session's API endpoint with the session token.
	req := &http.Request{Header: http.Header{}}
	body := []byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`)
	if err := p.Sign(context.Background(), req, Chat, body); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !strings.HasSuffix(req.URL.Path, "/api/chat/completions") {
		t.Errorf("chat request path = %s; want …/api/chat/completions", req.URL.Path)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sess-s6" {
		t.Errorf("Authorization = %q; want Bearer sess-s6", got)
	}
	if req.Header.Get("Copilot-Integration-Id") == "" {
		t.Errorf("Copilot-Integration-Id missing")
	}
	if got := req.Header.Get("X-Initiator"); got != "user" {
		t.Errorf("X-Initiator = %q; want user for a user-last conversation", got)
	}

	tool := []byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hi"},{"role":"tool","content":"result"}]}`)
	req2 := &http.Request{Header: http.Header{}}
	if err := p.Sign(context.Background(), req2, Chat, tool); err != nil {
		t.Fatalf("Sign tool: %v", err)
	}
	if got := req2.Header.Get("X-Initiator"); got != "agent" {
		t.Errorf("X-Initiator = %q; want agent when the last message is a tool reply", got)
	}
}

func TestS6A7CopilotRefreshWithNoChatModelsFails(t *testing.T) {
	home := s6Isolate(t)
	const oauth = "gho_s6_a7b"
	s6Copilot(t, home, "octo", oauth)
	s6CopilotStub(t, oauth, `{"data":[{"id":"copilot-search-s6","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`)
	if _, err := RefreshCopilot(context.Background()); err == nil {
		t.Errorf("RefreshCopilot with only internal models should fail")
	}
}

// Once refreshed, an account offers its whole list, even past the usual
// "too many to expose" cut-off.
func TestS6AccountOffersWholeRefreshedList(t *testing.T) {
	home := s6Isolate(t)
	s6Copilot(t, home, "octo", "gho_s6_all")
	var ms []catalog.Model
	for i := 0; i < 30; i++ {
		ms = append(ms, catalog.Model{ID: fmt.Sprintf("m-s6-%02d", i), Name: "m"})
	}
	copilotState.mu.Lock()
	copilotState.models = ms
	copilotState.mu.Unlock()
	p := s6Find(Accounts(), "copilot")
	if p == nil {
		t.Fatal("copilot account missing")
	}
	if got := len(p.Exposed()); got != 30 {
		t.Errorf("Exposed() = %d models; want all 30", got)
	}
}

// ---- A8 -------------------------------------------------------------------

func TestS6A8ClaudeSignInExcluded(t *testing.T) {
	home := s6Isolate(t)
	cred := filepath.Join(home, "claude", ".credentials.json")
	s6Write(t, cred, `{"claudeAiOauth":{"accessToken":"SECRET-s6-token"}}`)

	ex := Excluded()
	if len(ex) != 1 {
		t.Fatalf("Excluded() = %+v; want one claude entry", ex)
	}
	if ex[0].Agent != "claude" || strings.TrimSpace(ex[0].Why) == "" {
		t.Errorf("exclusion = %+v; want agent claude with a reason", ex[0])
	}
	if strings.Contains(ex[0].Why, "SECRET-s6-token") {
		t.Errorf("reason leaks credential contents")
	}
	if !strings.Contains(ex[0].Why, "Anthropic") || !strings.Contains(ex[0].Why, "API Key") {
		t.Errorf("reason should name Anthropic terms and the API Key alternative: %q", ex[0].Why)
	}
	for _, p := range Accounts() {
		if p.ID == "claude" || (p.Account != nil && p.Account.Agent == "claude") {
			t.Errorf("claude sign-in offered as provider: %+v", p)
		}
	}
	if s6Find(All(), "claude") != nil {
		t.Errorf("claude listed as a provider")
	}

	// The cached answer must notice a sign-out within the minute.
	if err := os.Remove(cred); err != nil {
		t.Fatal(err)
	}
	if got := Excluded(); len(got) != 0 {
		t.Errorf("Excluded() after sign-out = %+v; want none", got)
	}
}

func TestS6A8NoClaudeSignInNoExclusion(t *testing.T) {
	s6Isolate(t)
	if got := Excluded(); len(got) != 0 {
		t.Errorf("Excluded() with no Claude sign-in = %+v; want none", got)
	}
}
