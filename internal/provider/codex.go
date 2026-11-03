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
	"strings"
	"sync"
	"time"
)

// CodexAuthURL is the token endpoint a Codex login refreshes against. Tests
// point it at a local fake server.
var CodexAuthURL = "https://auth.openai.com/oauth/token"

// codexClientID is the public client id the Codex CLI refreshes with.
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// codexRefreshWithin is how close to expiry an access token is refreshed.
const codexRefreshWithin = 5 * time.Minute

// errCodexLoggedOut is what the user is told when the Codex login is gone or
// can no longer be refreshed.
var errCodexLoggedOut = errors.New("已登出，请运行 `codex login`")

// codexMu serializes refreshes. Refresh tokens rotate, so two refreshes racing
// each other could leave the login with a token that is already spent.
var codexMu sync.Mutex

// codexFile is Codex's own login file, ~/.codex/auth.json.
func codexFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// codexAccess gives the access token and account id to send to Codex. When the
// access token is within codexRefreshWithin of expiry, the login is refreshed
// first and the new tokens are written back into Codex's own file.
func codexAccess(ctx context.Context) (token, account string, err error) {
	codexMu.Lock()
	defer codexMu.Unlock()
	c, ok := codexLogin()
	if !ok {
		return "", "", errCodexLoggedOut
	}
	if !c.Expires.IsZero() && time.Until(c.Expires) < codexRefreshWithin {
		if c.Refresh == "" {
			return "", "", errCodexLoggedOut
		}
		if err := refreshCodex(ctx, c.Refresh); err != nil {
			return "", "", fmt.Errorf("%w（刷新失败：%v）", errCodexLoggedOut, err)
		}
		if c, ok = codexLogin(); !ok {
			return "", "", errCodexLoggedOut
		}
	}
	return c.Access, c.AccountID, nil
}

// refreshCodex trades the refresh token for new tokens, the way Codex does.
func refreshCodex(ctx context.Context, refresh string) error {
	body, _ := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
	})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexAuthURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return errors.New(APIError(b, res.Status))
	}
	var t struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(b, &t) != nil || t.AccessToken == "" {
		return errors.New("the token endpoint returned no access token")
	}
	return writeCodexTokens(t.IDToken, t.AccessToken, t.RefreshToken, time.Now())
}

// writeCodexTokens stores refreshed tokens in Codex's login file. Every other
// field is kept as it was; last_refresh is updated when the file has one.
func writeCodexTokens(idTok, access, refresh string, now time.Time) error {
	path := codexFile()
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var f map[string]any
	if err := dec.Decode(&f); err != nil {
		return err
	}
	if f == nil {
		f = map[string]any{}
	}
	tokens, _ := f["tokens"].(map[string]any)
	if tokens == nil {
		tokens = map[string]any{}
	}
	if idTok != "" {
		tokens["id_token"] = idTok
	}
	tokens["access_token"] = access
	if refresh != "" {
		tokens["refresh_token"] = refresh
	}
	f["tokens"] = tokens
	if _, ok := f["last_refresh"]; ok {
		f["last_refresh"] = now.UTC().Format(time.RFC3339Nano)
	}
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// codexDropped are the request fields the ChatGPT Codex backend refuses:
// sampling settings and output-length limits.
var codexDropped = []string{
	"max_output_tokens", "max_completion_tokens", "max_tokens",
	"temperature", "top_p", "frequency_penalty", "presence_penalty", "seed", "stop",
}

// CodexBody rewrites a Responses request for the ChatGPT Codex backend: the
// model loses its codex/ prefix, stream is on and store is off, the refused
// fields are removed, and a bare string input becomes a list of items. A body
// that does not parse is returned unchanged.
func CodexBody(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	if s, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(s, "codex/")
	}
	m["stream"] = true
	m["store"] = false
	for _, k := range codexDropped {
		delete(m, k)
	}
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": s}},
		}}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
