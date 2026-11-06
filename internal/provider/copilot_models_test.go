package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// copilotStandIn serves the token exchange and /models. modelsBody is the raw
// JSON answer for /models; the returned func gives the last /models request's
// headers.
func copilotStandIn(t *testing.T, modelsBody string) func() http.Header {
	t.Helper()
	var mu sync.Mutex
	var lastHeaders http.Header
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			if r.Header.Get("Authorization") != "token gho_refresh" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "sess-models",
				"expires_at": time.Now().Add(time.Hour).Unix(),
				"endpoints":  map[string]any{"api": srv.URL + "/api"},
			})
		case "/api/models":
			mu.Lock()
			lastHeaders = r.Header.Clone()
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(modelsBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { CopilotTokenURL = old })
	return func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		return lastHeaders
	}
}

// isolateCache keeps the models.dev/live cache inside the test's temp tree.
func isolateCache(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
}

func copilotAccount(t *testing.T) Provider {
	t.Helper()
	p := findAccount(Accounts(), "copilot")
	if p == nil {
		t.Fatal("copilot account not listed")
	}
	return *p
}

const filterFixture = `{"data":[
  {"id":"gpt-5.5","name":"GPT-5.5","capabilities":{"type":"chat","supports":{"reasoning_effort":["low","medium","high"]}},"model_picker_enabled":true},
  {"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","capabilities":{"type":"chat"},"policy":{"state":"enabled"}},
  {"id":"text-embedding-3","capabilities":{"type":"embeddings"},"model_picker_enabled":true},
  {"id":"copilot-search-v1","capabilities":{"type":"chat"},"model_picker_enabled":true},
  {"id":"exec-agent-x","capabilities":{"type":"chat"},"model_picker_enabled":true},
  {"id":"trajectory-y","capabilities":{"type":"chat"},"model_picker_enabled":true},
  {"id":"gpt-5-mini-secondary","capabilities":{"type":"chat"},"model_picker_enabled":true},
  {"id":"gpt-5-mini-tertiary","capabilities":{"type":"chat"},"model_picker_enabled":true},
  {"id":"gpt-5-mini-4th","capabilities":{"type":"chat"},"model_picker_enabled":true},
  {"id":"gpt-4o-free-auto","capabilities":{"type":"chat"},"model_picker_enabled":true},
  {"id":"old-disabled","capabilities":{"type":"chat"},"model_picker_enabled":false,"policy":{"state":"disabled"}},
  {"id":"no-flags","capabilities":{"type":"chat"}},
  {"id":"picker-on-policy-off","name":"Picker On","capabilities":{"type":"chat"},"model_picker_enabled":true,"policy":{"state":"disabled"}}
]}`

func TestRefreshCopilotModelsFiltersAndKeepsEfforts(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCopilot(t, "apps.json", `{"github.com:x":{"user":"octocat","oauth_token":"gho_refresh"}}`)
	copilotStandIn(t, filterFixture)

	ms, err := RefreshModels(context.Background(), copilotAccount(t))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.ID)
	}
	want := []string{"gpt-5.5", "claude-sonnet-4.5", "picker-on-policy-off"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kept models = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(ms[0].Efforts, []string{"low", "medium", "high"}) {
		t.Errorf("gpt-5.5 efforts = %v", ms[0].Efforts)
	}
	if ms[1].Name != "Claude Sonnet 4.5" || ms[1].Efforts != nil {
		t.Errorf("claude-sonnet-4.5 = %+v", ms[1])
	}
}

func TestRefreshCopilotModelsRequestHeaders(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCopilot(t, "apps.json", `{"github.com:x":{"user":"octocat","oauth_token":"gho_refresh"}}`)
	last := copilotStandIn(t, filterFixture)

	if _, err := RefreshModels(context.Background(), copilotAccount(t)); err != nil {
		t.Fatal(err)
	}
	h := last()
	if h == nil {
		t.Fatal("/models was not requested")
	}
	if got := h.Get("Authorization"); got != "Bearer sess-models" {
		t.Errorf("Authorization = %q", got)
	}
	if h.Get("Copilot-Integration-Id") == "" {
		t.Error("Copilot-Integration-Id must be non-empty")
	}
}

func TestRefreshCopilotModelsNoneUsableIsError(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCopilot(t, "apps.json", `{"github.com:x":{"user":"octocat","oauth_token":"gho_refresh"}}`)
	copilotStandIn(t, `{"data":[{"id":"text-embedding","capabilities":{"type":"embeddings"},"model_picker_enabled":true},{"id":"auto","capabilities":{"type":"chat"},"policy":{"state":"disabled"}}]}`)

	if _, err := RefreshModels(context.Background(), copilotAccount(t)); err == nil {
		t.Fatal("expected an error when no chat model is usable")
	}
}

func TestRefreshCopilotModelsSignedOut(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	copilotStandIn(t, filterFixture)
	p := Provider{Name: "Copilot", ID: "copilot", Account: &Account{Agent: "copilot", User: "GitHub"}}
	_, err := RefreshModels(context.Background(), p)
	if !errors.Is(err, ErrSignedOut) {
		t.Fatalf("err = %v, want ErrSignedOut", err)
	}
}

func TestRefreshCopilotModelsRememberedWithoutToken(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCopilot(t, "apps.json", `{"github.com:x":{"user":"octocat","oauth_token":"gho_refresh"}}`)
	copilotStandIn(t, filterFixture)

	if _, err := RefreshModels(context.Background(), copilotAccount(t)); err != nil {
		t.Fatal(err)
	}
	// Available now reports the refreshed list, not the built-in one.
	p := copilotAccount(t)
	var got []string
	for _, m := range p.Available() {
		got = append(got, m.ID)
	}
	if want := []string{"gpt-5.5", "claude-sonnet-4.5", "picker-on-policy-off"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Available after refresh = %v, want %v", got, want)
	}
	if _, ok := p.Fetched(); !ok {
		t.Error("Fetched should report the refresh")
	}
	// Nothing secret reaches the cache file.
	b, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CACHE_HOME"), "dial", "models", "copilot.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"gho_refresh", "sess-models"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("cache holds %q", secret)
		}
	}
}

func TestCopilotBuiltinListWithoutAuto(t *testing.T) {
	setupEnv(t)
	isolateCache(t)
	writeCopilot(t, "apps.json", `{"github.com:x":{"user":"octocat","oauth_token":"gho_refresh"}}`)
	p := copilotAccount(t)
	var sonnet bool
	for _, m := range p.Available() {
		if m.ID == "auto" {
			t.Fatal("built-in Copilot list must not contain auto")
		}
		if m.ID == "claude-sonnet-4.5" {
			sonnet = true
		}
	}
	if !sonnet {
		t.Error("built-in Copilot list must contain claude-sonnet-4.5")
	}
}

func TestCopilotChatRequestMarkers(t *testing.T) {
	setupEnv(t)
	writeCopilot(t, "apps.json", `{"github.com:x":{"user":"octocat","oauth_token":"gho_refresh"}}`)
	copilotStandIn(t, filterFixture)
	p := copilotAccount(t)

	cases := []struct {
		name, body, initiator string
		vision                bool
	}{
		{"user last", `{"messages":[{"role":"user","content":"hi"}]}`, "user", false},
		{"tool last", `{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"r"}]}`, "agent", false},
		{"assistant last", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"}]}`, "agent", false},
		{"vision", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`, "user", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := newReq(t)
			if err := p.Sign(context.Background(), req, Chat, []byte(c.body)); err != nil {
				t.Fatal(err)
			}
			if got := req.URL.String(); !strings.HasSuffix(got, "/api/chat/completions") {
				t.Errorf("URL = %s", got)
			}
			if got := req.Header.Get("Authorization"); got != "Bearer sess-models" {
				t.Errorf("Authorization = %q", got)
			}
			if req.Header.Get("Copilot-Integration-Id") == "" {
				t.Error("Copilot-Integration-Id must be set")
			}
			if got := req.Header.Get("X-Initiator"); got != c.initiator {
				t.Errorf("X-Initiator = %q, want %s", got, c.initiator)
			}
			if got := req.Header.Get("Copilot-Vision-Request") == "true"; got != c.vision {
				t.Errorf("vision header = %v, want %v", got, c.vision)
			}
		})
	}
}
