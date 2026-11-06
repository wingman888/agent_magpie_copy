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
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// f02CodexAuthWithExp writes a ChatGPT sign-in whose access token expires at exp.
func f02CodexAuthWithExp(t *testing.T, home string, exp int64) string {
	t.Helper()
	access := f02FakeJWT(fmt.Sprintf(`{"exp":%d}`, exp))
	id := f02FakeJWT(`{"email":"dev@example.com","https://api.openai.com/auth":{"chatgpt_plan_type":"plus","chatgpt_account_id":"acct-1"}}`)
	auth := fmt.Sprintf(`{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"custom":{"keep":1},"tokens":{"id_token":%q,"access_token":%q,"refresh_token":"R1","account_id":"acct-1","extra":"kept"}}`, id, access)
	f02WriteFile(t, filepath.Join(home, ".codex", "auth.json"), auth)
	return access
}

func f02RefreshServer(t *testing.T, calls *int32, newAccess string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if status != http.StatusOK {
			http.Error(w, `{"error":"invalid_grant"}`, status)
			return
		}
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["grant_type"] != "refresh_token" || in["refresh_token"] != "R1" {
			http.Error(w, "bad request", 400)
			return
		}
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"R2"}`, newAccess)
	}))
	t.Cleanup(srv.Close)
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	t.Cleanup(func() { CodexRefreshURL = old })
	return srv
}

func TestF02CodexRefreshWritesBackKeepingFields(t *testing.T) {
	home := f02SetHome(t)
	f02CodexAuthWithExp(t, home, time.Now().Add(2*time.Minute).Unix())
	newAccess := f02FakeJWT(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix()))
	var calls int32
	f02RefreshServer(t, &calls, newAccess, http.StatusOK)

	p := Provider{ID: codexID, Account: &Account{Agent: codexID}}
	req, err := p.SignedRequest(context.Background(), Responses, []byte(`{"model":"gpt-5.5","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+newAccess {
		t.Fatalf("request did not use the renewed token: %q", got)
	}
	if calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls)
	}

	path := filepath.Join(home, ".codex", "auth.json")
	raw, _ := os.ReadFile(path)
	var f map[string]any
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	tok := f["tokens"].(map[string]any)
	if tok["access_token"] != newAccess || tok["refresh_token"] != "R2" {
		t.Errorf("new tokens not written: %v", tok)
	}
	if tok["extra"] != "kept" || tok["account_id"] != "acct-1" || tok["id_token"] == nil {
		t.Errorf("token fields lost: %v", tok)
	}
	if _, ok := f["custom"]; !ok || f["last_refresh"] == nil {
		t.Errorf("top-level fields lost or no last_refresh: %s", raw)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestF02CodexFreshTokenNotRefreshed(t *testing.T) {
	home := f02SetHome(t)
	f02CodexAuthWithExp(t, home, time.Now().Add(time.Hour).Unix())
	var calls int32
	f02RefreshServer(t, &calls, "x", http.StatusOK)
	p := Provider{ID: codexID, Account: &Account{Agent: codexID}}
	if err := p.SignIn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("a fresh token was refreshed %d times", calls)
	}
}

func TestF02CodexConcurrentRequestsRefreshOnce(t *testing.T) {
	home := f02SetHome(t)
	f02CodexAuthWithExp(t, home, time.Now().Add(time.Minute).Unix())
	newAccess := f02FakeJWT(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix()))
	var calls int32
	f02RefreshServer(t, &calls, newAccess, http.StatusOK)
	p := Provider{ID: codexID, Account: &Account{Agent: codexID}}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.SignIn(context.Background()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent sign-in failed: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("refresh calls = %d, want exactly 1", n)
	}
}

func TestF02CodexRefreshFailureAsksToSignInAgain(t *testing.T) {
	home := f02SetHome(t)
	f02CodexAuthWithExp(t, home, time.Now().Add(time.Minute).Unix())
	var calls int32
	f02RefreshServer(t, &calls, "", http.StatusUnauthorized)
	p := Provider{ID: codexID, Account: &Account{Agent: codexID}}
	err := p.SignIn(context.Background())
	if err == nil {
		t.Fatal("a refused refresh must be an error")
	}
	if !strings.Contains(err.Error(), "Codex sign-in expired, run `codex login` again") {
		t.Fatalf("message does not ask for a new sign-in: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if !strings.Contains(string(raw), `"R1"`) {
		t.Error("auth.json changed after a refused refresh")
	}
}

func TestF02SignedOutMessageOnlyWhenLoggedOut(t *testing.T) {
	home := f02SetHome(t)
	if msg, gone := SignedOutMessage(codexID); !gone || !strings.Contains(msg, "codex login") {
		t.Fatalf("logged-out codex: %q %v", msg, gone)
	}
	f02CodexAuthWithExp(t, home, time.Now().Add(time.Hour).Unix())
	if _, gone := SignedOutMessage(codexID); gone {
		t.Fatal("signed-in codex reported as signed out")
	}
	if _, gone := SignedOutMessage("openrouter"); gone {
		t.Fatal("a non-account id reported as signed out")
	}
}
