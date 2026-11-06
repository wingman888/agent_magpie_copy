package provider

// Upstream access for account providers: how to get a usable token from the
// agent's own login each time one is needed. Nothing is cached in dial's own
// storage. The Codex refresh writes the new tokens back into Codex's login
// file, keeping every other field it holds.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// CodexRefreshURL is the OAuth token endpoint Codex refreshes its login at.
// Tests point it at a local fake server.
var CodexRefreshURL = "https://auth.openai.com/oauth/token"

// codexClientID is the client id the Codex CLI uses for its ChatGPT login.
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// codexRefreshWithin is how close to expiry an access token is refreshed.
const codexRefreshWithin = 5 * time.Minute

// ErrCodexSignOut is returned when the Codex login can no longer be used.
var ErrCodexSignOut = errors.New("已登出，请运行 `codex login`（signed out of Codex: run codex login）")

// ErrCopilotSignIn is returned when Copilot rejects the stored GitHub login.
var ErrCopilotSignIn = errCopilotSignIn

// CodexAccess returns a usable ChatGPT access token and the account id for
// the Codex login, refreshing the token first when it is about to lapse.
func CodexAccess(ctx context.Context) (token, accountID string, err error) {
	path := codexAuthPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", errors.New("not signed in to Codex — run codex login")
	}
	var f codexAuth
	if json.Unmarshal(raw, &f) != nil || f.AuthMode != "chatgpt" || f.Tokens.AccessToken == "" {
		return "", "", errors.New("not signed in to Codex with a ChatGPT login — run codex login")
	}
	exp, hasExp := jwtClaims(f.Tokens.AccessToken)["exp"].(float64)
	if !hasExp || time.Until(time.Unix(int64(exp), 0)) > codexRefreshWithin {
		return f.Tokens.AccessToken, f.Tokens.AccountID, nil
	}
	tok, err := refreshCodex(ctx, f.Tokens.RefreshToken)
	if err != nil {
		return "", "", fmt.Errorf("%w (%v)", ErrCodexSignOut, err)
	}
	if err := writeCodexTokens(path, tok); err != nil {
		return "", "", fmt.Errorf("save refreshed Codex login: %w", err)
	}
	return tok.AccessToken, f.Tokens.AccountID, nil
}

// codexTokens is what the refresh endpoint hands back. Fields it omits keep
// their previous value.
type codexTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func refreshCodex(ctx context.Context, refresh string) (codexTokens, error) {
	if refresh == "" {
		return codexTokens{}, errors.New("no refresh token in the Codex login")
	}
	body, _ := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
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
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return codexTokens{}, fmt.Errorf("token refresh HTTP %d", res.StatusCode)
	}
	var tok codexTokens
	if err := json.Unmarshal(b, &tok); err != nil {
		return codexTokens{}, fmt.Errorf("token refresh: %w", err)
	}
	if tok.AccessToken == "" {
		return codexTokens{}, errors.New("token refresh returned no access token")
	}
	return tok, nil
}

// writeCodexTokens puts refreshed tokens into Codex's auth file. Every other
// field in the file is kept as it was.
func writeCodexTokens(path string, tok codexTokens) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	toks, _ := doc["tokens"].(map[string]any)
	if toks == nil {
		toks = map[string]any{}
	}
	set := func(k, v string) {
		if v != "" {
			toks[k] = v
		}
	}
	set("id_token", tok.IDToken)
	set("access_token", tok.AccessToken)
	set("refresh_token", tok.RefreshToken)
	doc["tokens"] = toks
	doc["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// CopilotSession trades the stored GitHub login for a Copilot session token
// and the API base the session names. Each call asks afresh.
func CopilotSession(ctx context.Context) (session, api string, err error) {
	login, ok := readCopilotLogin()
	if !ok {
		return "", "", errors.New("not signed in to GitHub Copilot — sign in from your editor or the copilot CLI")
	}
	return copilotSession(ctx, login.OAuth)
}
