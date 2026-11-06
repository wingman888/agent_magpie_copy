package provider

// Codex's ChatGPT access token expires; the Codex CLI renews it with the
// refresh token in the same auth.json. dial does the same when a request is
// about to need a token that is nearly spent, and writes the result back to
// that file so the Codex CLI keeps working. Every other field is kept.

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

// CodexRefreshURL is the OAuth endpoint a Codex refresh token is exchanged at.
// A variable so tests can point it at a stand-in server.
var CodexRefreshURL = "https://auth.openai.com/oauth/token"

// codexClientID is the public client id the Codex CLI signs in with.
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// codexRefreshAhead is how close to expiry the access token is renewed.
const codexRefreshAhead = 5 * time.Minute

// codexMu serialises refreshes within this process, so concurrent requests
// renew the token once.
var codexMu sync.Mutex

// errCodexExpired is what a caller sees when the Codex sign-in cannot be renewed.
var errCodexExpired = errors.New("Codex sign-in expired, run `codex login` again")

// codexTokens is what a refresh returned. Empty fields keep their old value.
type codexTokens struct {
	access, refresh, id string
}

// codexAuthPath is the file the Codex CLI keeps its sign-in in.
func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// tokenExpiry reads the exp claim of a JWT.
func tokenExpiry(tok string) (time.Time, bool) {
	exp, ok := jwtClaims(tok)["exp"].(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(exp), 0), true
}

// codexDue is true when the access token expires within codexRefreshAhead.
// Without a readable expiry it is never due: the token is used as it is.
func codexDue(l codexLogin) bool {
	exp, ok := tokenExpiry(l.token)
	return ok && time.Until(exp) < codexRefreshAhead
}

// codexFresh returns the Codex sign-in to use for a request, renewing the
// access token first when it is about to expire.
func codexFresh(ctx context.Context) (codexLogin, error) {
	l, ok := readCodex()
	if !ok {
		return codexLogin{}, signedOutErr(codexID)
	}
	if !codexDue(l) {
		return l, nil
	}
	codexMu.Lock()
	defer codexMu.Unlock()
	// A request that held the lock before us may have renewed it already.
	if l, ok = readCodex(); !ok {
		return codexLogin{}, signedOutErr(codexID)
	}
	if !codexDue(l) {
		return l, nil
	}
	if l.refresh == "" {
		return codexLogin{}, errCodexExpired
	}
	t, err := refreshCodex(ctx, l.refresh)
	if err != nil {
		return codexLogin{}, err
	}
	if err := writeCodexTokens(t); err != nil {
		return codexLogin{}, fmt.Errorf("codex: could not save the renewed sign-in: %w", err)
	}
	l.token = t.access
	if t.id != "" {
		l.idToken = t.id
	}
	if t.refresh != "" {
		l.refresh = t.refresh
	}
	return l, nil
}

// refreshCodex exchanges a refresh token for new tokens.
func refreshCodex(ctx context.Context, refresh string) (codexTokens, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, err := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token", "refresh_token": refresh})
	if err != nil {
		return codexTokens{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexRefreshURL, bytes.NewReader(body))
	if err != nil {
		return codexTokens{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return codexTokens{}, fmt.Errorf("%w (%v)", errCodexExpired, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return codexTokens{}, fmt.Errorf("%w (%s)", errCodexExpired, APIError(b, res.Status))
	}
	var v struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if json.Unmarshal(b, &v) != nil || v.AccessToken == "" {
		return codexTokens{}, fmt.Errorf("%w (unexpected reply)", errCodexExpired)
	}
	return codexTokens{access: v.AccessToken, refresh: v.RefreshToken, id: v.IDToken}, nil
}

// writeCodexTokens updates the token fields of auth.json in place: other
// fields, including unknown ones, are kept as they were. The file is replaced
// atomically and stays mode 0600.
func writeCodexTokens(t codexTokens) error {
	path := codexAuthPath()
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return errors.New("auth.json is not a JSON object")
	}
	tokens, _ := m["tokens"].(map[string]any)
	if tokens == nil {
		tokens = map[string]any{}
	}
	tokens["access_token"] = t.access
	if t.refresh != "" {
		tokens["refresh_token"] = t.refresh
	}
	if t.id != "" {
		tokens["id_token"] = t.id
	}
	m["tokens"] = tokens
	m["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auth-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(out, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
