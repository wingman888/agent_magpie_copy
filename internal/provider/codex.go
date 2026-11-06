package provider

// Codex login support: token freshness, refresh, and the request shape the
// ChatGPT backend accepts. The agent's own auth file stays the only copy of
// the login; a refresh writes the new tokens back into it.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CodexRefreshURL is the OAuth endpoint that exchanges a refresh token for a
// new access token. A variable so tests can point it at a stand-in server.
var CodexRefreshURL = "https://auth.openai.com/oauth/token"

// codexClientID is the public client id the Codex CLI refreshes with.
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// codexRefreshWithin is how close to expiry an access token is refreshed
// before a request is sent with it.
const codexRefreshWithin = 5 * time.Minute

// ErrCodexSignedOut is returned when the Codex login is missing, was removed,
// or can no longer be refreshed. The caller should ask the user to sign in
// again.
var ErrCodexSignedOut = errors.New("codex: signed out, please run `codex login` again")

// codexRefreshMu serialises refreshes within the process so concurrent
// requests do not spend the same refresh token twice.
var codexRefreshMu sync.Mutex

var codexRefreshClient = &http.Client{Timeout: 30 * time.Second}

// codexAccessExpiry reads the exp claim of the access token. ok is false when
// the token is not a JWT with an exp claim; such a token is used as it is.
func codexAccessExpiry(tok string) (time.Time, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, false
	}
	var c struct {
		Exp float64 `json:"exp"`
	}
	if json.Unmarshal(payload, &c) != nil || c.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(c.Exp), 0), true
}

func codexNeedsRefresh(a codexAuth) bool {
	exp, ok := codexAccessExpiry(a.Tokens.AccessToken)
	return ok && time.Until(exp) < codexRefreshWithin
}

// codexToken returns the current Codex login, refreshing the access token
// first when it is about to expire. Any failure to refresh is reported as
// ErrCodexSignedOut.
func codexToken(ctx context.Context) (codexAuth, error) {
	a, ok := readCodex()
	if !ok {
		return codexAuth{}, ErrCodexSignedOut
	}
	if !codexNeedsRefresh(a) {
		return a, nil
	}
	codexRefreshMu.Lock()
	defer codexRefreshMu.Unlock()
	// Another request may have refreshed while this one waited.
	a, ok = readCodex()
	if !ok {
		return codexAuth{}, ErrCodexSignedOut
	}
	if !codexNeedsRefresh(a) {
		return a, nil
	}
	if err := refreshCodex(ctx); err != nil {
		return codexAuth{}, fmt.Errorf("%w (refresh failed: %v)", ErrCodexSignedOut, err)
	}
	a, ok = readCodex()
	if !ok {
		return codexAuth{}, ErrCodexSignedOut
	}
	return a, nil
}

// refreshCodex trades the stored refresh token for new tokens and writes them
// back into auth.json. Every other field in the file is kept as it was. The
// caller must hold codexRefreshMu.
func refreshCodex(ctx context.Context) error {
	path := codexAuthPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return fmt.Errorf("auth.json: %w", err)
	}
	var tokens map[string]json.RawMessage
	if err := json.Unmarshal(top["tokens"], &tokens); err != nil || tokens == nil {
		return errors.New("auth.json has no tokens")
	}
	var cur struct {
		RefreshToken string `json:"refresh_token"`
	}
	json.Unmarshal(tokens["refresh_token"], &cur.RefreshToken)
	if cur.RefreshToken == "" {
		return errors.New("no refresh token stored")
	}

	reqBody, _ := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": cur.RefreshToken,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexRefreshURL, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := codexRefreshClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("token endpoint answered %s", res.Status)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if json.Unmarshal(rb, &out) != nil || out.AccessToken == "" {
		return errors.New("token endpoint gave no access token")
	}

	set := func(m map[string]json.RawMessage, k, v string) {
		b, _ := json.Marshal(v)
		m[k] = b
	}
	set(tokens, "access_token", out.AccessToken)
	if out.RefreshToken != "" {
		set(tokens, "refresh_token", out.RefreshToken)
	}
	if out.IDToken != "" {
		set(tokens, "id_token", out.IDToken)
	}
	tb, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	top["tokens"] = tb
	set(top, "last_refresh", time.Now().UTC().Format(time.RFC3339Nano))

	b, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o600)
}

// writeFileAtomic replaces path through a temporary file in the same
// directory, so a reader never sees a half-written file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".auth-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// prepareCodex shapes a Responses request for the ChatGPT backend: it must
// stream, must not be stored, and takes input as a list of items. Sampling
// and output-limit parameters are removed; reasoning settings stay.
func prepareCodex(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	m["stream"] = true
	m["store"] = false
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": s,
			}},
		}}
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p", "max_tokens", "max_completion_tokens"} {
		delete(m, k)
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
