package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// TestFix_AccountExposesAllWhenMany: an account provider offers all of its
// available models, even when there are more than manyModels of them.
func TestFix_AccountExposesAllWhenMany(t *testing.T) {
	home := acctEnv(t)
	acctPut(t, filepath.Join(home, "xdgconfig", "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_secret"}}`)
	var many []catalog.Model
	for i := 0; i < manyModels+5; i++ {
		many = append(many, catalog.Model{ID: fmt.Sprintf("m%02d", i), Name: fmt.Sprintf("M%02d", i)})
	}
	many = append(many, catalog.Model{ID: "auto", Name: "Let Copilot pick"})
	if err := catalog.SaveLive("copilot", "", many); err != nil {
		t.Fatal(err)
	}
	p, ok := acctFind(All(), "copilot")
	if !ok {
		t.Fatal("copilot not listed")
	}
	want := manyModels + 5
	if got := len(p.Available()); got != want {
		t.Fatalf("available = %d, want %d (auto excluded)", got, want)
	}
	if got := len(p.Exposed()); got != want {
		t.Errorf("exposed = %d, want all %d available", got, want)
	}
}

// TestFix_BuiltinCopilotHasNoAuto: the built-in copilot list is non-empty and
// does not carry auto, and the provider list never offers copilot/auto.
func TestFix_BuiltinCopilotHasNoAuto(t *testing.T) {
	home := acctEnv(t)
	acctPut(t, filepath.Join(home, "xdgconfig", "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_secret"}}`)
	if len(catalog.Builtin("copilot")) == 0 {
		t.Fatal("copilot built-in list is empty")
	}
	for _, m := range catalog.Builtin("copilot") {
		if m.ID == "auto" {
			t.Fatal("copilot built-in list contains auto")
		}
	}
	for _, e := range Catalog() {
		if e.ID == "copilot/auto" {
			t.Error("copilot/auto in catalog")
		}
	}
}

// TestFix_AccountProbeUsesOwnUpstream: the probe of an account provider sends
// the agent's token and leaves out the output-limit fields.
func TestFix_AccountProbeUsesOwnUpstream(t *testing.T) {
	home := acctEnv(t)
	acctCodexLogin(t, home, time.Now().Add(time.Hour))

	var mu sync.Mutex
	var gotAuth, gotAcct string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotAuth, gotAcct, gotBody = r.Header.Get("Authorization"), r.Header.Get("chatgpt-account-id"), b
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer srv.Close()
	old := CodexBase
	CodexBase = srv.URL
	t.Cleanup(func() { CodexBase = old })

	p, ok := acctFind(All(), "codex")
	if !ok {
		t.Fatal("codex not listed")
	}
	res := p.testAccount(context.Background(), "gpt-5.5")
	if len(res) != 1 {
		t.Fatalf("results = %d, want one for responses", len(res))
	}
	if !res[0].OK {
		t.Fatalf("probe failed: %+v", res[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.HasPrefix(gotAuth, "Bearer ") || len(gotAuth) <= len("Bearer ") {
		t.Errorf("Authorization = %q, want the agent's token", gotAuth)
	}
	if gotAcct != "acct-123" {
		t.Errorf("chatgpt-account-id = %q, want acct-123", gotAcct)
	}
	s := string(gotBody)
	if strings.Contains(s, "max_output_tokens") || strings.Contains(s, "temperature") {
		t.Errorf("probe body carries output-limit fields: %s", s)
	}
}
