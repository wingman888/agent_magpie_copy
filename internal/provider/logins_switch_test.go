package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubClaudeAgent installs a fake `claude` whose `auth status` names the
// account the credential file holds: A1 is a@x.com, anything else is b@x.com.
// It can be made to answer a fixed account by rewriting the script.
func stubClaudeAgent(t *testing.T, home string) string {
	t.Helper()
	cred := filepath.Join(home, ".claude", ".credentials.json")
	script := filepath.Join(home, "claude")
	body := "#!/bin/sh\nif grep -q '\"A1\"' " + cred + "; then echo '{\"loggedIn\":true,\"email\":\"a@x.com\",\"subscriptionType\":\"max\"}'; else echo '{\"loggedIn\":true,\"email\":\"b@x.com\",\"subscriptionType\":\"max\"}'; fi\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeExecutable = func() string { return script }
	forgetClaudeStatus()
	return script
}

// writeClaudeAgentLogin signs the Claude Code file in as the given token.
func writeClaudeAgentLogin(t *testing.T, home, token string, expires time.Time) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": token, "refreshToken": "r-" + token,
			"expiresAt": expires.UnixMilli(), "subscriptionType": "max"},
	})
	forgetClaudeCredential()
	forgetClaudeStatus()
}

func TestClaudeSwitchVerifiesIdentity(t *testing.T) {
	home := loginHome(t)
	stubClaudeAgent(t, home)
	future := time.Now().Add(time.Hour)
	writeClaudeAgentLogin(t, home, "A1", future)
	if err := RememberLogins(); err != nil {
		t.Fatal(err)
	}
	writeClaudeAgentLogin(t, home, "B1", future)
	if err := RememberLogins(); err != nil {
		t.Fatal(err)
	}
	writeClaudeAgentLogin(t, home, "B2", future)

	if _, err := SwitchLogin("claude", "a@x.com"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	// the agent itself now reports the target account
	forgetClaudeStatus()
	if who, _, out := claudeIdentity(); out || who != "a@x.com" {
		t.Fatalf("after switch the agent is signed in as %q (signed out %v), want a@x.com", who, out)
	}
	if accessOf(t, "claude", "b@x.com") != "B2" {
		t.Error("outgoing login's latest token was not kept")
	}
}

func TestClaudeSwitchRestoresAgentWhenIdentityDiffers(t *testing.T) {
	home := loginHome(t)
	script := stubClaudeAgent(t, home)
	future := time.Now().Add(time.Hour)
	writeClaudeAgentLogin(t, home, "A1", future)
	if err := RememberLogins(); err != nil {
		t.Fatal(err)
	}
	writeClaudeAgentLogin(t, home, "B2", future)
	// the agent keeps reporting b, whatever the file says
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho '{\"loggedIn\":true,\"email\":\"b@x.com\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	forgetClaudeStatus()

	if _, err := SwitchLogin("claude", "a@x.com"); err == nil {
		t.Fatal("switch with a mismatched identity succeeded")
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	var got struct {
		OAuth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	json.Unmarshal(b, &got)
	if got.OAuth.AccessToken != "B2" {
		t.Errorf("agent login not restored after failed switch: %q", got.OAuth.AccessToken)
	}
	if accessOf(t, "claude", "a@x.com") != "A1" {
		t.Error("stored login for the target changed after a failed switch")
	}
}

func TestClaudeRefreshIsSingleFlight(t *testing.T) {
	home := loginHome(t)
	stubClaudeAgent(t, home)
	writeClaudeAgentLogin(t, home, "A1", time.Now().Add(-time.Minute))
	if err := RememberLogins(); err != nil {
		t.Fatal(err)
	}
	// the stored copy is expired, so every caller must refresh it

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(100 * time.Millisecond)
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["refresh_token"] != "r-A1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh", "refresh_token": "r-fresh", "expires_in": 3600})
	}))
	defer srv.Close()
	claudeTokenURL = srv.URL

	var wg sync.WaitGroup
	tokens := make([]string, 4)
	errs := make([]error, 4)
	for i := range tokens {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tokens[i], errs[i] = storedClaudeToken(context.Background(), "a@x.com")
		}(i)
	}
	wg.Wait()
	for i := range tokens {
		if errs[i] != nil || tokens[i] != "fresh" {
			t.Errorf("caller %d: token %q, err %v", i, tokens[i], errs[i])
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("refresh token spent %d times, want 1", n)
	}
}
