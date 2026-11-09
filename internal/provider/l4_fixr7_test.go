package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An access token that is not a JWT has no readable exp, so it must be used
// as is and never sent to the refresh endpoint.
func TestL4FixR7OpaqueAccessTokenIsUsedWithoutRefresh(t *testing.T) {
	l4AcctTEnv(t)
	authPath := filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")
	idTok := l4AcctTJWT(t, map[string]any{"email": "dev@example.com"})
	l4AcctTWrite(t, authPath, `{"auth_mode":"chatgpt","tokens":{"id_token":"`+idTok+
		`","access_token":"opaque-token","refresh_token":"rt-x","account_id":"acct-9"}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("token refresh must not run for an access token without exp")
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	oldURL := CodexTokenURL
	CodexTokenURL = srv.URL
	t.Cleanup(func() { CodexTokenURL = oldURL })

	creds, err := CodexToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessToken != "opaque-token" || creds.AccountID != "acct-9" {
		t.Fatalf("creds = %+v", creds)
	}
}

// A stored account record keeps only its id and ticked models.
func TestL4FixR7AccountRecordHasOnlyIDAndModels(t *testing.T) {
	l4AcctTEnv(t)
	l4AcctTCodexAuth(t, "chatgpt")
	if err := Save(Provider{ID: CodexID, Name: "Codex", Key: "sk-no", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"models"`) || strings.Contains(s, `"name"`) || strings.Contains(s, `"key"`) || strings.Contains(s, "sk-no") {
		t.Fatalf("config = %s", s)
	}
}

// Probing an account provider without a login says so instead of printing nothing.
func TestL4FixR7AccountProbeSaysSignedOut(t *testing.T) {
	l4AcctTEnv(t)
	rs := Provider{ID: CodexID}.Test(context.Background())
	if len(rs) != 1 || rs[0].OK || !strings.Contains(rs[0].Error, "not signed in") {
		t.Fatalf("results = %+v", rs)
	}
}
