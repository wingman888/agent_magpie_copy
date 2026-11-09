package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/catalog"
)

// copEnv points HOME, XDG dirs and Claude's config dir at a fresh temp dir, so
// no real sign-in on this machine is seen, and returns the XDG config dir.
func copEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg-config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return xdg
}

// copWriteApps writes the Copilot apps.json under the XDG config dir.
func copWriteApps(t *testing.T, xdg, content string) {
	t.Helper()
	path := filepath.Join(xdg, "github-copilot", "apps.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// copFake plays GitHub's token endpoint and the Copilot API host.
type copFake struct {
	tokenStatus int    // status for the token exchange; 0 means 200
	models      string // body of /models
	api         string // API host the session names; set after the server starts
	modelsHead  http.Header
	tokenHead   http.Header
}

func (f *copFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/copilot_internal/v2/token":
		f.tokenHead = r.Header.Clone()
		if f.tokenStatus != 0 {
			w.WriteHeader(f.tokenStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"token":"sess-1","expires_at":4102444800,"endpoints":{"api":"`+f.api+`"}}`)
	case "/models":
		f.modelsHead = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, f.models)
	default:
		http.NotFound(w, r)
	}
}

// copServe starts the fake and points CopilotTokenURL at it.
func copServe(t *testing.T, f *copFake) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.api = srv.URL
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { CopilotTokenURL = old })
}

const copAppsOK = `{"github.com:Iv1":{"user":"octo","oauth_token":"gho_secret"},"other.host":{"user":"x","oauth_token":"y"}}`

func TestCopCredReadsGitHubLogin(t *testing.T) {
	xdg := copEnv(t)
	copWriteApps(t, xdg, copAppsOK)
	acc, ok := copilotSignedIn()
	if !ok || acc.User != "octo" || acc.Source != "GitHub Copilot" {
		t.Fatalf("signed in = %v, %+v", ok, acc)
	}
	p, ok := copFind("copilot")
	if !ok || p.Account == nil || p.Account.User != "octo" {
		t.Fatalf("copilot not listed: %v %+v", ok, p)
	}
	if p.Key != "" || !p.Ready() {
		t.Errorf("account provider needs no key and must be ready: key=%q", p.Key)
	}
}

func TestCopCredMissingOrNotGitHubIsSignedOut(t *testing.T) {
	xdg := copEnv(t)
	if _, ok := copilotSignedIn(); ok {
		t.Fatal("signed in with no apps.json")
	}
	copWriteApps(t, xdg, `{"other.host":{"user":"x","oauth_token":"y"}}`)
	if _, ok := copilotSignedIn(); ok {
		t.Fatal("a non-github.com entry counted as a sign-in")
	}
}

func TestCopFilterKeepsOnlyUsableChatModels(t *testing.T) {
	body := `{"data":[
		{"id":"gpt-5.5","name":"GPT-5.5","capabilities":{"type":"chat"},"policy":{"state":"enabled"}},
		{"id":"auto","name":"Auto","capabilities":{"type":"chat"}},
		{"id":"text-embedding-3-small","capabilities":{"type":"embeddings"}},
		{"id":"ada-002","capabilities":{"type":"embeddings"}},
		{"id":"internal-router","capabilities":{"type":"chat","internal":true}},
		{"id":"tagged-internal","internal":true,"capabilities":{"type":"chat"}},
		{"id":"disabled-model","capabilities":{"type":"chat"},"policy":{"state":"disabled"}},
		{"id":"unpicked","capabilities":{"type":"chat"},"model_picker_enabled":false},
		{"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","capabilities":{"type":"chat"},"policy":{"state":"enabled"}}
	]}`
	ms, err := FilterCopilotModels([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if got := strings.Join(ids, ","); got != "gpt-5.5,claude-sonnet-4.5" {
		t.Errorf("kept %s", got)
	}
}

// copFind returns the provider with the given id from All(), if listed.
func copFind(id string) (Provider, bool) {
	for _, p := range All() {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

func TestCopBuiltinListNonEmptyNoAuto(t *testing.T) {
	xdg := copEnv(t)
	copWriteApps(t, xdg, copAppsOK)
	ms := accountModels("copilot")
	if len(ms) == 0 {
		t.Fatal("built-in copilot list is empty")
	}
	for _, m := range ms {
		if m.ID == "auto" {
			t.Errorf("built-in list contains auto")
		}
	}
	p, ok := copFind("copilot")
	if !ok {
		t.Fatal("copilot not listed")
	}
	for _, m := range p.Available() {
		if m.ID == "auto" {
			t.Errorf("Available() contains auto")
		}
	}
}

func TestCopRefreshSavesLiveList(t *testing.T) {
	xdg := copEnv(t)
	copWriteApps(t, xdg, copAppsOK)
	f := &copFake{models: `{"data":[
		{"id":"gpt-5.5","name":"GPT-5.5","capabilities":{"type":"chat"},"policy":{"state":"enabled"}},
		{"id":"auto","capabilities":{"type":"chat"}},
		{"id":"text-embedding-3-small","capabilities":{"type":"embeddings"}}
	]}`}
	copServe(t, f)
	p, ok := copFind("copilot")
	if !ok {
		t.Fatal("copilot not listed")
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != "gpt-5.5" {
		t.Fatalf("refreshed list = %+v", ms)
	}
	if got := f.modelsHead.Get("Authorization"); got != "Bearer sess-1" {
		t.Errorf("models Authorization = %q, want the session token", got)
	}
	if got := f.tokenHead.Get("Authorization"); got != "token gho_secret" {
		t.Errorf("token exchange Authorization = %q", got)
	}
	live, _, ok := catalog.Live("copilot")
	if !ok || len(live) != 1 {
		t.Fatalf("live list not saved: %v %+v", ok, live)
	}
	after := accountModels("copilot")
	if len(after) != 1 || after[0].ID != "gpt-5.5" {
		t.Errorf("after refresh accountModels = %+v", after)
	}
}

func TestCopSessionRejectedSaysReSignIn(t *testing.T) {
	xdg := copEnv(t)
	copWriteApps(t, xdg, copAppsOK)
	copServe(t, &copFake{tokenStatus: http.StatusUnauthorized})
	_, _, err := CopilotSession(context.Background())
	if err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Fatalf("err = %v, want the re-sign-in message", err)
	}
	p, _ := copFind("copilot")
	if _, ferr := p.Fetch(context.Background()); ferr == nil || !strings.Contains(ferr.Error(), "请重新登录 Copilot") {
		t.Errorf("refresh err = %v, want the re-sign-in message", ferr)
	}
}

func TestCopSessionUsesAPIHostFromSession(t *testing.T) {
	xdg := copEnv(t)
	copWriteApps(t, xdg, copAppsOK)
	f := &copFake{}
	copServe(t, f)
	tok, api, err := CopilotSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "sess-1" || api != f.api {
		t.Errorf("session = %q %q, want sess-1 %q", tok, api, f.api)
	}
}
