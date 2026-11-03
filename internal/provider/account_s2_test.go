package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCopilotRateLimitIsNotSignedOut(t *testing.T) {
	home := isolate(t)
	writeFile(t, copilotAppsPathFor(home), `{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_TOKEN"}}`)
	fakeCopilot(t, http.StatusTooManyRequests)

	p, _ := Find("copilot")
	_, err := p.Fetch(context.Background())
	if err == nil {
		t.Fatal("Fetch succeeded against a rate-limited token endpoint")
	}
	if IsSignedOut(err) || strings.Contains(err.Error(), "重新登录") {
		t.Errorf("429 reported as signed out: %v", err)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error lacks the real reason: %v", err)
	}
}

func TestCopilotRejectedLoginIsSignedOut(t *testing.T) {
	home := isolate(t)
	writeFile(t, copilotAppsPathFor(home), `{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_TOKEN"}}`)
	fakeCopilot(t, http.StatusForbidden)

	p, _ := Find("copilot")
	_, err := p.Fetch(context.Background())
	if !IsSignedOut(err) || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Errorf("403 error = %v, want the sign-in advice", err)
	}
}

func TestCodexRefreshRateLimitIsNotSignedOut(t *testing.T) {
	home := isolate(t)
	writeFile(t, codexAuthPathFor(home), codexLoginJSON(t, time.Now().Add(-time.Hour), "rt-1"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"slow down"}}`)
	}))
	t.Cleanup(srv.Close)
	old := codexTokenURL
	codexTokenURL = srv.URL + "/oauth/token"
	t.Cleanup(func() { codexTokenURL = old })

	_, _, err := CodexCredentials(context.Background())
	if err == nil || IsSignedOut(err) {
		t.Fatalf("error = %v, want a real reason rather than signed out", err)
	}
	if !strings.Contains(err.Error(), "slow down") {
		t.Errorf("error lacks the upstream message: %v", err)
	}
}

func TestCodexRefreshLeavesOnlyLoginFile(t *testing.T) {
	home := isolate(t)
	authPath := codexAuthPathFor(home)
	writeFile(t, authPath, codexLoginJSON(t, time.Now().Add(-time.Hour), "rt-old"))
	fresh := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": fresh, "refresh_token": "rt-2"})
	}))
	t.Cleanup(srv.Close)
	old := codexTokenURL
	codexTokenURL = srv.URL + "/oauth/token"
	t.Cleanup(func() { codexTokenURL = old })

	tok, _, err := CodexCredentials(context.Background())
	if err != nil || tok != fresh {
		t.Fatalf("CodexCredentials = %q, %v", tok, err)
	}
	entries, err := os.ReadDir(filepath.Dir(authPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "auth.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory after atomic write holds %v, want only auth.json", names)
	}
	st, err := os.Stat(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("login file mode = %v, want 0600", st.Mode().Perm())
	}
	if !strings.Contains(readFile(t, authPath), "rt-2") {
		t.Error("refreshed refresh token not written back")
	}
}

func TestCopilotAppsSkipsMalformedEntries(t *testing.T) {
	home := isolate(t)
	writeFile(t, copilotAppsPathFor(home), `{"github.com:bad":"junk","github.com:num":42,"github.com:ok":{"user":"octo","oauth_token":"gho_x"}}`)
	a, ok := copilotAccount()
	if !ok || a.User != "octo" {
		t.Fatalf("copilotAccount = %+v, %v; want octo despite malformed entries", a, ok)
	}
}

func TestCopilotLiveListFollowsLogin(t *testing.T) {
	home := isolate(t)
	writeFile(t, copilotAppsPathFor(home), `{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_TOKEN"}}`)
	fakeCopilot(t, 0)

	p, _ := Find("copilot")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("copilot")
	if n := len(p.Available()); n != 2 {
		t.Fatalf("octo's list has %d models, want the 2 refreshed", n)
	}

	// another GitHub login must not inherit octo's refreshed list
	writeFile(t, copilotAppsPathFor(home), `{"github.com:Iv1.xyz":{"user":"bob","oauth_token":"gho_TOKEN"}}`)
	p, _ = Find("copilot")
	if n := len(p.Available()); n != len(copilotBuiltin()) {
		t.Errorf("after switching login Available has %d models, want the built-in %d", n, len(copilotBuiltin()))
	}
}
