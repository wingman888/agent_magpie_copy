package provider

// The Codex account: a ChatGPT login kept by the Codex CLI in ~/.codex/auth.json.
// dial reads that file on every use. When the access token is about to expire
// it refreshes it and writes the new tokens back, keeping every other field.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CodexBase is the ChatGPT backend Codex talks to. A variable so tests can
// point it at a stand-in.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// CodexTokenURL is where refresh tokens are exchanged. A variable so tests can
// point it at a stand-in.
var CodexTokenURL = "https://auth.openai.com/oauth/token"

// codexClientID is the public client id the Codex CLI uses.
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// errCodexSignedOut is the error to show when the ChatGPT login is gone.
var errCodexSignedOut error = signedOutErr{errors.New("codex: 已登出，请重新登录（运行 codex login）")}

// codexMu serialises refreshes, so concurrent requests refresh only once.
// It also guards codexPending.
var codexMu sync.Mutex

// codexPending holds tokens that were refreshed but could not be written back
// to auth.json. They are used in memory until the file is written. from is the
// refresh token that was on disk when the refresh started: if the file no
// longer carries it, the agent has logged in again and the file wins.
type codexPendingTokens struct {
	from  string
	creds codexCreds
}

var codexPending *codexPendingTokens

// codexAuthPath is the agent's auth file, or "" when there is no home
// directory to look in. An empty path means "not signed in", never a relative
// path.
func codexAuthPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".codex", "auth.json")
}

type codexAuthFile struct {
	AuthMode string `json:"auth_mode"`
	Tokens   *struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

// codexCreds is what dial needs from auth.json. It is read, never copied.
type codexCreds struct {
	access, refresh, idToken, accountID string
}

// readCodex reports the ChatGPT login, if there is one. An API-key login is
// not an account.
func readCodex() (codexCreds, bool) {
	path := codexAuthPath()
	if path == "" {
		return codexCreds{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return codexCreds{}, false
	}
	var a codexAuthFile
	if json.Unmarshal(b, &a) != nil || a.Tokens == nil {
		return codexCreds{}, false
	}
	if strings.EqualFold(a.AuthMode, "apikey") || a.Tokens.AccessToken == "" {
		return codexCreds{}, false
	}
	c := codexCreds{
		access:    a.Tokens.AccessToken,
		refresh:   a.Tokens.RefreshToken,
		idToken:   a.Tokens.IDToken,
		accountID: a.Tokens.AccountID,
	}
	if c.accountID == "" {
		if auth, ok := jwtClaims(c.idToken)["https://api.openai.com/auth"].(map[string]any); ok {
			c.accountID, _ = auth["chatgpt_account_id"].(string)
		}
	}
	return c, true
}

// codexAccount takes the display name and plan from the id token.
func codexAccount(c codexCreds) (user, plan string) {
	user = "ChatGPT"
	claims := jwtClaims(c.idToken)
	if e, _ := claims["email"].(string); e != "" {
		user = e
	}
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		plan, _ = auth["chatgpt_plan_type"].(string)
	}
	return user, plan
}

// jwtClaims decodes the payload of a JWT without verifying it. dial only
// reads the claims for display and expiry; the upstream checks the signature.
func jwtClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func jwtExpiry(tok string) (time.Time, bool) {
	exp, ok := jwtClaims(tok)["exp"].(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(exp), 0), true
}

// codexAccessToken returns the current login, refreshing the access token
// first if it expires within five minutes.
func codexAccessToken(ctx context.Context) (codexCreds, error) {
	codexMu.Lock()
	defer codexMu.Unlock()
	// Read again under the lock: another request may just have refreshed.
	c, ok := readCodex()
	if !ok {
		codexPending = nil
		return codexCreds{}, errCodexSignedOut
	}
	diskRefresh := c.refresh
	if p := codexPending; p != nil {
		if p.from == diskRefresh {
			// The file is still the one we refreshed from: use the newer
			// tokens we hold in memory.
			c = p.creds
		} else {
			codexPending = nil
		}
	}
	if exp, ok := jwtExpiry(c.access); !ok || time.Until(exp) > 5*time.Minute {
		return c, nil
	}
	if c.refresh == "" {
		return codexCreds{}, errCodexSignedOut
	}
	nc, err := refreshCodex(ctx, c.refresh)
	if err != nil {
		return codexCreds{}, fmt.Errorf("%w（%v）", errCodexSignedOut, err)
	}
	if nc.access == "" {
		return codexCreds{}, fmt.Errorf("%w（刷新结果没有访问令牌）", errCodexSignedOut)
	}
	if nc.refresh == "" {
		nc.refresh = c.refresh
	}
	if nc.idToken == "" {
		nc.idToken = c.idToken
	}
	nc.accountID = c.accountID
	if err := writeCodexTokens(nc); err != nil {
		// The refresh succeeded, so the new tokens are kept in memory and
		// used from here on, rather than dropped. The file is updated by the
		// next refresh that can write it.
		codexPending = &codexPendingTokens{from: diskRefresh, creds: nc}
		return nc, nil
	}
	codexPending = nil
	return nc, nil
}

// refreshCodex exchanges a refresh token for new tokens.
func refreshCodex(ctx context.Context, refresh string) (codexCreds, error) {
	body, _ := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
		"scope":         "openid profile email",
	})
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexTokenURL, bytes.NewReader(body))
	if err != nil {
		return codexCreds{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return codexCreds{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return codexCreds{}, fmt.Errorf("token refresh failed: %s", res.Status)
	}
	var out struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return codexCreds{}, fmt.Errorf("token refresh: %w", err)
	}
	return codexCreds{access: out.AccessToken, refresh: out.RefreshToken, idToken: out.IDToken}, nil
}

// writeCodexTokens puts new tokens into auth.json. Other fields, including the
// other tokens and any top-level keys, stay as they were. The file is
// replaced atomically and kept mode 0600.
func writeCodexTokens(c codexCreds) error {
	path := codexAuthPath()
	if path == "" {
		return errors.New("codex: 找不到用户主目录")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return err
	}
	if top == nil {
		top = map[string]json.RawMessage{}
	}
	tokens := map[string]json.RawMessage{}
	if raw, ok := top["tokens"]; ok {
		if err := json.Unmarshal(raw, &tokens); err != nil {
			return err
		}
	}
	if tokens == nil {
		// "tokens": null unmarshals to a nil map, which cannot be written to.
		tokens = map[string]json.RawMessage{}
	}
	set := func(m map[string]json.RawMessage, k, v string) {
		r, _ := json.Marshal(v)
		m[k] = r
	}
	set(tokens, "access_token", c.access)
	set(tokens, "refresh_token", c.refresh)
	set(tokens, "id_token", c.idToken)
	top["tokens"], _ = json.Marshal(tokens)
	top["last_refresh"], _ = json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))

	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".auth-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// signCodex points the request at the ChatGPT backend and adds the token and
// account id the backend wants.
func signCodex(ctx context.Context, req *http.Request) error {
	c, err := codexAccessToken(ctx)
	if err != nil {
		return err
	}
	u, err := url.Parse(CodexBase + "/responses")
	if err != nil {
		return err
	}
	req.URL = u
	req.Host = u.Host
	req.Header.Set("Authorization", "Bearer "+c.access)
	if c.accountID != "" {
		req.Header.Set("chatgpt-account-id", c.accountID)
	}
	return nil
}

// prepareCodex makes a Responses request acceptable to the ChatGPT backend:
// streaming, not stored, input as a list of items, no sampling or output
// limits, and the model named without the "codex/" prefix.
func prepareCodex(body []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m == nil {
		return body
	}
	m["stream"] = json.RawMessage("true")
	m["store"] = json.RawMessage("false")
	for _, k := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens", "temperature", "top_p"} {
		delete(m, k)
	}
	if _, ok := m["instructions"]; !ok {
		m["instructions"] = json.RawMessage(`""`)
	}
	// a plain string is one user message, in the same item shape the
	// Responses translator writes
	var input string
	if raw, ok := m["input"]; ok && json.Unmarshal(raw, &input) == nil {
		item := map[string]any{
			"type": "message", "role": "user",
			"content": []map[string]string{{"type": "input_text", "text": input}},
		}
		m["input"], _ = json.Marshal([]any{item})
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return stripModelPrefix(out, "codex/")
}
