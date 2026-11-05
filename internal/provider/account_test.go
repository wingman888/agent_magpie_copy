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

// setupAgents points HOME, the XDG dirs and Claude's config dir at a fresh
// temp tree, so the tests never touch the real machine.
func setupAgents(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	old := CodexBase
	t.Cleanup(func() { CodexBase = old })
	return home
}

// jwt builds an unsigned token with the given payload, the way Codex's
// id_token and access_token look to dial (dial never checks signatures).
func jwt(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
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

func writeCodexLogin(t *testing.T, home, mode string) {
	t.Helper()
	exp := time.Now().Add(time.Hour).Unix()
	idTok := jwt(map[string]any{
		"email":                       "dev@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
	})
	accTok := jwt(map[string]any{"exp": exp})
	body := fmt.Sprintf(`{"auth_mode":%q,"OPENAI_API_KEY":null,"tokens":{"id_token":%q,"access_token":%q,"refresh_token":"rt","account_id":"acct-1"}}`,
		mode, idTok, accTok)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), body)
}

func writeCopilotLogin(t *testing.T) {
	t.Helper()
	body := `{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_secret"},"other":"x"}`
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json"), body)
}

func findID(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func TestCodexSignedInIsAccountProvider(t *testing.T) {
	home := setupAgents(t)
	writeCodexLogin(t, home, "chatgpt")

	p := findID(All(), CodexID)
	if p == nil {
		t.Fatal("codex not listed although signed in")
	}
	if p.Account == nil || p.Account.User != "dev@example.com" || p.Account.Plan != "plus" || p.Account.Agent != "Codex" {
		t.Fatalf("account = %+v", p.Account)
	}
	if p.Account.Expires.IsZero() {
		t.Error("expiry from access token not read")
	}
	if !p.Ready() {
		t.Error("account provider should be ready without a key")
	}
	if p.Responses != CodexBase {
		t.Errorf("responses base = %q, want CodexBase %q", p.Responses, CodexBase)
	}
	if p.Key != "" {
		t.Error("account provider must not carry a key")
	}
	if !p.Chosen("gpt-5.5") {
		t.Error("gpt-5.5 should be offered by default")
	}
	if e := findEntry(Catalog(), "codex/gpt-5.5"); e == nil {
		t.Error("codex/gpt-5.5 missing from the catalog")
	}
}

func TestCodexBaseOverride(t *testing.T) {
	home := setupAgents(t)
	writeCodexLogin(t, home, "chatgpt")
	CodexBase = "http://127.0.0.1:9/fake"
	p := findID(All(), CodexID)
	if p == nil || p.Responses != "http://127.0.0.1:9/fake" {
		t.Fatalf("CodexBase override not used: %+v", p)
	}
	if _, model, ok := Resolve("codex/gpt-5.5"); !ok || model != "gpt-5.5" {
		t.Errorf("codex/gpt-5.5 should resolve, got %q %v", model, ok)
	}
}

func TestCodexAPIKeyModeIsNotAccount(t *testing.T) {
	home := setupAgents(t)
	writeCodexLogin(t, home, "apikey")
	if findID(All(), CodexID) != nil {
		t.Fatal("apikey-mode Codex must not be an account provider")
	}
}

func TestUnsignedAccountPicksAreIgnored(t *testing.T) {
	setupAgents(t)
	if err := Save(Provider{ID: CodexID, Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: CopilotID, Models: []string{"gpt-4o"}}); err != nil {
		t.Fatal(err)
	}
	if len(All()) != 0 {
		t.Fatalf("residual picks made providers: %+v", All())
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") || strings.HasPrefix(e.ID, "copilot/") {
			t.Fatalf("catalog has %s although not signed in", e.ID)
		}
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		// Resolve may still find it through Find; it must not, when signed out.
		t.Error("codex should not resolve when signed out")
	}
}

func TestSaveAccountWritesOnlyModels(t *testing.T) {
	home := setupAgents(t)
	writeCodexLogin(t, home, "chatgpt")
	if err := Save(Provider{ID: CodexID, Name: "Codex", Key: "sk-leak", Chat: "https://x", Models: []string{" gpt-5.5 ", "gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, bad := range []string{"sk-leak", `"name"`, `"key"`, `"chat"`, "https://x"} {
		if strings.Contains(s, bad) {
			t.Errorf("config contains %s:\n%s", bad, s)
		}
	}
	if !strings.Contains(s, `"models"`) || !strings.Contains(s, "gpt-5.5") {
		t.Errorf("picks not saved:\n%s", s)
	}
	p := findID(All(), CodexID)
	if p == nil || len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Fatalf("picks not read back: %+v", p)
	}
	if strings.Contains(string(mustRead(t, filepath.Join(home, ".codex", "auth.json"))), "sk-leak") {
		t.Fatal("Save touched the Codex login")
	}
}

func TestDeleteAccountClearsPicksOnly(t *testing.T) {
	home := setupAgents(t)
	writeCodexLogin(t, home, "chatgpt")
	before := string(mustRead(t, filepath.Join(home, ".codex", "auth.json")))
	if err := Save(Provider{ID: CodexID, Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete(CodexID); err != nil {
		t.Fatal(err)
	}
	p := findID(All(), CodexID)
	if p == nil {
		t.Fatal("codex disappeared although still signed in")
	}
	if len(p.Models) != 0 {
		t.Errorf("picks not cleared: %v", p.Models)
	}
	if after := string(mustRead(t, filepath.Join(home, ".codex", "auth.json"))); after != before {
		t.Fatal("Delete changed the Codex login file")
	}
	if !p.Chosen("gpt-5.5") {
		t.Error("with picks cleared the built-in list should be offered")
	}
}

func TestDeleteAccountWhenSignedOutFails(t *testing.T) {
	setupAgents(t)
	if err := Delete(CopilotID); err == nil {
		t.Fatal("deleting a signed-out account provider should report no provider")
	}
}

func TestClaudeNeverOfferedButNoticed(t *testing.T) {
	setupAgents(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	writeFile(t, filepath.Join(cfg, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"sk-ant-secret"}}`)

	for _, p := range All() {
		if p.ID == ClaudeID {
			t.Fatal("claude listed as a provider")
		}
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "claude/") {
			t.Fatal("claude in catalog")
		}
	}
	if _, err := Find(ClaudeID); err == nil {
		t.Error("Find(claude) should fail")
	}
	if err := Save(Provider{ID: ClaudeID, Chat: "https://x", Key: "k"}); err == nil {
		t.Error("saving claude as a provider should fail")
	}
	ns := Notices()
	if len(ns) != 1 || ns[0].ID != ClaudeID || !strings.Contains(ns[0].Reason, "not offered") ||
		!strings.Contains(ns[0].Reason, "已登录但不提供") {
		t.Fatalf("notices = %+v", ns)
	}
	if strings.Contains(ns[0].Reason, "sk-ant") || strings.Contains(fmt.Sprint(All()), "sk-ant") {
		t.Fatal("claude token leaked")
	}
}

func TestClaudeNotSignedIn(t *testing.T) {
	setupAgents(t)
	if len(Notices()) != 0 {
		t.Fatal("notice without a Claude login")
	}
}

func TestCopilotSignedInAndBuiltinWithoutAuto(t *testing.T) {
	setupAgents(t)
	writeCopilotLogin(t)
	p := findID(All(), CopilotID)
	if p == nil || p.Account == nil || p.Account.User != "octo" {
		t.Fatalf("copilot = %+v", p)
	}
	if p.Key != "" || !p.Ready() {
		t.Error("copilot should be ready without a key")
	}
	ms := p.Available()
	if len(ms) == 0 {
		t.Fatal("copilot built-in list is empty")
	}
	for _, m := range ms {
		if m.ID == "auto" {
			t.Fatal("built-in copilot list offers auto")
		}
	}
	if e := findEntry(Catalog(), "copilot/claude-sonnet-4.5"); e == nil {
		t.Error("copilot/claude-sonnet-4.5 missing from the catalog")
	}
}

func TestCopilotChatModelFilter(t *testing.T) {
	body := `{"data":[
		{"id":"auto","capabilities":{"type":"chat"}},
		{"id":"gpt-5","name":"GPT-5","capabilities":{"type":"chat"},"model_picker_enabled":true,"policy":{"state":"enabled"}},
		{"id":"text-embedding-3","capabilities":{"type":"embeddings"}},
		{"id":"internal-router","capabilities":{"type":"chat"},"model_picker_enabled":false},
		{"id":"disabled-model","capabilities":{"type":"chat"},"policy":{"state":"disabled"}}
	]}`
	var v struct {
		Data []copilotRow `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	got := copilotChatModels(v.Data)
	if len(got) != 1 || got[0].ID != "gpt-5" || got[0].Name != "GPT-5" {
		t.Fatalf("filter = %+v", got)
	}
}

func TestCopilotRefreshSavesLiveList(t *testing.T) {
	setupAgents(t)
	writeCopilotLogin(t)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.Header.Get("Authorization") != "token gho_secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"token":"sess-1","expires_at":%d,"endpoints":{"api":%q}}`, time.Now().Add(time.Hour).Unix(), srv.URL)
		case "/models":
			if r.Header.Get("Authorization") != "Bearer sess-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			body := `{"data":[
				{"id":"auto","capabilities":{"type":"chat"}},
				{"id":"gpt-5.5","name":"GPT-5.5","capabilities":{"type":"chat"},"policy":{"state":"enabled"}},
				{"id":"claude-x","name":"Claude X","capabilities":{"type":"chat"},"policy":{"state":"unconfigured"}},
				{"id":"embed-1","capabilities":{"type":"embeddings"}}
			]}`
			fmt.Fprint(w, body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oldTok := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	defer func() { CopilotTokenURL = oldTok }()

	p := findID(All(), CopilotID)
	if p == nil {
		t.Fatal("copilot not listed")
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != "gpt-5.5" {
		t.Fatalf("refreshed list = %+v", ms)
	}
	// The live list is what the provider now offers.
	p = findID(All(), CopilotID)
	av := p.Available()
	if len(av) != 1 || av[0].ID != "gpt-5.5" {
		t.Fatalf("after refresh available = %+v", av)
	}
	if e := findEntry(Catalog(), "copilot/gpt-5.5"); e == nil {
		t.Error("copilot/gpt-5.5 missing after refresh")
	}
}

// A signed-in account provider lists every chat model its login can use,
// even past the usual "too many to expose" limit.
func TestCopilotExposesAllLiveModels(t *testing.T) {
	setupAgents(t)
	writeCopilotLogin(t)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			fmt.Fprintf(w, `{"token":"sess-1","expires_at":%d,"endpoints":{"api":%q}}`, time.Now().Add(time.Hour).Unix(), srv.URL)
		case "/models":
			var b strings.Builder
			b.WriteString(`{"data":[`)
			for i := 0; i < 30; i++ {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"id":"m%02d","name":"M%02d","capabilities":{"type":"chat"},"policy":{"state":"enabled"}}`, i, i)
			}
			b.WriteString(`]}`)
			fmt.Fprint(w, b.String())
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oldTok := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	defer func() { CopilotTokenURL = oldTok }()

	p := findID(All(), CopilotID)
	if p == nil {
		t.Fatal("copilot not listed")
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	p = findID(All(), CopilotID)
	ex := p.Exposed()
	if len(ex) != 30 {
		t.Fatalf("exposed %d models, want 30", len(ex))
	}
	n := 0
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "copilot/") {
			n++
		}
	}
	if n != 30 {
		t.Fatalf("catalog lists %d copilot models, want 30", n)
	}
}

func TestCopilotSessionExpiredMessage(t *testing.T) {
	setupAgents(t)
	writeCopilotLogin(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	oldTok := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/token"
	defer func() { CopilotTokenURL = oldTok }()

	p := findID(All(), CopilotID)
	_, err := p.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Fatalf("err = %v", err)
	}
}

func findEntry(es []Entry, id string) *Entry {
	for i := range es {
		if es[i].ID == id {
			return &es[i]
		}
	}
	return nil
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
