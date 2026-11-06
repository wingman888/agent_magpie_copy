package provider

import (
	"context"
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

	"github.com/yetone/dial/internal/catalog"
)

// cplHomeEnv gives the test a private HOME and XDG config dir.
func cplHomeEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

// cplPutFile writes a fixture file, creating its directory.
func cplPutFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// cplFindProvider returns the provider with the given id, or nil.
func cplFindProvider(ps []Provider, id string) *Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

// cplRecord captures what the stand-in Copilot server saw.
type cplRecord struct {
	mu        sync.Mutex
	exchanges int
	exAuth    string
	modelsHdr http.Header
	chatPath  string
	chatHdr   http.Header
}

// cplEnv isolates HOME, XDG dirs and the catalog cache in a temp dir and
// writes a Copilot login for user octo-cpl.
func cplEnv(t *testing.T) {
	t.Helper()
	home := cplHomeEnv(t)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	cplPutFile(t, filepath.Join(home, "xdg", "github-copilot", "apps.json"),
		`{"github.com:Iv1.cpl":{"user":"octo-cpl","oauth_token":"gho_cpl_oauth"}}`)
	copilotCache = copilotSession{}
	t.Cleanup(func() { copilotCache = copilotSession{} })
}

// cplServer starts a stand-in for the token exchange, the model list and chat.
// models is the body returned by GET /models.
func cplServer(t *testing.T, models string, rec *cplRecord) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			rec.exchanges++
			rec.exAuth = r.Header.Get("Authorization")
			b, _ := json.Marshal(map[string]any{
				"token":      "tid=cpl-session",
				"expires_at": time.Now().Add(time.Hour).Unix(),
				"endpoints":  map[string]string{"api": srv.URL},
			})
			w.Write(b)
		case "/models":
			rec.modelsHdr = r.Header.Clone()
			w.Write([]byte(models))
		case "/chat/completions":
			rec.chatPath = r.URL.Path
			rec.chatHdr = r.Header.Clone()
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	orig := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { CopilotTokenURL = orig })
	return srv
}

func cplProvider(t *testing.T) Provider {
	t.Helper()
	p := cplFindProvider(Accounts(), "copilot")
	if p == nil {
		t.Fatal("precondition: copilot login should be found")
	}
	return *p
}

// cplSignBody signs a Chat Completions request with the given JSON body.
func cplSignBody(t *testing.T, p Provider, body string) (*http.Request, error) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "https://unused.example.test/x", nil)
	err := p.Sign(context.Background(), req, Chat, []byte(body))
	return req, err
}

const cplModels = `{"data":[
 {"id":"gpt-5.5","name":"GPT-5.5","capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}},"model_picker_enabled":true},
 {"id":"text-embedding-3","capabilities":{"type":"embeddings"},"model_picker_enabled":true},
 {"id":"copilot-search-v2","capabilities":{"type":"chat"},"model_picker_enabled":true},
 {"id":"claude-sonnet-4.5-secondary","capabilities":{"type":"chat"},"model_picker_enabled":true},
 {"id":"trajectory-judge","capabilities":{"type":"chat"},"model_picker_enabled":true},
 {"id":"gemini-x","capabilities":{"type":"chat"},"model_picker_enabled":false},
 {"id":"claude-haiku-4.5","capabilities":{"type":"chat"},"model_picker_enabled":false,"policy":{"state":"disabled"}},
 {"id":"gpt-5.4","name":"GPT-5.4","capabilities":{"type":"chat"},"model_picker_enabled":false,"policy":{"state":"enabled"}}
]}`

func TestCplRefreshFiltersAndKeepsEfforts(t *testing.T) {
	cplEnv(t)
	rec := &cplRecord{}
	cplServer(t, cplModels, rec)
	p := cplProvider(t)

	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].ID != "gpt-5.5" || ms[1].ID != "gpt-5.4" {
		t.Fatalf("unexpected models after filtering: %+v", ms)
	}
	if strings.Join(ms[0].Efforts, ",") != "low,high" {
		t.Fatalf("efforts not kept: %v", ms[0].Efforts)
	}
	if ms[1].Efforts != nil {
		t.Fatalf("gpt-5.4 should have no efforts, got %v", ms[1].Efforts)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.modelsHdr.Get("Authorization") != "Bearer tid=cpl-session" {
		t.Fatalf("models request should carry the session token, got %q", rec.modelsHdr.Get("Authorization"))
	}
	if rec.modelsHdr.Get("Copilot-Integration-Id") == "" {
		t.Fatal("models request should carry Copilot-Integration-Id")
	}
}

func TestCplRefreshedListIsWhatIsAvailable(t *testing.T) {
	cplEnv(t)
	rec := &cplRecord{}
	cplServer(t, cplModels, rec)
	p := cplProvider(t)
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := p.Available()
	if len(got) != 2 || got[0].ID != "gpt-5.5" || got[1].ID != "gpt-5.4" {
		t.Fatalf("Available should be the refreshed list, got %+v", got)
	}
	if _, _, ok := catalog.Live("copilot"); !ok {
		t.Fatal("refresh should remember the list")
	}
}

func TestCplRefreshNoUsableModelErrors(t *testing.T) {
	cplEnv(t)
	rec := &cplRecord{}
	cplServer(t, `{"data":[{"id":"text-embedding-3","capabilities":{"type":"embeddings"},"model_picker_enabled":true}]}`, rec)
	p := cplProvider(t)
	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("refresh with no usable chat model should fail")
	}
	if _, _, ok := catalog.Live("copilot"); ok {
		t.Fatal("a failed refresh should not remember anything")
	}
}

func TestCplBuiltinListHasNoAuto(t *testing.T) {
	cplEnv(t)
	p := cplProvider(t)
	var hasSonnet bool
	for _, m := range p.Available() {
		if m.ID == "auto" {
			t.Fatal("built-in copilot list must not contain auto")
		}
		if m.ID == "claude-sonnet-4.5" {
			hasSonnet = true
		}
	}
	if !hasSonnet {
		t.Fatal("built-in copilot list should contain claude-sonnet-4.5")
	}
}

func TestCplSignHeadersAndURL(t *testing.T) {
	cplEnv(t)
	rec := &cplRecord{}
	srv := cplServer(t, cplModels, rec)
	p := cplProvider(t)

	req, err := cplSignBody(t, p, `{"model":"gpt-5.5","messages":[{"role":"system","content":"x"},{"role":"user","content":"hi"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := srv.URL + "/chat/completions"; req.URL.String() != want {
		t.Fatalf("URL = %s, want %s", req.URL, want)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tid=cpl-session" {
		t.Fatalf("Authorization = %q", got)
	}
	if req.Header.Get("Copilot-Integration-Id") == "" {
		t.Fatal("Copilot-Integration-Id must be non-empty")
	}
	if req.Header.Get("X-Initiator") != "user" {
		t.Fatalf("X-Initiator = %q, want user", req.Header.Get("X-Initiator"))
	}
	if req.Header.Get("Copilot-Vision-Request") != "" {
		t.Fatal("text-only request must not be marked as vision")
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.exAuth != "token gho_cpl_oauth" {
		t.Fatalf("token exchange Authorization = %q", rec.exAuth)
	}
}

func TestCplSignInitiatorToolIsAgent(t *testing.T) {
	cplEnv(t)
	rec := &cplRecord{}
	cplServer(t, cplModels, rec)
	p := cplProvider(t)

	req, err := cplSignBody(t, p, `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"tool","content":"result"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("X-Initiator"); got != "agent" {
		t.Fatalf("X-Initiator = %q, want agent", got)
	}
}

func TestCplSignVisionHeader(t *testing.T) {
	cplEnv(t)
	rec := &cplRecord{}
	cplServer(t, cplModels, rec)
	p := cplProvider(t)

	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`
	req, err := cplSignBody(t, p, body)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Copilot-Vision-Request"); got != "true" {
		t.Fatalf("Copilot-Vision-Request = %q, want true", got)
	}
	if got := req.Header.Get("X-Initiator"); got != "user" {
		t.Fatalf("X-Initiator = %q, want user", got)
	}
}

func TestCplSignCachesSessionAndRenewsNearExpiry(t *testing.T) {
	cplEnv(t)
	rec := &cplRecord{}
	cplServer(t, cplModels, rec)
	p := cplProvider(t)

	for i := 0; i < 3; i++ {
		if _, err := cplSignBody(t, p, `{"messages":[{"role":"user","content":"hi"}]}`); err != nil {
			t.Fatal(err)
		}
	}
	rec.mu.Lock()
	if rec.exchanges != 1 {
		t.Fatalf("exchanges = %d, want 1 (cached)", rec.exchanges)
	}
	rec.mu.Unlock()

	// Pretend the cached token is about to expire: the next sign exchanges again.
	copilotMu.Lock()
	copilotCache.expires = time.Now().Add(time.Minute)
	copilotMu.Unlock()
	if _, err := cplSignBody(t, p, `{"messages":[{"role":"user","content":"hi"}]}`); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.exchanges != 2 {
		t.Fatalf("exchanges = %d, want 2 after near-expiry", rec.exchanges)
	}
}

func TestCplSignRejectsOtherProtocols(t *testing.T) {
	cplEnv(t)
	rec := &cplRecord{}
	cplServer(t, cplModels, rec)
	p := cplProvider(t)

	req, _ := http.NewRequest(http.MethodPost, "https://unused.example.test/x", nil)
	if err := p.Sign(context.Background(), req, Responses, []byte(`{}`)); err == nil {
		t.Fatal("copilot should refuse Responses protocol")
	}
}

func TestCplSignedOutWithoutLogin(t *testing.T) {
	cplEnv(t)
	rec := &cplRecord{}
	cplServer(t, cplModels, rec)
	p := cplProvider(t)
	// Log out: point the config dir at an empty place after the provider was found.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "empty"))

	req, _ := http.NewRequest(http.MethodPost, "https://unused.example.test/x", nil)
	err := p.Sign(context.Background(), req, Chat, []byte(`{}`))
	if err != ErrCopilotSignedOut {
		t.Fatalf("err = %v, want ErrCopilotSignedOut", err)
	}
}

func TestCplTokenRejectedReportsSignedOut(t *testing.T) {
	cplEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"Bad credentials"}`)
	}))
	t.Cleanup(srv.Close)
	orig := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { CopilotTokenURL = orig })

	p := cplProvider(t)
	req, _ := http.NewRequest(http.MethodPost, "https://unused.example.test/x", nil)
	if err := p.Sign(context.Background(), req, Chat, []byte(`{}`)); err != ErrCopilotSignedOut {
		t.Fatalf("err = %v, want ErrCopilotSignedOut", err)
	}
}
