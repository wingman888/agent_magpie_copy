package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func unsignedJWT(exp time.Time) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(map[string]any{"exp": exp.Unix()})
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func writeCodexAuthForTest(t *testing.T, home, access string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": "", "access_token": access, "refresh_token": "rt_old", "account_id": "acct_1"}})
	writeTestFile(t, filepath.Join(home, ".codex", "auth.json"), string(body))
}

func TestCodexExpiringWindow(t *testing.T) {
	now := time.Now()
	if !codexExpiring(unsignedJWT(now.Add(4*time.Minute)), now) {
		t.Error("token 4 minutes from expiry should refresh")
	}
	if codexExpiring(unsignedJWT(now.Add(30*time.Minute)), now) {
		t.Error("token 30 minutes from expiry should not refresh")
	}
	if codexExpiring("not-a-jwt", now) {
		t.Error("a token without an exp claim is not treated as expiring")
	}
}

func TestCodexRefreshRunsOnceUnderConcurrency(t *testing.T) {
	home := sandbox(t)
	writeCodexAuthForTest(t, home, unsignedJWT(time.Now().Add(time.Minute)))
	fresh := unsignedJWT(time.Now().Add(time.Hour))

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond) // long enough that the others queue behind the lock
		json.NewEncoder(w).Encode(map[string]string{"access_token": fresh})
	}))
	defer srv.Close()
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	t.Cleanup(func() { CodexRefreshURL = old })

	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	tokens := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := codexTokenFresh(context.Background())
			errs <- err
			tokens <- c.token
		}()
	}
	wg.Wait()
	close(errs)
	close(tokens)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent refresh failed: %v", err)
		}
	}
	for tok := range tokens {
		if tok != fresh {
			t.Fatalf("a caller got the old token")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("refresh endpoint called %d times, want 1", got)
	}
}
