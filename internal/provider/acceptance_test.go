package provider

import (
	"context"
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

// Acceptance checks V6 (provider view), V8, V9 and V10 at the provider level.

func accRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func accModelIDs(ms []catalog.Model) string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return strings.Join(out, ",")
}

// V6: a ChatGPT login is a codex provider, signed in as its email, with no
// key needed.
func TestAcceptV6CodexSignedInAsEmail(t *testing.T) {
	home := setupAgents(t)
	writeCodexLogin(t, home, "chatgpt")
	cp := findID(All(), "codex")
	if cp == nil || cp.Account == nil || cp.Account.User != "dev@example.com" || !cp.Ready() {
		t.Fatalf("codex provider: %+v", cp)
	}
	if cp.Preset != "" || cp.Key != "" {
		t.Errorf("account provider carries a key or preset: %+v", cp)
	}
}

// V8: copilot is listed when signed in; after a refresh only the account's
// usable chat models remain, with no auto, internal, disabled or embedding
// models.
func TestAcceptV8CopilotLiveListFiltered(t *testing.T) {
	setupAgents(t)
	writeCopilotLogin(t)
	const models = `{"data":[
	 {"id":"gpt-4.1","name":"GPT-4.1","capabilities":{"type":"chat"},"model_picker_enabled":true,"policy":{"state":"enabled"}},
	 {"id":"auto","name":"Auto","capabilities":{"type":"chat"},"model_picker_enabled":true},
	 {"id":"copilot-internal-router","name":"Router","capabilities":{"type":"chat"},"model_picker_enabled":true},
	 {"id":"gpt-4o-mini-off","name":"Off","capabilities":{"type":"chat"},"model_picker_enabled":false},
	 {"id":"text-embedding-3-small","name":"Embed","capabilities":{"type":"embeddings"},"model_picker_enabled":true},
	 {"id":"policy-off","name":"Policy off","capabilities":{"type":"chat"},"model_picker_enabled":true,"policy":{"state":"unconfigured"}},
	 {"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","capabilities":{"type":"chat"},"model_picker_enabled":true,"policy":{"state":"enabled"}}
	]}`
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer sess-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, models)
	}))
	t.Cleanup(api.Close)
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"token":"sess-1","expires_at":%d,"endpoints":{"api":%q}}`, time.Now().Add(time.Hour).Unix(), api.URL)
	}))
	t.Cleanup(tok.Close)
	old := CopilotTokenURL
	CopilotTokenURL = tok.URL
	t.Cleanup(func() { CopilotTokenURL = old })

	cp := findID(All(), "copilot")
	if cp == nil || cp.Account == nil || cp.Account.User != "octo" {
		t.Fatalf("copilot not listed as signed in: %+v", cp)
	}
	for _, m := range cp.Available() {
		if m.ID == "auto" {
			t.Errorf("built-in list offers auto")
		}
	}
	ms, err := cp.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	const want = "gpt-4.1,claude-sonnet-4.5"
	if got := accModelIDs(ms); got != want {
		t.Errorf("refreshed list %q, want %q", got, want)
	}
	p, err := Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	if got := accModelIDs(p.Available()); got != want {
		t.Errorf("available after refresh %q, want %q", got, want)
	}
	seen := map[string]bool{}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "copilot/") {
			seen[e.ID] = true
		}
	}
	if !seen["copilot/gpt-4.1"] || seen["copilot/auto"] || seen["copilot/copilot-internal-router"] ||
		seen["copilot/gpt-4o-mini-off"] || seen["copilot/text-embedding-3-small"] || seen["copilot/policy-off"] {
		t.Errorf("catalog copilot models: %v", seen)
	}
}

// V9: saving or deleting an account provider only changes the picks. Login
// files stay byte-identical, and signing out removes the provider while a
// leftover pick does not come back as one.
func TestAcceptV9AccountSaveDeleteTouchesOnlyPicks(t *testing.T) {
	home := setupAgents(t)
	writeCodexLogin(t, home, "chatgpt")
	writeCopilotLogin(t)
	authPath := filepath.Join(home, ".codex", "auth.json")
	appsPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json")
	before := map[string]string{authPath: accRead(t, authPath), appsPath: accRead(t, appsPath)}

	if err := Save(Provider{ID: "codex", Name: "Codex", Key: "LEAK-KEY", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "copilot", Name: "GitHub Copilot", Key: "LEAK-KEY", Models: []string{"gpt-4.1"}}); err != nil {
		t.Fatal(err)
	}
	cfg := accRead(t, Path())
	for _, bad := range []string{"LEAK-KEY", "gho_secret", "access_token", "refresh_token", "GitHub Copilot"} {
		if strings.Contains(cfg, bad) {
			t.Errorf("config file holds %q: %s", bad, cfg)
		}
	}
	if p := findID(All(), "codex"); p == nil || accModelIDs(p.Exposed()) != "gpt-5.5" {
		t.Errorf("codex picks not applied: %+v", p)
	}

	if err := Delete("copilot"); err != nil {
		t.Fatal(err)
	}
	if cp := findID(All(), "copilot"); cp == nil || len(cp.Models) != 0 {
		t.Errorf("copilot should stay listed with its picks cleared: %+v", cp)
	}
	for path, want := range before {
		if got := accRead(t, path); got != want {
			t.Errorf("%s changed by save/delete", path)
		}
	}

	// sign out of Codex: the provider goes, and its leftover pick is ignored
	if err := os.Remove(authPath); err != nil {
		t.Fatal(err)
	}
	if findID(All(), "codex") != nil {
		t.Errorf("codex still listed after sign-out")
	}
	if _, err := Find("codex"); err == nil {
		t.Errorf("codex still found after sign-out")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Errorf("leftover codex pick became a model: %s", e.ID)
		}
	}
	if cp := findID(All(), "copilot"); cp == nil {
		t.Errorf("copilot disappeared when codex signed out")
	}
}

// V10: a signed-in Claude Code is only noticed, never offered, and its
// credential content is never shown.
func TestAcceptV10ClaudeNoticeNotProvider(t *testing.T) {
	home := setupAgents(t)
	claudeDir := filepath.Join(home, "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	credPath := filepath.Join(claudeDir, ".credentials.json")
	writeFile(t, credPath, `{"claudeAiOauth":{"accessToken":"SECRET-CLAUDE-TOKEN"}}`)

	var note *Notice
	for _, n := range Notices() {
		if n.ID == "claude" {
			n := n
			note = &n
		}
	}
	if note == nil || !strings.Contains(note.Reason, "已登录但不提供") || !strings.Contains(note.Reason, "凭据") {
		t.Fatalf("no explanation for signed-in Claude Code: %+v", note)
	}
	if strings.Contains(note.Reason, "SECRET") {
		t.Errorf("notice shows credential content")
	}
	if findID(All(), "claude") != nil {
		t.Errorf("claude offered as a provider")
	}
	if _, err := Find("claude"); err == nil {
		t.Errorf("claude found as a provider")
	}
	if err := Save(Provider{ID: "claude", Name: "Claude", Key: "k", Anthropic: "https://api.anthropic.com"}); err == nil {
		t.Errorf("claude accepted as a provider")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "claude") {
			t.Errorf("claude model in catalog: %s", e.ID)
		}
	}
	b, _ := json.Marshal(All())
	if strings.Contains(string(b), "SECRET") {
		t.Errorf("provider list shows claude credentials: %s", b)
	}

	// without the login file there is nothing to say
	os.Remove(credPath)
	for _, n := range Notices() {
		if n.ID == "claude" {
			t.Errorf("notice without a Claude login: %+v", n)
		}
	}
}
