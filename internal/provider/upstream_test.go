package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// expiringCodex writes a ChatGPT login whose access token expires in a minute.
func expiringCodex(t *testing.T) (idToken string) {
	t.Helper()
	sandbox(t)
	idToken = fakeJWT(map[string]any{"email": "me@example.com"})
	auth := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"last_refresh":   "old",
		"tokens": map[string]any{
			"id_token":      idToken,
			"access_token":  fakeJWT(map[string]any{"exp": time.Now().Add(time.Minute).Unix()}),
			"refresh_token": "rt-old",
			"account_id":    "acct-1",
		},
	}
	b, _ := json.Marshal(auth)
	writeFile(t, codexPath(), string(b))
	return idToken
}

func fakeRefresh(t *testing.T, status int, access string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if status != http.StatusOK || in["grant_type"] != "refresh_token" || in["refresh_token"] != "rt-old" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": access, "refresh_token": "rt-new"})
	}))
	t.Cleanup(srv.Close)
	prev := CodexRefreshURL
	CodexRefreshURL = srv.URL
	t.Cleanup(func() { CodexRefreshURL = prev })
}

func TestCodexRefreshWritesTokensBackKeepingOtherFields(t *testing.T) {
	idToken := expiringCodex(t)
	fresh := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	fakeRefresh(t, http.StatusOK, fresh)

	_, h, err := SignInUpstream(context.Background(), "codex", "/responses")
	if err != nil {
		t.Fatal(err)
	}
	if h["Authorization"] != "Bearer "+fresh || h["chatgpt-account-id"] != "acct-1" {
		t.Errorf("headers: %v", h)
	}

	raw, err := os.ReadFile(codexPath())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	tok, _ := got["tokens"].(map[string]any)
	if tok["access_token"] != fresh || tok["refresh_token"] != "rt-new" {
		t.Errorf("tokens not refreshed: %v", tok)
	}
	if tok["id_token"] != idToken || tok["account_id"] != "acct-1" {
		t.Errorf("tokens lost fields: %v", tok)
	}
	if got["auth_mode"] != "chatgpt" || got["last_refresh"] == "old" {
		t.Errorf("top level: %v", got)
	}
	if _, ok := got["OPENAI_API_KEY"]; !ok {
		t.Errorf("OPENAI_API_KEY dropped: %s", raw)
	}
}

func TestCodexRefreshFailureSaysLoggedOut(t *testing.T) {
	expiringCodex(t)
	fakeRefresh(t, http.StatusUnauthorized, "")

	_, _, err := SignInUpstream(context.Background(), "codex", "/responses")
	if err == nil || !strings.Contains(err.Error(), "已登出，请运行 `codex login`") {
		t.Errorf("err = %v", err)
	}
}
