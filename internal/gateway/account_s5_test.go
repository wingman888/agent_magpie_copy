package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestS5SignedOutCodexModelIs401: a codex/ model with no Codex sign-in behind
// it (and no provider of the user's own) answers 401 with a sign-in hint, not
// 404 "unknown model".
func TestS5SignedOutCodexModelIs401(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","input":"hi"}`))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401: %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"已登出", "请重新登录"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body lacks %q: %s", want, rec.Body.String())
		}
	}
}
