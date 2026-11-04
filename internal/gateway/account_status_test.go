package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// statusSandbox points every credential and config location at a fresh
// directory and restores the provider's stand-in addresses afterwards.
// It is self-contained so this file does not depend on other test files.
func statusSandbox(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	oldBase, oldRefresh, oldClient := provider.CodexBase, provider.CodexRefreshURL, provider.CodexClientID
	t.Cleanup(func() {
		provider.CodexBase, provider.CodexRefreshURL, provider.CodexClientID = oldBase, oldRefresh, oldClient
	})
}

// statusJWT builds an unsigned token carrying the given claims.
func statusJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// statusWriteAuth writes a ChatGPT-mode auth.json with the given access token.
func statusWriteAuth(t *testing.T, access string) {
	t.Helper()
	id := statusJWT(t, map[string]any{"email": "dev@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}})
	top := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"last_refresh":   "2026-01-01T00:00:00Z",
		"tokens": map[string]any{
			"id_token":      id,
			"access_token":  access,
			"refresh_token": "RT-OLD",
			"account_id":    "acct-1",
		},
	}
	b, err := json.Marshal(top)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// statusFreshAccess is a token that will not expire soon.
func statusFreshAccess(t *testing.T) string {
	return statusJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
}

// statusPostJSON sends a request through the gateway handler and returns the recorder.
func statusPostJSON(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	New().Handler().ServeHTTP(rec, req)
	return rec
}

// A signed-in login with a user-built, keyless provider of the same id must
// not be reported as signed out: the request gets a plain error instead.
func TestAcctStatusSignedInLoginWithKeylessCustomProviderIsNotSignedOut(t *testing.T) {
	statusSandbox(t)
	statusWriteAuth(t, statusFreshAccess(t))
	// Written straight into dial's config, as the user would have: a keyless
	// remote address under the id "codex". Save would treat it as a model pick.
	writeCustom := `{"providers":[{"id":"codex","name":"My Codex","chat":"http://relay.invalid/v1"}]}`
	if err := os.MkdirAll(filepath.Dir(provider.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provider.Path(), []byte(writeCustom), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := statusPostJSON(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d; want 404 for a keyless custom provider while signed in: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "codex login") {
		t.Fatalf("a signed-in login must not be reported as signed out: %s", rec.Body.String())
	}
}

// Only a 401 from the backend means the login was refused. A 403 is passed
// through as the backend's own error.
func TestAcctStatusBackendForbiddenIsNotMappedToSignIn(t *testing.T) {
	statusSandbox(t)
	statusWriteAuth(t, statusFreshAccess(t))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"detail":"forbidden here"}`)
	}))
	t.Cleanup(srv.Close)
	provider.CodexBase = srv.URL

	rec := statusPostJSON(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":true}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d; want 403: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "codex login") {
		t.Fatalf("a 403 must not ask for sign-in: %s", rec.Body.String())
	}
}
