package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// A signed-in login with a user-built, keyless provider of the same id must
// not be reported as signed out: the request gets a plain error instead.
func TestSignedInLoginWithKeylessCustomProviderIsNotSignedOut(t *testing.T) {
	codexSandbox(t)
	writeAuth(t, freshAccess(t), nil)
	if err := provider.Save(provider.Provider{ID: "codex", Chat: "http://relay.invalid/v1"}); err != nil {
		t.Fatal(err)
	}

	rec := postJSON(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if rec.Code == http.StatusUnauthorized || strings.Contains(rec.Body.String(), "codex login") {
		t.Fatalf("status %d; a signed-in login must not be reported as signed out: %s", rec.Code, rec.Body.String())
	}
}

// Only a 401 from the backend means the login was refused. A 403 is passed
// through as the backend's own error.
func TestBackendForbiddenIsNotMappedToSignIn(t *testing.T) {
	codexSandbox(t)
	writeAuth(t, freshAccess(t), nil)
	backend := &codexBackend{code: http.StatusForbidden, reply: `{"detail":"forbidden here"}`, contentType: "application/json"}
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)
	provider.CodexBase = srv.URL

	rec := postJSON(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":true}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d; want 403: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "codex login") {
		t.Fatalf("a 403 must not ask for sign-in: %s", rec.Body.String())
	}
}
