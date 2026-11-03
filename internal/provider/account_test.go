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
	"reflect"
	"strings"
	"testing"
	"time"
)

// isolate points HOME, dial's config and cache, and Claude's config dir at a
// fresh temp tree, so nothing on the test machine leaks into the results.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	old := CodexBase
	t.Cleanup(func() { CodexBase = old })
	oldTok := CopilotTokenURL
	t.Cleanup(func() { CopilotTokenURL = oldTok })
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

// fakeJWT is an unsigned JWT with the given payload; dial only reads claims.
func fakeJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// writeCodex writes Codex's login file in the given auth mode.
func writeCodex(t *testing.T, home, mode, email, plan string, exp int64) string {
	t.Helper()
	path := filepath.Join(home, ".codex", "auth.json")
	id := fakeJWT(map[string]any{
		"email":                       email,
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": plan},
	})
	acc := fakeJWT(map[string]any{"exp": exp})
	body, _ := json.Marshal(map[string]any{
		"auth_mode": mode,
		"tokens": map[string]any{
			"id_token": id, "access_token": acc, "refresh_token": "rt-secret", "account_id": "acct-1",
		},
	})
	writeFile(t, path, string(body))
	return path
}

// writeCopilot writes the GitHub sign-in Copilot keeps in apps.json.
func writeCopilot(t *testing.T, xdg, user, token string) {
	t.Helper()
	body := `{"github.com:1":{"user":"` + user + `","oauth_token":"` + token + `"},"other.example:2":{"user":"nobody","oauth_token":"x"}}`
	writeFile(t, filepath.Join(xdg, "github-copilot", "apps.json"), body)
}

func ids(ms []Provider) []string {
	var out []string
	for _, p := range ms {
		out = append(out, p.ID)
	}
	return out
}

func find(t *testing.T, ps []Provider, id string) Provider {
	t.Helper()
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("provider %q not in %v", id, ids(ps))
	return Provider{}
}

func TestCodexEmailFromIDToken(t *testing.T) {
	home := isolate(t)
	exp := time.Now().Add(time.Hour).Unix()
	writeCodex(t, home, "chatgpt", "me@example.com", "plus", exp)
	CodexBase = "http://127.0.0.1:9/backend-api/codex"

	p := find(t, All(), codexID)
	if p.Account == nil || p.Account.User != "me@example.com" || p.Account.Plan != "plus" || p.Account.Source != "Codex" {
		t.Fatalf("account = %+v", p.Account)
	}
	if p.Responses != CodexBase {
		t.Errorf("responses base = %q, want CodexBase", p.Responses)
	}
	if p.Key != "" || !p.Ready() {
		t.Errorf("codex should be ready without a key: key=%q ready=%v", p.Key, p.Ready())
	}
	l, ok := ReadCodex()
	if !ok || l.Expires.Unix() != exp {
		t.Errorf("expires = %v (ok=%v), want %d", l.Expires, ok, exp)
	}
	if !strings.Contains(p.Name, "Codex") {
		t.Errorf("name = %q", p.Name)
	}
	// the default list includes gpt-5.5 even with no pick and no refresh
	found := false
	for _, e := range Catalog() {
		if e.ID == "codex/gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Errorf("codex/gpt-5.5 missing from the catalog: %v", IDs())
	}
}

func TestAPIKeyModeIsNotAnAccount(t *testing.T) {
	home := isolate(t)
	writeCodex(t, home, "apikey", "me@example.com", "plus", time.Now().Add(time.Hour).Unix())
	for _, p := range All() {
		if p.ID == codexID {
			t.Fatalf("apikey-mode Codex listed as a provider: %+v", p)
		}
	}
	if _, err := Find("codex"); err == nil {
		t.Error("Find(codex) should fail in apikey mode")
	}
}

func TestCopilotLoginAndModelFilter(t *testing.T) {
	isolate(t)
	writeCopilot(t, os.Getenv("XDG_CONFIG_HOME"), "octocat", "gho_secret")

	var tokenAuth, modelsAuth string
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		tokenAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"token": "sess-1", "endpoints": map[string]string{"api": srv.URL}})
	})
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		modelsAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":[
			{"id":"gpt-5.5","name":"GPT-5.5","model_picker_enabled":true,"capabilities":{"type":"chat"},"policy":{"state":"enabled"}},
			{"id":"auto","name":"Auto","model_picker_enabled":true,"capabilities":{"type":"chat"}},
			{"id":"text-embedding-3","name":"Embed","model_picker_enabled":true,"capabilities":{"type":"embeddings"}},
			{"id":"internal-router","name":"Router","model_picker_enabled":false,"capabilities":{"type":"chat"}},
			{"id":"disabled-model","name":"Off","model_picker_enabled":true,"capabilities":{"type":"chat"},"policy":{"state":"disabled"}},
			{"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","model_picker_enabled":true,"capabilities":{"type":"chat"},"policy":{"state":"enabled"}}
		]}`))
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()
	CopilotTokenURL = srv.URL + "/token"

	p := find(t, All(), copilotID)
	if p.Account == nil || p.Account.User != "octocat" || p.Account.Source != "Copilot CLI" {
		t.Fatalf("account = %+v", p.Account)
	}
	if !p.Ready() {
		t.Error("copilot should be ready without a key")
	}
	// before any refresh: the built-in list, never auto
	for _, m := range p.Available() {
		if m.ID == "auto" {
			t.Error("built-in copilot list contains auto")
		}
	}
	if len(p.Available()) == 0 {
		t.Error("built-in copilot list is empty")
	}

	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tokenAuth != "token gho_secret" || modelsAuth != "Bearer sess-1" {
		t.Errorf("auth headers: token=%q models=%q", tokenAuth, modelsAuth)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.ID)
	}
	if want := []string{"gpt-5.5", "claude-sonnet-4.5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("filtered = %v, want %v", got, want)
	}
	// the refreshed list is what the account offers from now on
	p = find(t, All(), copilotID)
	if n := len(p.Available()); n != 2 {
		t.Errorf("after refresh Available has %d models, want 2", n)
	}
	var catIDs []string
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "copilot/") {
			catIDs = append(catIDs, e.ID)
		}
	}
	if want := []string{"copilot/gpt-5.5", "copilot/claude-sonnet-4.5"}; !reflect.DeepEqual(catIDs, want) {
		t.Errorf("catalog copilot ids = %v, want %v", catIDs, want)
	}
}

func TestCopilotSessionRejectionAsksForLogin(t *testing.T) {
	isolate(t)
	writeCopilot(t, os.Getenv("XDG_CONFIG_HOME"), "octocat", "gho_old")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	CopilotTokenURL = srv.URL
	_, _, err := RefreshCopilot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Fatalf("err = %v, want a sign-in prompt", err)
	}
}

func TestCopilotHomeFallback(t *testing.T) {
	home := isolate(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	writeCopilot(t, filepath.Join(home, ".config"), "fallback-user", "gho_x")
	p := find(t, All(), copilotID)
	if p.Account.User != "fallback-user" {
		t.Errorf("user = %q", p.Account.User)
	}
}

func TestStaleAccountPicksIgnored(t *testing.T) {
	home := isolate(t)
	writeFile(t, Path(), `{"providers":[{"id":"codex","models":["gpt-5.5"]},{"id":"copilot","models":["gpt-5.5"]}]}`)
	for _, p := range All() {
		if IsAccount(p.ID) {
			t.Fatalf("signed-out account listed: %s", p.ID)
		}
	}
	// signing in brings the picks back, and they are the only thing stored
	writeCodex(t, home, "chatgpt", "me@example.com", "plus", time.Now().Add(time.Hour).Unix())
	if got := find(t, All(), codexID).Models; !reflect.DeepEqual(got, []string{"gpt-5.5"}) {
		t.Errorf("picks = %v", got)
	}
}

func TestSaveAccountWritesOnlyModels(t *testing.T) {
	home := isolate(t)
	writeCodex(t, home, "chatgpt", "me@example.com", "plus", time.Now().Add(time.Hour).Unix())
	// a key, a name or a base URL has no place on an account: Save ignores them and keeps only the picks
	err := Save(Provider{ID: codexID, Name: "Codex", Key: "sk-secret", Chat: "https://x.example/v1", Models: []string{"gpt-5.5", "gpt-5.5", " "}})
	if err != nil {
		t.Fatalf("Save with a key/URL should succeed and ignore them: %v", err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "rt-secret") || strings.Contains(string(raw), "sk-secret") {
		t.Fatalf("secret written to providers.json: %s", raw)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(raw, &f); err != nil || len(f.Providers) != 1 {
		t.Fatalf("providers.json = %s (%v)", raw, err)
	}
	entry := f.Providers[0]
	if len(entry) != 2 || entry["id"] != codexID {
		t.Errorf("stored entry = %v, want only id and models", entry)
	}
	if got := find(t, All(), codexID); !reflect.DeepEqual(got.Models, []string{"gpt-5.5"}) || got.Key != "" {
		t.Errorf("after save: models=%v key=%q", got.Models, got.Key)
	}
	// a provider read back with Find carries the sign-in's own base URL; saving
	// it again with new picks is the normal "dial provider models" path
	found := find(t, All(), codexID)
	found.Models = []string{"gpt-5.5"}
	if err := Save(found); err != nil {
		t.Errorf("re-saving a found account with picks: %v", err)
	}
}

// An account with no picks offers everything its sign-in can use, even when
// that is more than the picker would show for an ordinary vendor.
func TestAccountWithoutPicksIsNeverCapped(t *testing.T) {
	home := isolate(t)
	writeCodex(t, home, "chatgpt", "me@example.com", "plus", time.Now().Add(time.Hour).Unix())
	var models []map[string]any
	for i := 0; i < manyModels+6; i++ {
		models = append(models, map[string]any{"slug": fmt.Sprintf("model-%02d", i), "display_name": "M", "priority": i})
	}
	b, _ := json.Marshal(map[string]any{"models": models})
	writeFile(t, filepath.Join(home, ".codex", "models_cache.json"), string(b))

	p := find(t, All(), codexID)
	if n, want := len(p.Exposed()), len(p.Available()); n != want || n <= manyModels {
		t.Fatalf("exposed %d of %d available; an account with no picks should expose all of them", n, want)
	}
	// with picks, only the picks are exposed
	if err := Save(Provider{ID: codexID, Models: []string{"model-00"}}); err != nil {
		t.Fatal(err)
	}
	if got := find(t, All(), codexID).Exposed(); len(got) != 1 || got[0].ID != "model-00" {
		t.Errorf("picked exposure = %v", got)
	}
}

func TestDeleteAccountClearsPicksOnly(t *testing.T) {
	home := isolate(t)
	authPath := writeCodex(t, home, "chatgpt", "me@example.com", "plus", time.Now().Add(time.Hour).Unix())
	before, _ := os.ReadFile(authPath)
	if err := Save(Provider{ID: codexID, Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete(codexID); err != nil {
		t.Fatal(err)
	}
	p := find(t, All(), codexID)
	if len(p.Models) != 0 {
		t.Errorf("picks survived delete: %v", p.Models)
	}
	after, _ := os.ReadFile(authPath)
	if string(before) != string(after) {
		t.Error("delete touched the Codex login file")
	}
	// deleting again, or when never saved, is not an error for an account
	if err := Delete(codexID); err != nil {
		t.Errorf("second delete: %v", err)
	}
}

func TestClaudeNeverAProvider(t *testing.T) {
	home := isolate(t)
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"SECRET-TOKEN"}}`)
	// a stale custom entry called claude must not come back either
	writeFile(t, Path(), `{"providers":[{"id":"claude","name":"Claude","key":"k","anthropic":"https://api.anthropic.com"}]}`)

	for _, p := range All() {
		if p.ID == claudeID {
			t.Fatalf("claude listed as a provider")
		}
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "claude/") {
			t.Fatalf("claude model in catalog: %s", e.ID)
		}
	}
	if err := Save(Provider{ID: claudeID, Name: "Claude", Key: "k", Anthropic: "https://api.anthropic.com"}); err == nil {
		t.Error("Save(claude) should fail")
	}
	b, _ := json.Marshal(All())
	if strings.Contains(string(b), "SECRET-TOKEN") {
		t.Error("provider list leaks the Claude token")
	}
}

func TestClaudeNoteWhenSignedIn(t *testing.T) {
	home := isolate(t)
	if n := Notes(); len(n) != 0 {
		t.Fatalf("notes without a sign-in: %v", n)
	}
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{}`)
	n := Notes()
	if len(n) != 1 || n[0].Name != "Claude Code" || !strings.Contains(n[0].Note, "已登录但不提供") {
		t.Fatalf("notes = %+v", n)
	}
	if strings.Contains(n[0].Note, "\n") || !strings.Contains(n[0].Note, "令牌") {
		t.Errorf("note must be one line and give the reason: %q", n[0].Note)
	}
	// the note is shown through the API, so it must not name Claude as a provider id
	b, _ := json.Marshal(n)
	if strings.Contains(string(b), `"id"`) || strings.Contains(string(b), `"claude"`) {
		t.Errorf("note JSON carries a provider id: %s", b)
	}
}

func TestCopilotTurnHeaders(t *testing.T) {
	cases := []struct {
		toolReturn, image bool
		want              map[string]string
	}{
		{false, false, map[string]string{"X-Initiator": "user"}},
		{true, false, map[string]string{"X-Initiator": "agent"}},
		{false, true, map[string]string{"X-Initiator": "user", "Copilot-Vision-Request": "true"}},
		{true, true, map[string]string{"X-Initiator": "agent", "Copilot-Vision-Request": "true"}},
	}
	for _, c := range cases {
		if got := CopilotHeaders(c.toolReturn, c.image); !reflect.DeepEqual(got, c.want) {
			t.Errorf("CopilotHeaders(%v,%v) = %v, want %v", c.toolReturn, c.image, got, c.want)
		}
	}
}
