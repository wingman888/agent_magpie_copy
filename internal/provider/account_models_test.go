package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// copilotStandIn serves the token exchange and a model list. It records the
// headers of the model-list request.
func copilotStandIn(t *testing.T, models string) (*httptest.Server, *http.Header) {
	t.Helper()
	var seen http.Header
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_x" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "sess-models",
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
			"endpoints":  map[string]string{"api": srv.URL + "/api"},
		})
	})
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		if r.Header.Get("Authorization") != "Bearer sess-models" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(models))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	setVar(t, &CopilotTokenURL, srv.URL+"/token")
	return srv, &seen
}

// copilotModelsFixture covers every rule: kept chat models (with and without
// reasoning levels, enabled through policy), and dropped ones.
const copilotModelsFixture = `{"data":[
 {"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}},"model_picker_enabled":true},
 {"id":"text-embedding-3","capabilities":{"type":"embeddings"},"model_picker_enabled":true},
 {"id":"copilot-search-x","capabilities":{"type":"chat"},"model_picker_enabled":true},
 {"id":"exec-agent-1","capabilities":{"type":"chat"},"model_picker_enabled":true},
 {"id":"trajectory-judge","capabilities":{"type":"chat"},"model_picker_enabled":true},
 {"id":"gpt-4.1-secondary","capabilities":{"type":"chat"},"model_picker_enabled":true},
 {"id":"gpt-4.1-tertiary","capabilities":{"type":"chat"},"model_picker_enabled":true},
 {"id":"gpt-4.1-4th","capabilities":{"type":"chat"},"model_picker_enabled":true},
 {"id":"gpt-5-mini-free-auto","capabilities":{"type":"chat"},"model_picker_enabled":true},
 {"id":"gpt-4o-old","capabilities":{"type":"chat"},"model_picker_enabled":false,"policy":{"state":"disabled"}},
 {"id":"gpt-5.5","name":"GPT-5.5","capabilities":{"type":"chat","supports":{"reasoning_effort":["medium","xhigh"]}},"model_picker_enabled":false,"policy":{"state":"enabled"}},
 {"id":"gemini-3.5-flash","capabilities":{"type":"chat"},"model_picker_enabled":true,"policy":{"state":"enabled"}},
 {"id":"claude-sonnet-4.5","capabilities":{"type":"chat"},"model_picker_enabled":true}
]}`

func TestBuiltinListHasNoCopilotAuto(t *testing.T) {
	for _, m := range accountSpecFor("copilot").builtin() {
		if m.ID == "auto" {
			t.Fatal("built-in Copilot list offers auto")
		}
	}
	isolate(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "hosts.json"),
		`{"github.com":{"user":"octo","oauth_token":"gho_x"}}`)
	p, ok := findAccount(Accounts(), "copilot")
	if !ok {
		t.Fatal("copilot missing")
	}
	if slices.Contains(p.Models, "auto") {
		t.Fatalf("default Copilot models include auto: %v", p.Models)
	}
	if !slices.Contains(p.Models, "claude-sonnet-4.5") {
		t.Fatalf("default Copilot models lack claude-sonnet-4.5: %v", p.Models)
	}
}

func TestRefreshCopilotKeepsOnlyUsableChatModels(t *testing.T) {
	isolate(t)
	resetCopilotCache()
	t.Cleanup(resetCopilotCache)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "hosts.json"),
		`{"github.com":{"user":"octo","oauth_token":"gho_x"}}`)
	_, seen := copilotStandIn(t, copilotModelsFixture)

	p, ok := findAccount(Accounts(), "copilot")
	if !ok {
		t.Fatal("copilot missing")
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if want := []string{"claude-sonnet-4.5", "gpt-5.5", "gemini-3.5-flash"}; !slices.Equal(ids, want) {
		t.Fatalf("refreshed models = %v, want %v", ids, want)
	}
	if got := ms[0].Efforts; !slices.Equal(got, []string{"low", "high"}) {
		t.Errorf("claude-sonnet-4.5 efforts = %v", got)
	}
	if got := ms[1].Efforts; !slices.Equal(got, []string{"medium", "xhigh"}) {
		t.Errorf("gpt-5.5 efforts = %v", got)
	}
	if got := (*seen).Get("Authorization"); got != "Bearer sess-models" {
		t.Errorf("model list Authorization = %q", got)
	}
	if (*seen).Get("Copilot-Integration-Id") == "" {
		t.Error("model list request lacks Copilot-Integration-Id")
	}

	// The refreshed list is what the account now offers, with its efforts.
	again, ok := findAccount(Accounts(), "copilot")
	if !ok {
		t.Fatal("copilot missing after refresh")
	}
	if !slices.Equal(again.Listed, ids) {
		t.Errorf("listed = %v, want %v", again.Listed, ids)
	}
	avail := again.Available()
	if len(avail) != 3 || !slices.Equal(avail[0].Efforts, []string{"low", "high"}) {
		t.Errorf("available after refresh = %+v", avail)
	}

	// dial's config holds the list and efforts, never the session or the token.
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if s := string(raw); strings.Contains(s, "gho_x") || strings.Contains(s, "sess-models") {
		t.Fatalf("config holds a credential:\n%s", s)
	}
}

func TestRefreshCopilotWithoutChatModelIsAnError(t *testing.T) {
	isolate(t)
	resetCopilotCache()
	t.Cleanup(resetCopilotCache)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "hosts.json"),
		`{"github.com":{"user":"octo","oauth_token":"gho_x"}}`)
	copilotStandIn(t, `{"data":[{"id":"text-embedding-3","capabilities":{"type":"embeddings"},"model_picker_enabled":true}]}`)

	p, _ := findAccount(Accounts(), "copilot")
	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("refresh with no chat model succeeded")
	}
}

func TestCodexRefreshUsesBuiltInListWithoutNetwork(t *testing.T) {
	home := isolate(t)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"),
		codexAuthJSON(t, "chatgpt", codexIDToken(t, "me@example.com", "pro")))
	p, _ := findAccount(Accounts(), "codex")
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(ms, func(m catalog.Model) bool { return m.ID == "gpt-5.5" }) {
		t.Fatalf("codex refresh lacks gpt-5.5: %+v", ms)
	}
}

func TestAccountReferencesResolveThroughCatalog(t *testing.T) {
	home := isolate(t)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"),
		codexAuthJSON(t, "chatgpt", codexIDToken(t, "me@example.com", "pro")))
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "hosts.json"),
		`{"github.com":{"user":"octo","oauth_token":"gho_x"}}`)

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
		t.Error("catalog offers copilot/auto")
	}

	p, model, ok := Resolve("codex/gpt-5.5")
	if !ok || p.ID != "codex" || model != "gpt-5.5" {
		t.Fatalf("Resolve(codex/gpt-5.5) = %q, %q, %v", p.ID, model, ok)
	}
	if p.Owner() != "me@example.com" {
		t.Errorf("owner = %q", p.Owner())
	}
}

func TestSaveAccountKeepsOnlySelection(t *testing.T) {
	home := isolate(t)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"),
		codexAuthJSON(t, "chatgpt", codexIDToken(t, "me@example.com", "pro")))

	if err := Save(Provider{ID: "codex", Chat: "https://evil.example/v1", Key: "sk-secret-789",
		Name: "Mine", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if s := string(raw); strings.Contains(s, "evil") || strings.Contains(s, "sk-secret") || strings.Contains(s, "me@example.com") {
		t.Fatalf("config holds more than the selection:\n%s", s)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(raw, &f); err != nil || len(f.Providers) != 1 {
		t.Fatalf("config = %s (%v)", raw, err)
	}
	for k := range f.Providers[0] {
		if k != "id" && k != "models" {
			t.Errorf("account record has field %q", k)
		}
	}

	p, ok := findAccount(All(), "codex")
	if !ok || !p.Ready() {
		t.Fatal("codex not a ready provider after saving its selection")
	}
	if !slices.Equal(p.Models, []string{"gpt-5.5"}) || p.Chat != "" || p.Key != "" {
		t.Fatalf("codex = %+v, want only the selection", p)
	}
}

func TestSaveAccountWithoutLoginIsRefused(t *testing.T) {
	isolate(t)
	if err := Save(Provider{ID: "copilot", Models: []string{"gpt-5.5"}}); err == nil {
		t.Fatal("selection saved for a provider that is not signed in")
	}
}

func TestDeleteAccountClearsSelectionButKeepsProvider(t *testing.T) {
	home := isolate(t)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"),
		codexAuthJSON(t, "chatgpt", codexIDToken(t, "me@example.com", "pro")))
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}

	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatalf("codex gone after delete: %v", err)
	}
	if len(p.Models) == 0 || slices.Equal(p.Models, []string{"gpt-5.5"}) {
		t.Fatalf("selection not cleared: %v", p.Models)
	}
	raw, _ := os.ReadFile(Path())
	if strings.Contains(string(raw), "gpt-5.5") {
		t.Fatalf("stored selection survived delete:\n%s", raw)
	}
}

func TestLogoutLeavesNoProviderOrCatalogEntry(t *testing.T) {
	home := isolate(t)
	authPath := filepath.Join(home, ".codex", "auth.json")
	writeFile(t, authPath, codexAuthJSON(t, "chatgpt", codexIDToken(t, "me@example.com", "pro")))
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(authPath); err != nil {
		t.Fatal(err)
	}
	if _, ok := findAccount(All(), "codex"); ok {
		t.Fatal("codex provider survives logout with a leftover selection")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Fatalf("catalog still lists %s after logout", e.ID)
		}
	}
	// The leftover record is cleared, not an error; it stays inert.
	if err := Delete("codex"); err != nil {
		t.Errorf("delete of a leftover record: %v", err)
	}
}

func TestSelectionOfExcludedAccountDoesNotBecomeProvider(t *testing.T) {
	isolate(t)
	writeFile(t, Path(), `{"providers":[{"id":"copilot","models":["gpt-5.5"]}]}`)
	for _, p := range All() {
		if p.ID == "copilot" {
			t.Fatal("copilot provider from a leftover selection")
		}
	}
}
