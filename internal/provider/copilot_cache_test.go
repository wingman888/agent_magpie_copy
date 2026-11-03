package provider

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The cached Copilot session is keyed by the exchange address as well as the
// OAuth token: pointing CopilotTokenURL at another stand-in must not reuse a
// session obtained from the first one, even when the OAuth token is the same.
func TestCopilotSessionKeyedByExchangeURL_CpCache(t *testing.T) {
	a := cpTSandbox(t, cpTModelsJSON)
	ctx := context.Background()

	sa, err := copilotSessionFor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a.exchangeCount() != 1 || sa.api != a.srv.URL+"/api" {
		t.Fatalf("first exchange: count=%d session=%+v", a.exchangeCount(), sa)
	}

	// second stand-in, same GitHub OAuth token (acctWriteCopilot uses gho_secret)
	b := cpTStartFake(t, cpTModelsJSON)
	old := CopilotTokenURL
	CopilotTokenURL = b.srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { CopilotTokenURL = old })

	sb, err := copilotSessionFor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b.exchangeCount() != 1 {
		t.Fatalf("after pointing at stand-in B: B exchanges = %d, want 1", b.exchangeCount())
	}
	if sb.api != b.srv.URL+"/api" {
		t.Fatalf("session api = %q, want stand-in B's %q", sb.api, b.srv.URL+"/api")
	}
	if a.exchangeCount() != 1 {
		t.Fatalf("stand-in A was exchanged again: %d", a.exchangeCount())
	}
}

// Signing out (deleting the agent's Copilot sign-in file) takes effect at once,
// even when a session for that sign-in is still cached.
func TestCopilotSignOutTakesEffectWithCachedSession_CpCache(t *testing.T) {
	f := cpTSandbox(t, cpTModelsJSON)
	ctx := context.Background()
	if _, err := copilotSessionFor(ctx); err != nil {
		t.Fatal(err)
	}

	dir := copilotDir()
	for _, name := range []string{"apps.json", "hosts.json"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}

	if _, err := copilotSessionFor(ctx); !errors.Is(err, ErrCopilotSignedOut) {
		t.Fatalf("after sign-out: err = %v, want ErrCopilotSignedOut", err)
	}
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cpTAccount().Sign(ctx, req, Chat, []byte(`{"messages":[]}`)); !IsSignedOut(err) {
		t.Fatalf("Sign after sign-out: err = %v, want a signed-out error", err)
	}
	if n := f.exchangeCount(); n != 1 {
		t.Fatalf("exchanges = %d, want 1 (no exchange without a sign-in)", n)
	}
}
