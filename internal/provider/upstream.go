package provider

// Upstream access for agent accounts. Every request reads the agent's own
// sign-in; dial keeps no copy. The one write is Codex's token refresh, which
// goes back into Codex's own auth.json with the rest of the file untouched.

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

// CodexRefreshURL is where Codex trades a refresh token for new tokens.
var CodexRefreshURL = "https://auth.openai.com/oauth/token"

// codexClientID is the public client id the Codex CLI refreshes with.
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// codexRefreshWindow: an access token this close to expiry is refreshed first.
const codexRefreshWindow = 5 * time.Minute

// errCodexSignedOut is the message when Codex's sign-in no longer works.
var errCodexSignedOut = errors.New("已登出，请运行 `codex login`")

// refreshMu keeps concurrent requests from rotating the refresh token twice.
var refreshMu sync.Mutex

// SignedOut is the error for an account whose sign-in no longer works. Its
// text tells the user what to run.
func SignedOut(id string) error {
	if id == codexID {
		return errCodexSignedOut
	}
	return errCopilotSignedOut
}

// AccountUpstream returns the base URL and headers for a request to an
// account provider's upstream. For Codex the access token is refreshed first
// when it is near expiry; for Copilot a fresh session is exchanged.
func AccountUpstream(ctx context.Context, p Provider) (string, map[string]string, error) {
	switch p.ID {
	case codexID:
		l, err := codexFresh(ctx)
		if err != nil {
			return "", nil, err
		}
		return CodexBase, map[string]string{
			"Authorization":      "Bearer " + l.AccessToken,
			"chatgpt-account-id": l.AccountID,
		}, nil
	case copilotID:
		login, ok := ReadCopilot()
		if !ok {
			return "", nil, errCopilotSignedOut
		}
		sess, err := copilotSessionFor(ctx, login.Token)
		if err != nil {
			return "", nil, err
		}
		return sess.API, map[string]string{
			"Authorization":          "Bearer " + sess.Token,
			"Copilot-Integration-Id": "vscode-chat",
			"Editor-Version":         "vscode/1.99.0",
		}, nil
	}
	return "", nil, fmt.Errorf("%s is not a signed-in account", p.Name)
}

// codexFresh reads Codex's login and refreshes it when the access token is
// within codexRefreshWindow of expiry. A token whose expiry is unknown is used
// as it is.
func codexFresh(ctx context.Context) (CodexLogin, error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	l, ok := ReadCodex()
	if !ok {
		return CodexLogin{}, errCodexSignedOut
	}
	if l.Expires.IsZero() || time.Until(l.Expires) > codexRefreshWindow {
		return l, nil
	}
	if l.RefreshToken == "" {
		return CodexLogin{}, errCodexSignedOut
	}
	tok, err := refreshCodex(ctx, l.RefreshToken)
	if err != nil {
		return CodexLogin{}, errCodexSignedOut
	}
	// The new tokens are used for this request even if writing them back
	// fails; a later refresh then reports that the user is signed out.
	if err := writeCodexTokens(l.Path, tok); err != nil {
		fresh := l
		fresh.AccessToken = tok.Access
		if tok.Refresh != "" {
			fresh.RefreshToken = tok.Refresh
		}
		if tok.ID != "" {
			fresh.IDToken = tok.ID
		}
		return fresh, nil
	}
	l, ok = ReadCodex()
	if !ok {
		return CodexLogin{}, errCodexSignedOut
	}
	return l, nil
}

// codexTokens is the answer to a refresh.
type codexTokens struct {
	ID      string
	Access  string
	Refresh string
}

// refreshCodex asks Codex's token endpoint for new tokens.
func refreshCodex(ctx context.Context, refresh string) (codexTokens, error) {
	body, _ := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
		"scope":         "openid profile email",
	})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexRefreshURL, bytes.NewReader(body))
	if err != nil {
		return codexTokens{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return codexTokens{}, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return codexTokens{}, err
	}
	if res.StatusCode != http.StatusOK {
		return codexTokens{}, fmt.Errorf("codex refresh: %d", res.StatusCode)
	}
	var v struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(b, &v) != nil || v.AccessToken == "" {
		return codexTokens{}, errors.New("codex refresh: no access token in the answer")
	}
	// an empty refresh token means it was not rotated; the caller keeps its own
	return codexTokens{ID: v.IDToken, Access: v.AccessToken, Refresh: v.RefreshToken}, nil
}

// writeCodexTokens writes new tokens into Codex's auth.json. Only the token
// fields and last_refresh change; every other field is kept as it was.
// A refresh token that was not rotated is left alone.
func writeCodexTokens(path string, t codexTokens) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return err
	}
	tokens := map[string]json.RawMessage{}
	if raw, ok := top["tokens"]; ok {
		if err := json.Unmarshal(raw, &tokens); err != nil {
			return err
		}
	}
	set := func(k, v string) error {
		if v == "" {
			return nil
		}
		j, err := json.Marshal(v)
		if err != nil {
			return err
		}
		tokens[k] = j
		return nil
	}
	for k, v := range map[string]string{"access_token": t.Access, "id_token": t.ID, "refresh_token": t.Refresh} {
		if err := set(k, v); err != nil {
			return err
		}
	}
	tj, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	top["tokens"] = tj
	now, _ := json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
	top["last_refresh"] = now
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	return replaceFile(path, append(out, '\n'), 0o600)
}

// replaceFile writes a file through a temp file and a rename, so a reader
// never sees a half-written login.
func replaceFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".auth-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, path)
}
