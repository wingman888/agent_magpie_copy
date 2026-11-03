package provider

import (
	"context"
	"encoding/json"
	"errors"
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

// cpTFake stands in for GitHub's token exchange and the Copilot API. It
// records what it was sent so tests can check headers, paths and bodies.
type cpTFake struct {
	mu  sync.Mutex
	srv *httptest.Server

	expiresAt   time.Time
	rejectToken bool   // answer the exchange with 401
	modelsBody  string // reply to GET /models

	exchanges   int
	exchangeHdr http.Header
	modelsPath  string
	modelsHdr   http.Header
	chatPath    string
	chatHdr     http.Header
	chatBody    []byte
}

func (f *cpTFake) setExpiry(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expiresAt = t
}

func (f *cpTFake) setRejectToken(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejectToken = on
}

func (f *cpTFake) exchangeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exchanges
}

func cpTStartFake(t *testing.T, models string) *cpTFake {
	t.Helper()
	f := &cpTFake{expiresAt: time.Now().Add(time.Hour), modelsBody: models}
	mux := http.NewServeMux()
	mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.exchanges++
		f.exchangeHdr = r.Header.Clone()
		if f.rejectToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"token":"sess-1","expires_at":%d,"endpoints":{"api":%q}}`,
			f.expiresAt.Unix(), f.srv.URL+"/api")
	})
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.modelsPath = r.URL.Path
		f.modelsHdr = r.Header.Clone()
		if r.Header.Get("Authorization") != "Bearer sess-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, f.modelsBody)
	})
	mux.HandleFunc("/api/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.chatPath = r.URL.Path
		f.chatHdr = r.Header.Clone()
		f.chatBody, _ = io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer sess-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","choices":[]}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func cpTResetCache() {
	copilotCache.Lock()
	copilotCache.sessions = nil
	copilotCache.Unlock()
}

// cpTSandbox signs a fake user into Copilot and points the token exchange
// at a stand-in that serves the given model list.
func cpTSandbox(t *testing.T, models string) *cpTFake {
	t.Helper()
	home := acctSandbox(t)
	acctWriteCopilot(t, filepath.Join(home, ".config"))
	f := cpTStartFake(t, models)
	old := CopilotTokenURL
	CopilotTokenURL = f.srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { CopilotTokenURL = old })
	cpTResetCache()
	t.Cleanup(cpTResetCache)
	return f
}

func cpTAccount() Provider {
	return Provider{ID: "copilot", Name: "Copilot", Account: &Account{Agent: "copilot", User: "octocat"}}
}

// cpTModelsJSON mixes every kind of entry the filter has to handle.
const cpTModelsJSON = `{"data":[
 {"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5","model_picker_enabled":true,
  "capabilities":{"type":"chat","supports":{"reasoning_effort":["low","medium","high"]}}},
 {"id":"text-embedding-3","model_picker_enabled":true,"capabilities":{"type":"embeddings"}},
 {"id":"copilot-search-fast","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"gpt-4o-secondary","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"old-model","model_picker_enabled":false,"policy":{"state":"disabled"},"capabilities":{"type":"chat"}},
 {"id":"gpt-5.5","model_picker_enabled":false,"policy":{"state":"enabled"},"capabilities":{"type":"chat"}},
 {"id":"picker-on-policy-off","model_picker_enabled":true,"policy":{"state":"disabled"},"capabilities":{"type":"chat"}},
 {"id":"gpt-5.4-mini","model_picker_enabled":true,
  "capabilities":{"type":"chat","supports":{"reasoning_effort":["none","low"]}}},
 {"id":"exec-agent-1","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"trajectory-x","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"claude-haiku-4.5-4th","model_picker_enabled":true,"capabilities":{"type":"chat"}},
 {"id":"gpt-5-free-auto","model_picker_enabled":true,"capabilities":{"type":"chat"}}
]}`

func cpTRowsFromJSON(t *testing.T, s string) []copilotModelRow {
	t.Helper()
	var v struct {
		Data []copilotModelRow `json:"data"`
	}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v.Data
}

func TestCopilotTokenExchangeHeaders_Cp(t *testing.T) {
	f := cpTSandbox(t, cpTModelsJSON)
	s, err := copilotSessionFor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := f.exchangeHdr.Get("Authorization"); got != "token gho_secret" {
		t.Errorf("token exchange Authorization = %q, want %q", got, "token gho_secret")
	}
	if s.token != "sess-1" || s.api != f.srv.URL+"/api" {
		t.Errorf("session = %+v", s)
	}
}

func TestCopilotSessionReusedUntilNearExpiry_Cp(t *testing.T) {
	f := cpTSandbox(t, cpTModelsJSON)
	ctx := context.Background()

	if _, err := copilotSessionFor(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := copilotSessionFor(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.exchangeCount(); n != 1 {
		t.Fatalf("exchanges with a fresh session = %d, want 1", n)
	}

	// a session about to expire is exchanged again on the next use
	f.setExpiry(time.Now().Add(time.Minute))
	cpTResetCache()
	if _, err := copilotSessionFor(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := copilotSessionFor(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.exchangeCount(); n != 3 {
		t.Fatalf("exchanges with a near-expiry session = %d, want 3 (1 + one per call)", n)
	}

	// a healthy session is exchanged once, then kept
	f.setExpiry(time.Now().Add(time.Hour))
	for i := 0; i < 2; i++ {
		if _, err := copilotSessionFor(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.exchangeCount(); n != 4 {
		t.Fatalf("exchanges after a healthy session = %d, want 4", n)
	}
}

func TestCopilotConcurrentUseExchangesOnce_Cp(t *testing.T) {
	f := cpTSandbox(t, cpTModelsJSON)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := copilotSessionFor(context.Background())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := f.exchangeCount(); n != 1 {
		t.Fatalf("concurrent requests caused %d exchanges, want 1", n)
	}
}

func TestCopilotRejectedTokenAsksForSignIn_Cp(t *testing.T) {
	f := cpTSandbox(t, cpTModelsJSON)
	f.setRejectToken(true)
	_, err := copilotSessionFor(context.Background())
	if err == nil || !errors.Is(err, ErrCopilotSignedOut) || !IsSignedOut(err) {
		t.Fatalf("rejected exchange error = %v, want a signed-out error", err)
	}
	if !strings.Contains(err.Error(), "sign in again") {
		t.Errorf("error does not ask for sign-in: %v", err)
	}
}

func TestCopilotSignedOutWhenFileGone_Cp(t *testing.T) {
	cpTSandbox(t, cpTModelsJSON)
	apps := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json")
	if err := os.Remove(apps); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://account.invalid/", strings.NewReader(`{}`))
	err := cpTAccount().Sign(context.Background(), req, Chat, []byte(`{"messages":[]}`))
	if !IsSignedOut(err) {
		t.Fatalf("Sign without a sign-in = %v, want signed-out", err)
	}
	if acctFindProvider(Accounts(), "copilot") != nil {
		t.Error("copilot should not be listed once signed out")
	}
}

func TestCopilotModelFilter_Cp(t *testing.T) {
	got := copilotChatModels(cpTRowsFromJSON(t, cpTModelsJSON))
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	want := []string{"claude-sonnet-4.5", "gpt-5.5", "picker-on-policy-off", "gpt-5.4-mini"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("kept models = %v, want %v (order kept)", ids, want)
	}
	if e := got[0].Efforts; strings.Join(e, ",") != "low,medium,high" {
		t.Errorf("claude efforts = %v", e)
	}
	if e := got[3].Efforts; strings.Join(e, ",") != "none,low" {
		t.Errorf("gpt-5.4-mini efforts = %v", e)
	}
	if got[1].Efforts != nil {
		t.Errorf("gpt-5.5 has no reasoning list, got %v", got[1].Efforts)
	}
	if got[0].Name != "Claude Sonnet 4.5" {
		t.Errorf("name = %q", got[0].Name)
	}
}

func TestCopilotNoChatModelIsError_Cp(t *testing.T) {
	cpTSandbox(t, `{"data":[{"id":"text-embedding-3","capabilities":{"type":"embeddings"}}]}`)
	_, err := cpTAccount().Fetch(context.Background())
	if err == nil {
		t.Fatal("refresh with no usable chat model should fail")
	}
}

func TestCopilotFetchAsksModelsWithSessionAndKeepsList_Cp(t *testing.T) {
	f := cpTSandbox(t, cpTModelsJSON)
	ms, err := cpTAccount().Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.modelsPath != "/api/models" {
		t.Errorf("models path = %q, want /api/models", f.modelsPath)
	}
	if got := f.modelsHdr.Get("Authorization"); got != "Bearer sess-1" {
		t.Errorf("models Authorization = %q", got)
	}
	if f.modelsHdr.Get("Copilot-Integration-Id") == "" {
		t.Error("models request lacks Copilot-Integration-Id")
	}
	if len(ms) != 4 {
		t.Fatalf("fetched %d models, want 4", len(ms))
	}

	// the refreshed list is what the account offers afterwards, with its reasoning levels
	cp := acctFindProvider(Accounts(), "copilot")
	if cp == nil {
		t.Fatal("copilot not listed")
	}
	av := cp.Available()
	if len(av) != 4 || av[0].ID != "claude-sonnet-4.5" {
		t.Fatalf("available after refresh = %+v", av)
	}
	if strings.Join(av[0].Efforts, ",") != "low,medium,high" {
		t.Errorf("efforts lost after refresh: %v", av[0].Efforts)
	}
	for _, m := range av {
		if m.ID == "auto" {
			t.Error("auto must not be offered")
		}
	}
	raw, _ := os.ReadFile(catalog.LivePath("copilot"))
	if strings.Contains(string(raw), "sess-1") || strings.Contains(string(raw), "gho_secret") {
		t.Errorf("saved model list holds a token: %s", raw)
	}
}

func TestCopilotBuiltinListWithoutRefresh_Cp(t *testing.T) {
	cpTSandbox(t, cpTModelsJSON)
	cp := acctFindProvider(Accounts(), "copilot")
	if cp == nil {
		t.Fatal("copilot not listed")
	}
	av := cp.Available()
	if len(av) == 0 {
		t.Fatal("built-in copilot list is empty")
	}
	for _, m := range av {
		if m.ID == "auto" {
			t.Error("built-in copilot list must not offer auto")
		}
	}
}

func TestCopilotSignPostsToSessionEndpoint_Cp(t *testing.T) {
	f := cpTSandbox(t, cpTModelsJSON)
	body := `{"model":"claude-sonnet-4.5","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, "https://account.invalid/", strings.NewReader(body))
	if err := cpTAccount().Sign(context.Background(), req, Chat, []byte(body)); err != nil {
		t.Fatal(err)
	}
	if want := f.srv.URL + "/api/chat/completions"; req.URL.String() != want {
		t.Fatalf("signed URL = %s, want %s", req.URL, want)
	}
	if req.Header.Get("Authorization") != "Bearer sess-1" {
		t.Errorf("Authorization = %q", req.Header.Get("Authorization"))
	}
	if req.Header.Get("Copilot-Integration-Id") == "" {
		t.Error("Copilot-Integration-Id missing")
	}
	if req.Header.Get("X-Initiator") != "user" {
		t.Errorf("X-Initiator = %q, want user", req.Header.Get("X-Initiator"))
	}
	if req.Header.Get("Copilot-Vision-Request") != "" {
		t.Error("text request must not be marked as vision")
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("signed request answered %s", res.Status)
	}
	if f.chatPath != "/api/chat/completions" || string(f.chatBody) != body {
		t.Errorf("chat reached %q with body %q", f.chatPath, f.chatBody)
	}
}

func TestCopilotInitiatorAndVision_Cp(t *testing.T) {
	cases := []struct {
		name, body, initiator string
		vision                bool
	}{
		{"user last", `{"messages":[{"role":"assistant","content":"a"},{"role":"user","content":"b"}]}`, "user", false},
		{"tool last", `{"messages":[{"role":"user","content":"b"},{"role":"tool","content":"r"}]}`, "agent", false},
		{"assistant last", `{"messages":[{"role":"user","content":"b"},{"role":"assistant","content":"c"}]}`, "agent", false},
		{"image part", `{"messages":[{"role":"user","content":[{"type":"text","text":"x"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`, "user", true},
		{"image in earlier turn", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"u"}}]},{"role":"assistant","content":"ok"},{"role":"user","content":"more"}]}`, "user", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cpTSandbox(t, cpTModelsJSON)
			req, _ := http.NewRequest(http.MethodPost, "https://account.invalid/", nil)
			if err := cpTAccount().Sign(context.Background(), req, Chat, []byte(c.body)); err != nil {
				t.Fatal(err)
			}
			if got := req.Header.Get("X-Initiator"); got != c.initiator {
				t.Errorf("X-Initiator = %q, want %q", got, c.initiator)
			}
			if got := req.Header.Get("Copilot-Vision-Request") == "true"; got != c.vision {
				t.Errorf("vision marked = %v, want %v", got, c.vision)
			}
		})
	}
}

func TestCopilotTestProbesChatEndpoint_Cp(t *testing.T) {
	f := cpTSandbox(t, cpTModelsJSON)
	res := cpTAccount().Test(context.Background())
	if len(res) != 1 || res[0].Protocol != Chat {
		t.Fatalf("results = %+v, want one chat probe", res)
	}
	if !res[0].OK || res[0].Model != "claude-sonnet-4.5" {
		t.Fatalf("probe = %+v", res[0])
	}
	if f.chatPath != "/api/chat/completions" {
		t.Errorf("probe went to %q", f.chatPath)
	}
	if !strings.Contains(string(f.chatBody), `"model":"claude-sonnet-4.5"`) {
		t.Errorf("probe body = %s", f.chatBody)
	}
}
