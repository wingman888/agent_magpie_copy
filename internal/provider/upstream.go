package provider

// Upstream access for sign-in providers. Each request reads the agent's own
// login file afresh; nothing read here is kept by dial. The only write is
// Codex's refreshed tokens, which go back into Codex's own auth.json.

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

// CodexRefreshURL is where Codex trades its refresh token for new tokens.
// It is a variable so tests can aim it at a local server.
var CodexRefreshURL = "https://auth.openai.com/oauth/token"

// codexClientID is the OAuth client Codex itself refreshes with.
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// codexRefreshAhead is how early before expiry Codex's token is refreshed.
const codexRefreshAhead = 5 * time.Minute

var (
	errCodexLoggedOut = errors.New("已登出，请运行 `codex login`")
	errCodexNotSigned = errors.New("未登录 Codex：运行 `codex login`")
)

// SignInExpired is the error for an upstream that rejected the login of a
// sign-in provider. It tells the user how to get the login back.
func SignInExpired(id string) error {
	switch id {
	case "codex":
		return errCodexLoggedOut
	case "copilot":
		return errReloginCopilot
	}
	return errNotSignedIn
}

// SignInUpstream returns the full URL and the auth headers for one request
// to a sign-in provider's own backend. path is the protocol path (for
// example "/responses" for Codex, "/chat/completions" for Copilot), and it is
// appended to the backend's base address.
func SignInUpstream(ctx context.Context, id, path string) (string, map[string]string, error) {
	switch id {
	case "codex":
		c, err := codexToken(ctx)
		if err != nil {
			return "", nil, err
		}
		h := map[string]string{"Authorization": "Bearer " + c.AccessToken}
		if c.AccountID != "" {
			h["chatgpt-account-id"] = c.AccountID
		}
		return CodexBase + path, h, nil
	case "copilot":
		app, err := readCopilot()
		if err != nil {
			return "", nil, errors.New("未登录 Copilot：在编辑器或 Copilot CLI 中登录 GitHub")
		}
		session, base, err := copilotSession(ctx, app.OAuthToken)
		if err != nil {
			return "", nil, err
		}
		return base + path, map[string]string{
			"Authorization":          "Bearer " + session,
			"Copilot-Integration-Id": "vscode-chat",
		}, nil
	}
	return "", nil, errNotSignedIn
}

// codexToken reads Codex's login and refreshes it when the access token is
// close to expiry.
func codexToken(ctx context.Context) (codexCred, error) {
	c, err := readCodex()
	if err != nil {
		if errors.Is(err, errCodexAPIKey) {
			return codexCred{}, errors.New("Codex 是 API Key 模式，不是 ChatGPT 账号，dial 无法使用它的登录")
		}
		return codexCred{}, errCodexNotSigned
	}
	if c.Expires.IsZero() || time.Until(c.Expires) > codexRefreshAhead {
		return c, nil
	}
	return refreshCodex(ctx, c)
}

// refreshCodex runs Codex's OAuth refresh and writes the new tokens back into
// Codex's auth.json, keeping every other field there as it was.
func refreshCodex(ctx context.Context, c codexCred) (codexCred, error) {
	if c.RefreshToken == "" {
		return codexCred{}, errCodexLoggedOut
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": c.RefreshToken,
		"scope":         "openid profile email",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexRefreshURL, bytes.NewReader(body))
	if err != nil {
		return codexCred{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return codexCred{}, errCodexLoggedOut
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return codexCred{}, errCodexLoggedOut
	}
	var v struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(b, &v); err != nil || v.AccessToken == "" {
		return codexCred{}, errCodexLoggedOut
	}
	if v.RefreshToken == "" {
		v.RefreshToken = c.RefreshToken
	}
	if err := writeCodexTokens(v.IDToken, v.AccessToken, v.RefreshToken); err != nil {
		return codexCred{}, fmt.Errorf("Codex 已刷新登录，但无法写回 %s：%v", codexPath(), err)
	}
	c.AccessToken, c.RefreshToken = v.AccessToken, v.RefreshToken
	if claims, err := jwtClaims(v.AccessToken); err == nil {
		if exp, ok := claims["exp"].(float64); ok {
			c.Expires = time.Unix(int64(exp), 0)
		}
	}
	return c, nil
}

// writeCodexTokens rewrites the three tokens in Codex's auth.json. An empty
// id token leaves the stored one in place.
func writeCodexTokens(idToken, accessToken, refreshToken string) error {
	path := codexPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return err
	}
	tokens, ok := m["tokens"].(map[string]any)
	if !ok {
		return errors.New("auth.json has no tokens")
	}
	if idToken != "" {
		tokens["id_token"] = idToken
	}
	tokens["access_token"] = accessToken
	tokens["refresh_token"] = refreshToken
	if _, ok := m["last_refresh"]; ok {
		m["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}
