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

	"github.com/yetone/dial/internal/catalog"
)

// sandbox points every file dial and the agents read at a temp directory.
func sandbox(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeJWT builds an unsigned token whose payload is the given claims.
func fakeJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func writeCodex(t *testing.T, email, plan string) {
	t.Helper()
	idTok := fakeJWT(map[string]any{
		"email":                        email,
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": plan},
	})
	acc := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	b, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token": idTok, "access_token": acc,
			"refresh_token": "rt-secret", "account_id": "acct-1",
		},
	})
	writeFile(t, codexPath(), string(b))
}

func writeCopilot(t *testing.T) {
	t.Helper()
	writeFile(t, copilotPath(), `{
  "github.com:Iv1.abc": {"user": "octo", "oauth_token": "gho_test"},
  "other.example:xyz": {"user": "nobody", "oauth_token": "nope"}
}`)
}

func setString(t *testing.T, p *string, v string) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func findByID(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func hasEntry(es []Entry, id string) bool {
	for _, e := range es {
		if e.ID == id {
			return true
		}
	}
	return false
}

func TestCodexSignInIsProvider(t *testing.T) {
	sandbox(t)
	writeCodex(t, "dev@example.com", "plus")

	p := findByID(All(), "codex")
	if p == nil {
		t.Fatal("codex not in All() while signed in")
	}
	if p.Account == nil || p.Account.User != "dev@example.com" || p.Account.Plan != "plus" || p.Account.Source != "codex" {
		t.Fatalf("account = %+v", p.Account)
	}
	if !p.Ready() {
		t.Error("sign-in provider should be ready without a key")
	}
	if !hasEntry(Catalog(), "codex/gpt-5.5") {
		t.Error("codex/gpt-5.5 missing from the catalog before any pick or refresh")
	}
	var found bool
	for _, l := range Logins() {
		if l.Agent == "codex" {
			found = true
			if !l.Signed || !l.Provided || l.User != "dev@example.com" {
				t.Errorf("codex login = %+v", l)
			}
		}
	}
	if !found {
		t.Error("codex missing from Logins()")
	}
}

func TestCodexAPIKeyModeIsNotAccount(t *testing.T) {
	sandbox(t)
	writeFile(t, codexPath(), `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-x"}`)

	if findByID(All(), "codex") != nil {
		t.Fatal("API-key Codex login must not be a provider")
	}
	for _, l := range Logins() {
		if l.Agent == "codex" && (l.Signed || l.Provided || l.Note == "") {
			t.Errorf("codex login = %+v", l)
		}
	}
}

func TestSaveAndDeleteSignInOnlyTouchPicks(t *testing.T) {
	sandbox(t)
	writeCodex(t, "dev@example.com", "plus")

	if err := Save(Provider{ID: "codex", Name: "ignored", Models: []string{"gpt-5.5", "gpt-5.5", " "}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
		SignIns   map[string][]string `json:"signins"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Providers) != 0 {
		t.Errorf("sign-in picks leaked into the provider list: %s", b)
	}
	if got := f.SignIns["codex"]; len(got) != 1 || got[0] != "gpt-5.5" {
		t.Errorf("stored picks = %v, want [gpt-5.5]: %s", got, b)
	}
	if strings.Contains(string(b), "rt-secret") || strings.Contains(string(b), "dev@example.com") || strings.Contains(string(b), "ignored") {
		t.Errorf("config file holds credentials or other fields: %s", b)
	}

	p := findByID(All(), "codex")
	if p == nil || len(p.Models) != 1 || p.Models[0] != "gpt-5.5" {
		t.Fatalf("picks after save = %+v", p)
	}

	if err := Save(Provider{ID: "codex", Key: "sk-1"}); err == nil {
		t.Error("a sign-in provider must not accept a key")
	}
	if err := Save(Provider{ID: "codex", Chat: "https://x.example/v1"}); err == nil {
		t.Error("a sign-in provider must not accept a URL")
	}

	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p = findByID(All(), "codex")
	if p == nil {
		t.Fatal("deleting picks must not remove a signed-in provider")
	}
	if len(p.Models) != 0 {
		t.Errorf("picks after delete = %v", p.Models)
	}
	if _, err := os.Stat(codexPath()); err != nil {
		t.Errorf("delete touched the agent's login file: %v", err)
	}
}

func TestLoggedOutIgnoresLeftoverPicks(t *testing.T) {
	sandbox(t)
	writeCodex(t, "dev@example.com", "plus")
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(codexPath()); err != nil {
		t.Fatal(err)
	}

	if findByID(All(), "codex") != nil {
		t.Error("signed-out codex still in All()")
	}
	if hasEntry(Catalog(), "codex/gpt-5.5") {
		t.Error("signed-out codex still in the catalog")
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Error("signed-out codex still resolves")
	}
}

func TestClaudeIsDetectedNeverProvided(t *testing.T) {
	sandbox(t)
	writeFile(t, filepath.Join(claudeDir(), ".credentials.json"), `{"claudeAiOauth":{"accessToken":"SECRET-TOKEN"}}`)
	// even a hand-edited config must not turn claude into a provider
	writeFile(t, Path(), `{"providers":[{"id":"claude","anthropic":"https://x.example","key":"k"}]}`)

	all := All()
	if findByID(all, "claude") != nil {
		t.Fatal("claude appeared in All()")
	}
	b, _ := json.Marshal(all)
	if strings.Contains(string(b), "SECRET") {
		t.Fatal("All() leaked Claude's credentials")
	}
	if hasEntry(Catalog(), "claude/x") {
		t.Error("claude in catalog")
	}
	if err := Save(Provider{ID: "claude", Anthropic: "https://api.anthropic.com", Key: "k"}); err == nil {
		t.Error("Save accepted claude")
	}
	if _, err := Find("claude"); err == nil {
		t.Error("Find returned claude")
	}

	var claude *Login
	for _, l := range Logins() {
		if l.Agent == "claude" {
			l := l
			claude = &l
		}
	}
	if claude == nil || !claude.Signed || claude.Provided || claude.Note == "" {
		t.Errorf("claude login = %+v", claude)
	}
	if strings.Contains(fmt.Sprint(Logins()), "SECRET") {
		t.Error("Logins() leaked Claude's credentials")
	}
}

func TestClaudeSignedOutHasNoNotice(t *testing.T) {
	sandbox(t)
	for _, l := range Logins() {
		if l.Agent == "claude" && (l.Signed || l.Provided) {
			t.Errorf("claude login = %+v", l)
		}
	}
}

func TestCopilotBuiltinExcludesAuto(t *testing.T) {
	sandbox(t)
	writeCopilot(t)

	p := findByID(All(), "copilot")
	if p == nil {
		t.Fatal("copilot not in All() while signed in")
	}
	if p.Account == nil || p.Account.User != "octo" || p.Account.Source != "copilot" {
		t.Fatalf("account = %+v", p.Account)
	}
	ms := p.Available()
	if len(ms) == 0 {
		t.Fatal("copilot built-in list is empty")
	}
	for _, m := range ms {
		if m.ID == "auto" {
			t.Error("built-in copilot list contains auto")
		}
	}
	if !hasEntry(Catalog(), "copilot/gpt-5.5") {
		t.Error("copilot/gpt-5.5 missing from the catalog")
	}
	if hasEntry(Catalog(), "copilot/auto") {
		t.Error("copilot/auto listed")
	}
}

func TestFilterCopilot(t *testing.T) {
	off := false
	on := true
	rows := []CopilotModel{
		{ID: "gpt-5.5", Name: "GPT-5.5", Capabilities: struct {
			Type string `json:"type"`
		}{Type: "chat"}},
		{ID: "auto", Capabilities: struct {
			Type string `json:"type"`
		}{Type: "chat"}},
		{ID: "embed-x", Capabilities: struct {
			Type string `json:"type"`
		}{Type: "embeddings"}},
		{ID: "internal-y", Internal: true, Capabilities: struct {
			Type string `json:"type"`
		}{Type: "chat"}},
		{ID: "disabled-z", ModelPickerEnabled: &off, Capabilities: struct {
			Type string `json:"type"`
		}{Type: "chat"}},
		{ID: "on-w", ModelPickerEnabled: &on, Capabilities: struct {
			Type string `json:"type"`
		}{Type: "chat"}},
	}
	got := FilterCopilot(rows)
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "gpt-5.5,on-w" {
		t.Errorf("FilterCopilot kept %v", ids)
	}
	if got[0].Name != "GPT-5.5" || got[1].Name != "on-w" {
		t.Errorf("names = %q, %q", got[0].Name, got[1].Name)
	}
}

func TestCopilotRefreshSavesFilteredLiveList(t *testing.T) {
	sandbox(t)
	writeCopilot(t)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			if r.Header.Get("Authorization") != "token gho_test" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"token":"sess-1","endpoints":{"api":%q}}`, srv.URL)
		case "/models":
			if r.Header.Get("Authorization") != "Bearer sess-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"data":[
				{"id":"gpt-5.5","name":"GPT-5.5","capabilities":{"type":"chat"}},
				{"id":"auto","name":"Auto","capabilities":{"type":"chat"}},
				{"id":"text-embedding-3","capabilities":{"type":"embeddings"}},
				{"id":"hidden-internal","internal":true,"capabilities":{"type":"chat"}},
				{"id":"off-model","model_picker_enabled":false,"capabilities":{"type":"chat"}},
				{"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","model_picker_enabled":true,"capabilities":{"type":"chat"}}
			]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	setString(t, &CopilotTokenURL, srv.URL+"/copilot_internal/v2/token")
	setString(t, &CopilotBase, srv.URL)

	ms, err := RefreshCopilot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "gpt-5.5,claude-sonnet-4.5" {
		t.Fatalf("refresh kept %v", ids)
	}

	p := findByID(All(), "copilot")
	if p == nil {
		t.Fatal("copilot missing after refresh")
	}
	if _, ok := p.Fetched(); !ok {
		t.Error("refreshed list was not saved")
	}
	avail := p.Available()
	if len(avail) != 2 {
		t.Errorf("Available after refresh = %d models, want 2", len(avail))
	}
	if !hasEntry(Catalog(), "copilot/claude-sonnet-4.5") {
		t.Error("refreshed model not in the catalog")
	}
}

func TestCopilotRevokedSessionAsksToSignInAgain(t *testing.T) {
	sandbox(t)
	writeCopilot(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	setString(t, &CopilotTokenURL, srv.URL+"/copilot_internal/v2/token")
	setString(t, &CopilotBase, srv.URL)

	_, err := RefreshCopilot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Fatalf("err = %v, want a sign-in-again message", err)
	}
}
