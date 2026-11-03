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
	"sync/atomic"
	"testing"
	"time"
)

// copilotExchangeAt serves a Copilot token exchange that answers with the
// given session token and an API base on apiHost. hits counts exchanges.
func copilotExchangeAt(t *testing.T, session, apiHost string, hits *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_x" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      session,
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
			"endpoints":  map[string]string{"api": "https://" + apiHost},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A cached Copilot session must not outlive the token URL it came from: after
// CopilotTokenURL changes, the next request exchanges at the new endpoint.
func TestCopilotSessionIsNotReusedAcrossTokenURLs(t *testing.T) {
	isolate(t)
	resetCopilotCache()
	t.Cleanup(resetCopilotCache)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "hosts.json"),
		`{"github.com":{"user":"octo","oauth_token":"gho_x"}}`)

	var aHits, bHits int32
	a := copilotExchangeAt(t, "sess-a", "api-a.example.invalid", &aHits)
	b := copilotExchangeAt(t, "sess-b", "api-b.example.invalid", &bHits)
	body := []byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}]}`)

	setVar(t, &CopilotTokenURL, a.URL+"/token")
	p, ok := findAccount(Accounts(), "copilot")
	if !ok {
		t.Fatal("copilot account missing")
	}
	sign := func() *http.Request {
		t.Helper()
		req := newSignReq(t)
		if err := p.Sign(context.Background(), req, Chat, body); err != nil {
			t.Fatal(err)
		}
		return req
	}

	r1 := sign()
	if got := r1.Header.Get("Authorization"); got != "Bearer sess-a" {
		t.Fatalf("first Authorization = %q, want Bearer sess-a", got)
	}

	setVar(t, &CopilotTokenURL, b.URL+"/token")
	r2 := sign()
	if got := r2.Header.Get("Authorization"); got != "Bearer sess-b" {
		t.Fatalf("after URL change Authorization = %q, want Bearer sess-b (stale session reused)", got)
	}
	if r2.URL.Host != "api-b.example.invalid" {
		t.Errorf("after URL change host = %q, want api-b.example.invalid", r2.URL.Host)
	}
	if n := atomic.LoadInt32(&bHits); n != 1 {
		t.Errorf("exchanges at the new URL = %d, want 1", n)
	}
}

// An account's refreshed list is offered whole, even past the 24-model
// threshold that caps "expose everything" for ordinary providers.
func TestCopilotRefreshedListOverManyModelsStaysWhole(t *testing.T) {
	isolate(t)
	resetCopilotCache()
	t.Cleanup(resetCopilotCache)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "hosts.json"),
		`{"github.com":{"user":"octo","oauth_token":"gho_x"}}`)

	var entries []string
	for i := 0; i < 30; i++ {
		entries = append(entries, fmt.Sprintf(
			`{"id":"chat-model-%02d","name":"Chat %02d","capabilities":{"type":"chat"},"model_picker_enabled":true}`, i, i))
	}
	copilotStandIn(t, `{"data":[`+strings.Join(entries, ",")+`]}`)

	p, ok := findAccount(Accounts(), "copilot")
	if !ok {
		t.Fatal("copilot account missing")
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	again, ok := findAccount(Accounts(), "copilot")
	if !ok {
		t.Fatal("copilot account missing after refresh")
	}
	if n := len(again.Available()); n != 30 {
		t.Fatalf("available = %d, want 30", n)
	}
	if n := len(again.Exposed()); n != 30 {
		t.Fatalf("exposed = %d, want all 30", n)
	}
	// The same holds for a bare account provider with no saved selection.
	bare := Provider{ID: "copilot", Name: "Copilot", Account: again.Account, Listed: again.Listed, Efforts: again.Efforts}
	if n := len(bare.Exposed()); n != 30 {
		t.Fatalf("bare account exposed = %d, want all 30", n)
	}
}

// The plan key is always present in an account's JSON, empty when unknown.
func TestAccountJSONAlwaysHasPlanKey(t *testing.T) {
	b, err := json.Marshal(Account{Agent: "copilot", User: "octo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"plan":""`) {
		t.Fatalf("account JSON = %s, want an explicit empty plan key", b)
	}
}
