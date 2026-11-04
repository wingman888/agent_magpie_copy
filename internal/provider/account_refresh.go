package provider

// Codex access tokens are short-lived. Shortly before one expires, dial
// refreshes it the way Codex itself does and writes the new tokens back into
// the agent's own auth.json. Every other field of that file is kept, and the
// file stays private (0600). A mutex plus a re-read under the lock keeps
// concurrent requests from refreshing the same token twice.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CodexRefreshURL is the OAuth endpoint that exchanges a refresh token for a
// new access token. A variable so tests can point it at a stand-in.
var CodexRefreshURL = "https://auth.openai.com/oauth/token"

// CodexClientID is the OAuth client Codex itself refreshes with.
var CodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// codexRefreshSlack is how long before expiry a Codex token is refreshed.
const codexRefreshSlack = 5 * time.Minute

var (
	codexRefreshMu sync.Mutex
	codexHTTP      = &http.Client{Timeout: 30 * time.Second}
)

// codexTokenExpiring reports whether an access token's exp claim is within
// the refresh slack. A token without an exp claim is not treated as expiring.
func codexTokenExpiring(tok string, now time.Time) bool {
	exp, ok := jwtClaims(tok)["exp"].(float64)
	if !ok {
		return false
	}
	return time.Unix(int64(exp), 0).Sub(now) < codexRefreshSlack
}

// codexUsable returns the current Codex login with a fresh access token,
// refreshing it first when it is about to expire. A failed refresh is a
// signed-out error: the user has to sign in to Codex again.
func codexUsable(ctx context.Context) (codexLogin, error) {
	c, ok := loadCodex()
	if !ok {
		return codexLogin{}, errCodexSignedOut
	}
	if !codexTokenExpiring(c.access, time.Now()) {
		return c, nil
	}
	codexRefreshMu.Lock()
	defer codexRefreshMu.Unlock()
	// Another request may have refreshed while this one waited for the lock.
	c, ok = loadCodex()
	if !ok {
		return codexLogin{}, errCodexSignedOut
	}
	if !codexTokenExpiring(c.access, time.Now()) {
		return c, nil
	}
	if err := refreshCodex(ctx); err != nil {
		return codexLogin{}, fmt.Errorf("%w (token refresh: %v)", errCodexSignedOut, err)
	}
	c, ok = loadCodex()
	if !ok {
		return codexLogin{}, errCodexSignedOut
	}
	return c, nil
}

// refreshCodex exchanges the saved refresh token and writes the reply back
// into auth.json. Callers must hold codexRefreshMu.
func refreshCodex(ctx context.Context) error {
	path := filepath.Join(codexHome(), "auth.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Raw values keep every field exactly as the agent wrote it.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return fmt.Errorf("auth.json is unreadable: %w", err)
	}
	var tokens map[string]json.RawMessage
	if err := json.Unmarshal(top["tokens"], &tokens); err != nil || tokens == nil {
		return errors.New("auth.json has no tokens")
	}
	var refresh string
	_ = json.Unmarshal(tokens["refresh_token"], &refresh)
	if refresh == "" {
		return errors.New("no refresh token is saved")
	}

	reqBody, err := json.Marshal(map[string]string{
		"client_id":     CodexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexRefreshURL, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := codexHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("refresh rejected: %s", APIError(rb, res.Status))
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(rb, &out) != nil || out.AccessToken == "" {
		return errors.New("refresh reply is unreadable")
	}

	set := func(k, v string) error {
		if v == "" {
			return nil
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		tokens[k] = raw
		return nil
	}
	if err := set("access_token", out.AccessToken); err != nil {
		return err
	}
	if err := set("id_token", out.IDToken); err != nil {
		return err
	}
	if err := set("refresh_token", out.RefreshToken); err != nil {
		return err
	}
	tb, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	top["tokens"] = tb
	if _, ok := top["last_refresh"]; ok {
		raw, _ := json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
		top["last_refresh"] = raw
	}
	data, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(data, '\n'))
}

// writePrivate replaces path with data atomically. The new file is created
// with mode 0600 and renamed into place, so readers never see a partial file.
func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auth-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename has succeeded
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// SignedOut is the error for a borrowed login that is gone or no longer
// accepted. The gateway turns it into a 401 that asks the user to sign in.
func SignedOut(p Provider) error {
	if p.Account != nil {
		if e := SignedOutFor(p.Account.Agent); e != nil {
			return e
		}
	}
	return errCodexSignedOut
}

// SignedOutFor is the sign-in error for a borrowed-login id, or nil when id is
// not one. Used for model references whose login is gone.
func SignedOutFor(id string) error {
	switch id {
	case "codex":
		return errCodexSignedOut
	case "copilot":
		return errCopilotSignedOut
	}
	return nil
}

// IsSignedOut reports whether err means a borrowed login has to be signed in again.
func IsSignedOut(err error) bool {
	return errors.Is(err, errCodexSignedOut) || errors.Is(err, errCopilotSignedOut)
}
