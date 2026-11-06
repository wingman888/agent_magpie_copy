package provider

// Codex access tokens expire. When one is about to, dial renews it the way the
// Codex CLI does and writes the new tokens back into ~/.codex/auth.json, so the
// CLI keeps working. Every other field in that file is kept as it was.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CodexTokenURL is OpenAI's OAuth token endpoint, used to renew a Codex sign-in.
// A variable so tests can point it at a stand-in server.
var CodexTokenURL = "https://auth.openai.com/oauth/token"

const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// codexRenewWithin is how close to expiry an access token is renewed.
const codexRenewWithin = 5 * time.Minute

// CodexSignInExpired is returned when the Codex sign-in is gone or can no
// longer be renewed. It matches ErrSignedOut, and its text tells the user what
// to do.
var CodexSignInExpired error = codexSignedOut{msg: `codex sign-in expired: run "codex login" again`}

type codexSignedOut struct{ msg string }

func (e codexSignedOut) Error() string { return e.msg }

// Is makes the error match ErrSignedOut for callers that test the class.
func (e codexSignedOut) Is(target error) bool { return target == ErrSignedOut }

// codexMu serialises renewals: while one request renews, others wait and then
// read the file the renewal already wrote.
var codexMu sync.Mutex

// codexToken returns the Codex sign-in to use for a request, renewing the
// access token first if it is about to expire.
func codexToken(ctx context.Context) (codexCreds, error) {
	c, ok := codexLogin()
	if !ok {
		return codexCreds{}, CodexSignInExpired
	}
	if !expiresSoon(c.AccessToken) {
		return c, nil
	}
	codexMu.Lock()
	defer codexMu.Unlock()
	// Re-read under the lock: another request may have renewed already.
	c, ok = codexLogin()
	if !ok {
		return codexCreds{}, CodexSignInExpired
	}
	if !expiresSoon(c.AccessToken) {
		return c, nil
	}
	if c.Refresh == "" {
		return codexCreds{}, CodexSignInExpired
	}
	t, err := refreshCodex(ctx, c.Refresh)
	if err != nil {
		return codexCreds{}, CodexSignInExpired
	}
	if err := writeCodexTokens(t); err != nil {
		return codexCreds{}, fmt.Errorf("codex: could not save the renewed sign-in: %w", err)
	}
	c.AccessToken = t.Access
	if t.Refresh != "" {
		c.Refresh = t.Refresh
	}
	return c, nil
}

// expiresSoon reports whether a JWT access token expires within codexRenewWithin.
// A token without an exp claim is taken as valid.
func expiresSoon(tok string) bool {
	exp, ok := jwtClaims(tok)["exp"].(float64)
	return ok && time.Until(time.Unix(int64(exp), 0)) < codexRenewWithin
}

// codexTokens are the tokens a renewal returns. Empty Refresh or ID mean the
// server did not send one, and the stored value stays.
type codexTokens struct {
	Access  string
	Refresh string
	ID      string
}

func refreshCodex(ctx context.Context, refresh string) (codexTokens, error) {
	body, err := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
	})
	if err != nil {
		return codexTokens{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexTokenURL, bytes.NewReader(body))
	if err != nil {
		return codexTokens{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := accountHTTP.Do(req)
	if err != nil {
		return codexTokens{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		return codexTokens{}, fmt.Errorf("codex: token renewal: %s", res.Status)
	}
	var v struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if json.Unmarshal(b, &v) != nil || v.AccessToken == "" {
		return codexTokens{}, fmt.Errorf("codex: token renewal returned an unexpected response")
	}
	return codexTokens{Access: v.AccessToken, Refresh: v.RefreshToken, ID: v.IDToken}, nil
}

// writeCodexTokens stores renewed tokens in auth.json, keeping every other
// field, and replaces the file atomically with mode 0600.
func writeCodexTokens(t codexTokens) error {
	p := homeFile(".codex", "auth.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return err
	}
	if m == nil {
		m = map[string]any{}
	}
	tokens, _ := m["tokens"].(map[string]any)
	if tokens == nil {
		tokens = map[string]any{}
	}
	tokens["access_token"] = t.Access
	if t.Refresh != "" {
		tokens["refresh_token"] = t.Refresh
	}
	if t.ID != "" {
		tokens["id_token"] = t.ID
	}
	m["tokens"] = tokens
	m["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(p, append(out, '\n'))
}

// writePrivate replaces a file through a temporary file in the same directory,
// so readers never see a partial write and the mode stays 0600.
func writePrivate(p string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(p), ".auth-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
