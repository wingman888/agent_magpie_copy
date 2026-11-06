package provider_test

// Black-box checks of the acceptance criteria for account providers (Codex,
// Copilot) and the Claude Code exclusion. Only exported API is used: Accounts,
// Excluded, Catalog, Resolve, Find, Save, Delete, Provider.Sign, SignedRequest,
// Prepare, Fetch, and the CodexBase / CopilotTokenURL variables. Every test
// uses a temporary HOME and XDG dirs and httptest stand-ins for upstreams.

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
	"sync"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// bbHome points HOME, the XDG dirs and the Claude config dir at a fresh tree.
func bbHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

func bbWrite(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// bbJWT is an unsigned token whose payload is the given claims.
func bbJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// bbCodexLogin writes a ChatGPT-mode Codex sign-in for dev@example.com on the
// pro plan, with an access token expiring at exp. It returns the access token.
func bbCodexLogin(t *testing.T, home string, exp int64) string {
	t.Helper()
	id := bbJWT(map[string]any{
		"email":                       "dev@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-1"},
	})
	access := bbJWT(map[string]any{"exp": exp})
	bbWrite(t, filepath.Join(home, ".codex", "auth.json"),
		fmt.Sprintf(`{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":%q,"access_token":%q,"refresh_token":"R1","account_id":"acct-1"}}`, id, access))
	return access
}

// bbCopilotLogin writes a GitHub Copilot editor sign-in for octocat.
func bbCopilotLogin(t *testing.T, home string) {
	t.Helper()
	bbWrite(t, filepath.Join(home, "config", "github-copilot", "apps.json"),
		`{"github.com:Iv1.test":{"user":"octocat","oauth_token":"gho_TEST"}}`)
}

func bbFind(ps []provider.Provider, id string) (provider.Provider, bool) {
	for _, p := range ps {
		if p.ID == id {
			return p, true
		}
	}
	return provider.Provider{}, false
}

func bbCatalogIDs() map[string]bool {
	ids := map[string]bool{}
	for _, e := range provider.Catalog() {
		ids[e.ID] = true
	}
	return ids
}

// bbSetString replaces a package variable for the rest of the test.
func bbSetString(t *testing.T, p *string, v string) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// ---- acceptance 1: both sign-ins are providers, models are in the catalog ----

func TestAcceptance1BothSignInsOfferedAsReadyProviders(t *testing.T) {
	home := bbHome(t)
	bbCodexLogin(t, home, time.Now().Add(time.Hour).Unix())
	bbCopilotLogin(t, home)

	ps := provider.Accounts()
	cx, ok := bbFind(ps, "codex")
	if !ok {
		t.Fatalf("codex not offered: %+v", ps)
	}
	if cx.Account == nil || cx.Account.Agent != "codex" || cx.Account.User != "dev@example.com" || cx.Account.Plan != "pro" {
		t.Errorf("codex account = %+v, want dev@example.com on pro", cx.Account)
	}
	if !cx.Ready() {
		t.Error("codex provider is not ready")
	}
	cp, ok := bbFind(ps, "copilot")
	if !ok {
		t.Fatalf("copilot not offered: %+v", ps)
	}
	if cp.Account == nil || cp.Account.Agent != "copilot" || cp.Account.User != "octocat" {
		t.Errorf("copilot account = %+v, want octocat", cp.Account)
	}
	if !cp.Ready() {
		t.Error("copilot provider is not ready")
	}

	ids := bbCatalogIDs()
	for _, want := range []string{"codex/gpt-5.5", "copilot/claude-sonnet-4.5"} {
		if !ids[want] {
			t.Errorf("catalog lacks %s", want)
		}
	}
	if ids["copilot/auto"] {
		t.Error("catalog offers copilot/auto before any refresh")
	}

	p, model, ok := provider.Resolve("codex/gpt-5.5")
	if !ok || p.ID != "codex" || model != "gpt-5.5" {
		t.Errorf("Resolve(codex/gpt-5.5) = %q, %q, %v", p.ID, model, ok)
	}
	p, model, ok = provider.Resolve("copilot/claude-sonnet-4.5")
	if !ok || p.ID != "copilot" || model != "claude-sonnet-4.5" {
		t.Errorf("Resolve(copilot/claude-sonnet-4.5) = %q, %q, %v", p.ID, model, ok)
	}
}

// ---- acceptance 2: only the model choice is stored ----

func TestAcceptance2SelectionIsTheOnlyThingStored(t *testing.T) {
	home := bbHome(t)
	bbCodexLogin(t, home, time.Now().Add(time.Hour).Unix())

	err := provider.Save(provider.Provider{
		ID: "codex", Name: "Codex", Key: "SECRET-KEY-123",
		Chat: "https://evil.example/v1", Responses: "https://evil.example/v1",
		Models: []string{"gpt-5.5"},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"SECRET-KEY-123", "evil.example", `"key"`, `"chat"`, `"responses"`, `"anthropic"`, `"account"`} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("stored file contains %s: %s", bad, raw)
		}
	}
	var f struct {
		Providers []map[string]json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Providers) != 1 {
		t.Fatalf("stored providers = %d, want 1: %s", len(f.Providers), raw)
	}
	for k := range f.Providers[0] {
		if k != "id" && k != "models" {
			t.Errorf("stored account entry has key %q: %s", k, raw)
		}
	}

	cx, ok := bbFind(provider.Accounts(), "codex")
	if !ok {
		t.Fatal("codex gone after saving a selection")
	}
	if cx.Key != "" || cx.Chat != "" || cx.Responses != "" || cx.Anthropic != "" {
		t.Errorf("account provider carries an address or key: %+v", cx)
	}
	var exposed []string
	for _, m := range cx.Exposed() {
		exposed = append(exposed, m.ID)
	}
	if len(exposed) != 1 || exposed[0] != "gpt-5.5" {
		t.Errorf("exposed = %v, want [gpt-5.5]", exposed)
	}
	for id := range bbCatalogIDs() {
		if strings.HasPrefix(id, "codex/") && id != "codex/gpt-5.5" {
			t.Errorf("catalog exposes %s beyond the selection", id)
		}
	}

	if err := provider.Delete("codex"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	cx, ok = bbFind(provider.Accounts(), "codex")
	if !ok {
		t.Fatal("codex provider removed by delete; only the selection should go")
	}
	if len(cx.Models) != 0 {
		t.Errorf("selection not cleared: %v", cx.Models)
	}
	raw, _ = os.ReadFile(provider.Path())
	if strings.Contains(string(raw), "gpt-5.5") {
		t.Errorf("selection still stored after delete: %s", raw)
	}
}

// ---- acceptance 3: logout removes the provider, even with a leftover selection ----

func TestAcceptance3LogoutRemovesProviderEvenWithSelection(t *testing.T) {
	home := bbHome(t)
	bbCodexLogin(t, home, time.Now().Add(time.Hour).Unix())
	bbCopilotLogin(t, home)
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := os.Remove(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}

	ps := provider.Accounts()
	if _, ok := bbFind(ps, "codex"); ok {
		t.Error("codex still offered after logout")
	}
	if _, ok := bbFind(ps, "copilot"); !ok {
		t.Error("copilot lost when codex logged out")
	}
	for id := range bbCatalogIDs() {
		if strings.HasPrefix(id, "codex/") {
			t.Errorf("catalog still lists %s after logout", id)
		}
	}
	if _, _, ok := provider.Resolve("codex/gpt-5.5"); ok {
		t.Error("codex/gpt-5.5 still resolves after logout")
	}
	if _, err := provider.Find("codex"); err == nil {
		t.Error("Find(codex) succeeds after logout")
	}
}

func TestAcceptance3APIKeyModeIsNotASignIn(t *testing.T) {
	home := bbHome(t)
	bbWrite(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test-123","tokens":{"access_token":"ACCESS-LEFTOVER","refresh_token":"R"}}`)
	if _, ok := bbFind(provider.Accounts(), "codex"); ok {
		t.Error("codex in API-key mode offered as a sign-in")
	}
}

// ---- acceptance 7: Copilot refresh, filtering, signing ----

const bbCopilotModels = `{"data":[
 {"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","medium","high"]}},"policy":{"state":"enabled"}},
 {"id":"copilot-search-v2","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"text-embedding-3-small","model_picker_enabled":true,"capabilities":{"type":"embeddings"}},
 {"id":"gpt-5.5-secondary","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"old-model","model_picker_enabled":false,"capabilities":{"type":"chat"},"policy":{"state":"disabled"}},
 {"id":"gpt-5.5","model_picker_enabled":false,"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}},"policy":{"state":"enabled"}},
 {"id":"exec-agent-1","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"trajectory-x","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"claude-haiku-4.5-free-auto","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"gpt-4.1-4th","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"gpt-5-mini-tertiary","model_picker_enabled":true,"capabilities":{"type":"chat"}}
]}`

// bbCopilot stands in for GitHub's token exchange and the Copilot API the
// session token points at. It records the headers of the last model list and
// chat request it saw.
type bbCopilot struct {
	mu        sync.Mutex
	modelHead http.Header
	chatHead  http.Header
	modelHits int
	chatHits  int
}

func (c *bbCopilot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch r.URL.Path {
	case "/copilot_internal/v2/token":
		if r.Header.Get("Authorization") != "token gho_TEST" {
			http.Error(w, "bad oauth", http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"token":"SESSION-TOK","expires_at":%d,"endpoints":{"api":%q}}`,
			time.Now().Add(time.Hour).Unix(), "http://"+r.Host+"/api")
	case "/api/models":
		c.modelHits++
		c.modelHead = r.Header.Clone()
		if r.Header.Get("Authorization") != "Bearer SESSION-TOK" {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		io.WriteString(w, bbCopilotModels)
	case "/api/chat/completions":
		c.chatHits++
		c.chatHead = r.Header.Clone()
		io.Copy(io.Discard, r.Body)
		if r.Header.Get("Authorization") != "Bearer SESSION-TOK" {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	default:
		http.NotFound(w, r)
	}
}

func bbCopilotStandIn(t *testing.T) (*httptest.Server, *bbCopilot) {
	t.Helper()
	c := &bbCopilot{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	bbSetString(t, &provider.CopilotTokenURL, srv.URL+"/copilot_internal/v2/token")
	return srv, c
}

func TestAcceptance7CopilotRefreshKeepsChatModelsAndSigns(t *testing.T) {
	home := bbHome(t)
	bbCopilotLogin(t, home)
	_, c := bbCopilotStandIn(t)

	cp, ok := bbFind(provider.Accounts(), "copilot")
	if !ok {
		t.Fatal("copilot not offered")
	}
	ms, err := cp.Fetch(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "claude-sonnet-4.5,gpt-5.5" {
		t.Fatalf("refreshed models = %v, want [claude-sonnet-4.5 gpt-5.5]", ids)
	}
	if got := strings.Join(ms[0].Efforts, ","); got != "low,medium,high" {
		t.Errorf("claude-sonnet-4.5 efforts = %q", got)
	}
	if got := strings.Join(ms[1].Efforts, ","); got != "low,high" {
		t.Errorf("gpt-5.5 efforts = %q", got)
	}

	c.mu.Lock()
	if c.modelHits != 1 {
		t.Errorf("model list requested %d times, want 1", c.modelHits)
	}
	if got := c.modelHead.Get("Authorization"); got != "Bearer SESSION-TOK" {
		t.Errorf("model list Authorization = %q", got)
	}
	if c.modelHead.Get("Copilot-Integration-Id") == "" {
		t.Error("model list request lacks Copilot-Integration-Id")
	}
	c.mu.Unlock()

	// After a refresh the usable list is the refreshed one.
	cp, _ = bbFind(provider.Accounts(), "copilot")
	var avail []string
	for _, m := range cp.Available() {
		avail = append(avail, m.ID)
	}
	if strings.Join(avail, ",") != "claude-sonnet-4.5,gpt-5.5" {
		t.Errorf("available after refresh = %v", avail)
	}
	cat := bbCatalogIDs()
	if !cat["copilot/claude-sonnet-4.5"] {
		t.Error("catalog lacks copilot/claude-sonnet-4.5 after refresh")
	}
	for _, bad := range []string{"copilot/auto", "copilot/copilot-search-v2", "copilot/old-model"} {
		if cat[bad] {
			t.Errorf("catalog offers %s", bad)
		}
	}

	// Signing: the request goes to the session's API base, with the session
	// token, and its initiator follows the last message.
	ctx := context.Background()
	userLast := []byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`)
	agentLast := []byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"},{"role":"tool","tool_call_id":"t1","content":"42"}]}`)
	vision := []byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`)

	for _, tc := range []struct {
		name, initiator string
		body            []byte
		vision          bool
	}{
		{"user-last", "user", userLast, false},
		{"tool-last", "agent", agentLast, false},
		{"vision", "user", vision, true},
	} {
		req, err := cp.SignedRequest(ctx, provider.Chat, tc.body)
		if err != nil {
			t.Fatalf("%s: sign: %v", tc.name, err)
		}
		if !strings.HasSuffix(req.URL.Path, "/api/chat/completions") {
			t.Errorf("%s: signed URL = %s, want the session API base + /chat/completions", tc.name, req.URL)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer SESSION-TOK" {
			t.Errorf("%s: Authorization = %q", tc.name, got)
		}
		if req.Header.Get("Copilot-Integration-Id") == "" {
			t.Errorf("%s: no Copilot-Integration-Id", tc.name)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: send: %v", tc.name, err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", tc.name, res.StatusCode)
		}
		c.mu.Lock()
		if got := c.chatHead.Get("X-Initiator"); got != tc.initiator {
			t.Errorf("%s: X-Initiator = %q, want %q", tc.name, got, tc.initiator)
		}
		if tc.vision && c.chatHead.Get("Copilot-Vision-Request") == "" {
			t.Errorf("%s: no Copilot-Vision-Request header", tc.name)
		}
		c.mu.Unlock()
	}
}

// ---- acceptance 8: Claude Code login is recognised and excluded ----

func TestAcceptance8ClaudeLoginExcludedWithReason(t *testing.T) {
	home := bbHome(t)
	bbWrite(t, filepath.Join(home, "claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"SECRET-CLAUDE"}}`)

	ex := provider.Excluded()
	var found *provider.Exclusion
	for i := range ex {
		if ex[i].Agent == "claude" {
			found = &ex[i]
		}
	}
	if found == nil {
		t.Fatalf("Excluded() = %+v, want an entry for claude", ex)
	}
	if strings.TrimSpace(found.Why) == "" {
		t.Error("exclusion has an empty reason")
	}
	if strings.Contains(found.Why, "SECRET-CLAUDE") {
		t.Error("exclusion reason leaks the credential")
	}
	for _, p := range provider.Accounts() {
		if p.ID == "claude" || (p.Account != nil && p.Account.Agent == "claude") {
			t.Errorf("claude offered as a provider: %+v", p)
		}
	}
}

func TestAcceptance8NoClaudeLoginNoExclusion(t *testing.T) {
	bbHome(t)
	if ex := provider.Excluded(); len(ex) != 0 {
		t.Errorf("Excluded() = %+v with no sign-ins, want none", ex)
	}
}

// ---- robustness: missing files, bad credentials, upstream errors ----

func TestAccountsAndExcludedWithMissingHomeDoNotPanic(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "does-not-exist"))
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if ps := provider.Accounts(); len(ps) != 0 {
		t.Errorf("Accounts() = %+v with no sign-ins", ps)
	}
	_ = provider.Excluded()
}

func TestSignWithoutCredentialsReturnsError(t *testing.T) {
	bbHome(t)
	ctx := context.Background()
	body := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`)

	for _, p := range []provider.Provider{
		{ID: "codex", Name: "Codex", Account: &provider.Account{Agent: "codex"}},
		{ID: "copilot", Name: "Copilot", Account: &provider.Account{Agent: "copilot"}},
		{ID: "odd", Name: "Odd", Account: &provider.Account{Agent: "other"}},
	} {
		req, err := http.NewRequest(http.MethodPost, "http://example.invalid/", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Sign(ctx, req, provider.Chat, body); err == nil {
			t.Errorf("%s: Sign without a sign-in returned no error", p.ID)
		}
		if _, err := p.SignedRequest(ctx, provider.Chat, body); err == nil {
			t.Errorf("%s: SignedRequest without a sign-in returned no error", p.ID)
		}
	}
	// Copilot's refresh needs the sign-in; Codex's refresh is the built-in list
	// and needs none, so only Copilot is checked here.
	cpOnly := provider.Provider{ID: "copilot", Name: "Copilot", Account: &provider.Account{Agent: "copilot"}}
	if _, err := cpOnly.Fetch(ctx); err == nil {
		t.Error("copilot: Fetch without a sign-in returned no error")
	}

	// A provider of one's own still signs with its key.
	plain := provider.Provider{ID: "mine", Name: "Mine", Key: "K", Chat: "https://api.example.com/v1"}
	req, _ := http.NewRequest(http.MethodPost, "https://api.example.com/v1/chat/completions", nil)
	if err := plain.Sign(ctx, req, provider.Chat, body); err != nil {
		t.Errorf("plain provider Sign: %v", err)
	}
}

func TestPrepareLeavesPlainProvidersAndBadBodiesAlone(t *testing.T) {
	plain := provider.Provider{ID: "mine", Key: "K"}
	in := []byte(`{"model":"m","input":"hi","max_output_tokens":4}`)
	if got := plain.Prepare(in); string(got) != string(in) {
		t.Errorf("plain provider changed body: %s", got)
	}
	cx := provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex"}}
	bad := []byte("not json")
	if got := cx.Prepare(bad); string(got) != string(bad) {
		t.Errorf("codex Prepare changed an unparseable body: %s", got)
	}
}

func TestCopilotRefreshUpstreamErrorsAreErrors(t *testing.T) {
	home := bbHome(t)
	bbCopilotLogin(t, home)
	ctx := context.Background()

	// Token exchange answers 500.
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(fail.Close)
	bbSetString(t, &provider.CopilotTokenURL, fail.URL+"/copilot_internal/v2/token")
	cp, ok := bbFind(provider.Accounts(), "copilot")
	if !ok {
		t.Fatal("copilot not offered")
	}
	if _, err := cp.Fetch(ctx); err == nil {
		t.Error("token exchange 500 gave no error")
	}

	// Token exchange works, the model list answers 500.
	modelsFail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/copilot_internal/v2/token" {
			fmt.Fprintf(w, `{"token":"S2","expires_at":%d,"endpoints":{"api":%q}}`,
				time.Now().Add(time.Hour).Unix(), "http://"+r.Host+"/api")
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(modelsFail.Close)
	bbSetString(t, &provider.CopilotTokenURL, modelsFail.URL+"/copilot_internal/v2/token")
	if _, err := cp.Fetch(ctx); err == nil {
		t.Error("model list 500 gave no error")
	}

	// Only internal or disabled models: nothing usable is an error.
	none := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/copilot_internal/v2/token" {
			fmt.Fprintf(w, `{"token":"S3","expires_at":%d,"endpoints":{"api":%q}}`,
				time.Now().Add(time.Hour).Unix(), "http://"+r.Host+"/api")
			return
		}
		io.WriteString(w, `{"data":[{"id":"exec-agent-1","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`)
	}))
	t.Cleanup(none.Close)
	bbSetString(t, &provider.CopilotTokenURL, none.URL+"/copilot_internal/v2/token")
	if _, err := cp.Fetch(ctx); err == nil {
		t.Error("no usable chat model gave no error")
	}
}
