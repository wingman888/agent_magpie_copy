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
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// zvJWT builds an unsigned token whose payload is claims; dial only reads payloads.
func zvJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// zvHome points HOME, the dial config and cache, and Claude's config at a fresh temp dir.
func zvHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(h, ".claude"))
	return h
}

func zvWriteFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// zvCodexAuth writes a ChatGPT login whose access token expires at exp.
func zvCodexAuth(t *testing.T, h string, exp time.Time) {
	t.Helper()
	access := zvJWT(t, map[string]any{"exp": exp.Unix()})
	idt := zvJWT(t, map[string]any{
		"email":                        "me@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
	})
	b, _ := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{"id_token": idt, "access_token": access,
			"refresh_token": "rt-old", "account_id": "acct-1"},
		"last_refresh": "2026-01-01T00:00:00Z",
		"custom":       "keep",
	})
	zvWriteFile(t, filepath.Join(h, ".codex", "auth.json"), string(b))
}

func zvFindID(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func zvModelIDs(ms []catalog.Model) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func TestZv_CodexSignedInShows(t *testing.T) {
	h := zvHome(t)
	zvCodexAuth(t, h, time.Now().Add(time.Hour))
	p := zvFindID(All(), "codex")
	if p == nil {
		t.Fatal("codex not listed while signed in")
	}
	if p.Account == nil || p.Account.User != "me@example.com" || p.Account.Plan != "plus" || p.Account.Source != "Codex" {
		t.Fatalf("account = %+v", p.Account)
	}
	if !p.Ready() || p.Key != "" {
		t.Fatalf("account provider should be ready without a key")
	}
	if p.Responses != CodexBase {
		t.Fatalf("responses base = %q, want %q", p.Responses, CodexBase)
	}
	if !p.Chosen("gpt-5.5") {
		t.Fatalf("gpt-5.5 should be offered by default, got %v", zvModelIDs(p.Available()))
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); !ok {
		t.Fatal("codex/gpt-5.5 does not resolve")
	}
}

func TestZv_APIKeyCodexIsNotAccount(t *testing.T) {
	h := zvHome(t)
	zvWriteFile(t, filepath.Join(h, ".codex", "auth.json"), `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-x"}`)
	if zvFindID(All(), "codex") != nil {
		t.Fatal("an API-key Codex login must not be an account provider")
	}
}

func TestZv_CodexRefreshWritesBack(t *testing.T) {
	h := zvHome(t)
	zvCodexAuth(t, h, time.Now().Add(-time.Hour))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path != "/oauth/token" || in["grant_type"] != "refresh_token" || in["refresh_token"] != "rt-old" {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": "at-new", "refresh_token": "rt-new"})
	}))
	defer srv.Close()
	old := CodexAuthBase
	CodexAuthBase = srv.URL
	t.Cleanup(func() { CodexAuthBase = old })

	tok, acct, err := codexToken(context.Background())
	if err != nil || tok != "at-new" || acct != "acct-1" {
		t.Fatalf("codexToken = %q, %q, %v", tok, acct, err)
	}
	b, _ := os.ReadFile(filepath.Join(h, ".codex", "auth.json"))
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["custom"] != "keep" || m["auth_mode"] != "chatgpt" {
		t.Fatalf("unrelated fields lost: %s", b)
	}
	if _, ok := m["OPENAI_API_KEY"]; !ok {
		t.Fatalf("OPENAI_API_KEY dropped: %s", b)
	}
	toks, _ := m["tokens"].(map[string]any)
	if toks["access_token"] != "at-new" || toks["refresh_token"] != "rt-new" || toks["account_id"] != "acct-1" || toks["id_token"] == nil {
		t.Fatalf("tokens not written back correctly: %v", toks)
	}
}

func TestZv_CodexRefreshFailureSaysLoggedOut(t *testing.T) {
	h := zvHome(t)
	zvCodexAuth(t, h, time.Now().Add(-time.Hour))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	old := CodexAuthBase
	CodexAuthBase = srv.URL
	t.Cleanup(func() { CodexAuthBase = old })

	_, _, err := codexToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "codex login") {
		t.Fatalf("err = %v, want a message telling the user to run codex login", err)
	}
}

func TestZv_CopilotListFilteredAndSaved(t *testing.T) {
	h := zvHome(t)
	zvWriteFile(t, filepath.Join(h, "config", "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_test"}}`)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			if r.Header.Get("Authorization") != "token gho_test" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"token": "sess-1", "expires_at": time.Now().Add(time.Hour).Unix(),
				"endpoints": map[string]string{"api": srv.URL}})
		case "/models":
			if r.Header.Get("Authorization") != "Bearer sess-1" {
				w.WriteHeader(401)
				return
			}
			io.WriteString(w, `{"data":[
				{"id":"gpt-4.1","name":"GPT-4.1","capabilities":{"type":"chat"},"model_picker_enabled":true,"policy":{"state":"enabled"}},
				{"id":"auto","capabilities":{"type":"chat"},"model_picker_enabled":true},
				{"id":"internal-x","capabilities":{"type":"chat"},"model_picker_enabled":false},
				{"id":"off-model","capabilities":{"type":"chat"},"model_picker_enabled":true,"policy":{"state":"disabled"}},
				{"id":"text-embedding-3-small","capabilities":{"type":"embeddings"},"model_picker_enabled":true},
				{"id":"claude-sonnet-4.5","capabilities":{"type":"chat"},"model_picker_enabled":true,"policy":{"state":"enabled"}}
			]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { CopilotTokenURL = old })

	p := zvFindID(All(), "copilot")
	if p == nil || p.Account == nil || p.Account.User != "octo" {
		t.Fatalf("copilot not listed as octo: %+v", p)
	}
	for _, id := range zvModelIDs(p.Available()) {
		if id == "auto" {
			t.Fatal("built-in Copilot list must not contain auto")
		}
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(zvModelIDs(ms), ",")
	if got != "gpt-4.1,claude-sonnet-4.5" {
		t.Fatalf("filtered list = %s", got)
	}
	p = zvFindID(All(), "copilot")
	if got := strings.Join(zvModelIDs(p.Available()), ","); got != "gpt-4.1,claude-sonnet-4.5" {
		t.Fatalf("saved live list = %s", got)
	}
}

func TestZv_SaveDeleteAccountOnlyKeepsPicks(t *testing.T) {
	h := zvHome(t)
	zvCodexAuth(t, h, time.Now().Add(time.Hour))
	if err := Save(Provider{ID: "codex", Name: "whatever", Key: "sk-should-not-be-kept", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(Path())
	s := string(raw)
	if strings.Contains(s, "sk-should-not-be-kept") || strings.Contains(s, "whatever") || strings.Contains(s, "rt-old") ||
		strings.Contains(s, "access_token") || !strings.Contains(s, "gpt-5.5") {
		t.Fatalf("config file holds more than the picks:\n%s", s)
	}
	p := zvFindID(All(), "codex")
	if p == nil || len(p.Models) != 1 || p.Key != "" {
		t.Fatalf("after save: %+v", p)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p = zvFindID(All(), "codex")
	if p == nil || len(p.Models) != 0 {
		t.Fatalf("delete should only clear picks and keep the signed-in provider: %+v", p)
	}
	if _, err := os.Stat(filepath.Join(h, ".codex", "auth.json")); err != nil {
		t.Fatal("delete must not touch the agent's login file")
	}
}

func TestZv_LogoutHidesAccount(t *testing.T) {
	h := zvHome(t)
	zvCodexAuth(t, h, time.Now().Add(time.Hour))
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h, ".codex", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if zvFindID(All(), "codex") != nil {
		t.Fatal("codex still listed after logout")
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Fatal("codex/gpt-5.5 still resolves after logout")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Fatalf("catalog still has %s", e.ID)
		}
	}
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err == nil {
		t.Fatal("saving picks for a signed-out account should fail")
	}
}

func TestZv_ClaudeNeverOffered(t *testing.T) {
	h := zvHome(t)
	zvWriteFile(t, filepath.Join(h, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"secret-token"}}`)
	ps := All()
	if zvFindID(ps, "claude") != nil {
		t.Fatal("claude must never be a provider")
	}
	b, _ := json.Marshal(ps)
	if strings.Contains(string(b), "secret-token") {
		t.Fatal("provider list leaks Claude's token")
	}
	if _, err := Find("claude"); err == nil {
		t.Fatal("Find(claude) should fail")
	}
	if notes := Notes(); len(notes) != 1 || !strings.Contains(notes[0], "不作为 provider 提供") {
		t.Fatalf("notes = %v", notes)
	}
	if err := Save(Provider{ID: "claude", Models: []string{"x"}}); err == nil {
		t.Fatal("saving claude should fail")
	}
}
