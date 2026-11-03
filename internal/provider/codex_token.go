package provider

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

// Codex access tokens are refreshed by dial when they are about to expire,
// the way the Codex CLI refreshes them. The refreshed tokens go back into the
// agent's own auth.json, keeping every other field it holds, so the Codex CLI
// keeps working.

// Where the refresh happens. Variables so tests can point them at a stand-in.
var (
	CodexRefreshURL = "https://auth.openai.com/oauth/token"
	codexClientID   = "app_EMoamEEZ73f0CkXaXp7hrann"
)

// codexRefreshWindow is how close to expiry a token is refreshed.
const codexRefreshWindow = 5 * time.Minute

// ErrSignedOut means the Codex sign-in is gone or can no longer be used. The
// gateway turns it into a 401 that tells the user to sign in again.
var ErrSignedOut = errors.New("codex is signed out: sign in again with `codex login`")

// codexRefreshMu serializes refreshes: a refresh token may be single-use, so
// two requests must not both spend it.
var codexRefreshMu sync.Mutex

var codexHTTP = &http.Client{Timeout: 30 * time.Second}

// codexTokenFresh returns the current sign-in. When its access token expires
// within codexRefreshWindow it refreshes first, under a lock, and checks again
// once the lock is held in case another request already did it.
func codexTokenFresh(ctx context.Context) (codexLogin, error) {
	c, ok := readCodex()
	if !ok {
		return codexLogin{}, ErrSignedOut
	}
	if !codexExpiring(c.token, time.Now()) {
		return c, nil
	}
	codexRefreshMu.Lock()
	defer codexRefreshMu.Unlock()
	if c, ok = readCodex(); !ok {
		return codexLogin{}, ErrSignedOut
	}
	if !codexExpiring(c.token, time.Now()) {
		return c, nil
	}
	if err := refreshCodex(ctx); err != nil {
		return codexLogin{}, fmt.Errorf("%w (token refresh failed: %v)", ErrSignedOut, err)
	}
	c, ok = readCodex()
	if !ok {
		return codexLogin{}, ErrSignedOut
	}
	return c, nil
}

// codexExpiring reports whether an access token is within the refresh window.
// A token without a readable exp claim is not treated as expiring.
func codexExpiring(token string, now time.Time) bool {
	exp, ok := jwtClaims(token)["exp"].(float64)
	if !ok {
		return false
	}
	return time.Unix(int64(exp), 0).Sub(now) < codexRefreshWindow
}

// refreshCodex exchanges the refresh token for new tokens and writes them
// back. Callers hold codexRefreshMu.
func refreshCodex(ctx context.Context) error {
	raw, tokens, err := readCodexFile()
	if err != nil {
		return err
	}
	refresh, _ := tokens["refresh_token"].(string)
	if refresh == "" {
		return errors.New("no refresh token in " + codexPath())
	}
	body, _ := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexRefreshURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := codexHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("refresh endpoint answered %s", res.Status)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return fmt.Errorf("refresh reply: %v", err)
	}
	if out.AccessToken == "" {
		return errors.New("refresh reply has no access token")
	}
	tokens["access_token"] = out.AccessToken
	if out.IDToken != "" {
		tokens["id_token"] = out.IDToken
	}
	if out.RefreshToken != "" {
		tokens["refresh_token"] = out.RefreshToken
	}
	raw["tokens"] = tokens
	raw["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	return writeCodexFile(raw)
}

// readCodexFile parses auth.json as generic JSON so that writing it back
// keeps fields dial does not know about. Numbers are kept as they were.
func readCodexFile() (map[string]any, map[string]any, error) {
	b, err := os.ReadFile(codexPath())
	if err != nil {
		return nil, nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil || raw == nil {
		return nil, nil, fmt.Errorf("%s is not a JSON object", codexPath())
	}
	tokens, ok := raw["tokens"].(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("%s has no tokens", codexPath())
	}
	return raw, tokens, nil
}

// writeCodexFile replaces auth.json atomically: a private temp file in the
// same directory, then a rename over the original.
func writeCodexFile(raw map[string]any) error {
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	path := codexPath()
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auth-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
