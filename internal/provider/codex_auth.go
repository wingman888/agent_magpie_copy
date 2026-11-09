package provider

// Codex sign-in upkeep. dial reads ~/.codex/auth.json on every request. When the
// access token is about to expire, it refreshes the token the way the Codex CLI
// does and writes the new tokens back into that same file, keeping every other
// field. Nothing is cached in dial's own storage.

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

// codexClientID is the public OAuth client id the Codex CLI uses.
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// codexRefreshURL is the token endpoint. A var so tests can point it at a fake.
var codexRefreshURL = "https://auth.openai.com/oauth/token"

// codexRefreshWithin is how close to expiry an access token gets refreshed.
const codexRefreshWithin = 5 * time.Minute

// ErrCodexSignedOut means the Codex sign-in is gone and cannot be renewed.
var ErrCodexSignedOut = errors.New("codex: 已登出，请运行 `codex login`")

// codexMu keeps two requests from refreshing the rotating refresh token at once.
var codexMu sync.Mutex

type codexTokens struct {
	IDToken      string
	AccessToken  string
	RefreshToken string
}

// CodexAuth returns the bearer token and account id for a request to the Codex
// backend. The sign-in is read from the file every time; an access token within
// codexRefreshWithin of expiry is refreshed and written back first.
func CodexAuth(ctx context.Context) (token, accountID string, err error) {
	codexMu.Lock()
	defer codexMu.Unlock()
	c, ok := readCodex()
	if !ok {
		return "", "", ErrCodexSignedOut
	}
	if !c.Expires.IsZero() && time.Until(c.Expires) < codexRefreshWithin {
		if c.RefreshToken == "" {
			return "", "", ErrCodexSignedOut
		}
		fresh, rerr := refreshCodex(ctx, c.RefreshToken)
		if rerr != nil {
			return "", "", fmt.Errorf("%w（刷新失败：%v）", ErrCodexSignedOut, rerr)
		}
		if werr := writeCodexTokens(fresh); werr != nil {
			return "", "", fmt.Errorf("codex: could not save the refreshed sign-in: %w", werr)
		}
		c.AccessToken = fresh.AccessToken
	}
	return c.AccessToken, c.AccountID, nil
}

// refreshCodex exchanges a refresh token for new tokens, as the Codex CLI does.
func refreshCodex(ctx context.Context, refresh string) (codexTokens, error) {
	body, _ := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
	})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexRefreshURL, bytes.NewReader(body))
	if err != nil {
		return codexTokens{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return codexTokens{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return codexTokens{}, fmt.Errorf("token endpoint answered %s", res.Status)
	}
	var t struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return codexTokens{}, err
	}
	if t.AccessToken == "" {
		return codexTokens{}, errors.New("token endpoint sent no access token")
	}
	if t.RefreshToken == "" {
		t.RefreshToken = refresh
	}
	return codexTokens{IDToken: t.IDToken, AccessToken: t.AccessToken, RefreshToken: t.RefreshToken}, nil
}

// writeCodexTokens updates the tokens in ~/.codex/auth.json and leaves every
// other field as it was. The file is replaced atomically.
func writeCodexTokens(t codexTokens) error {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".codex", "auth.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	if doc == nil {
		return errors.New("auth.json is not an object")
	}
	tokens, _ := doc["tokens"].(map[string]any)
	if tokens == nil {
		tokens = map[string]any{}
	}
	tokens["access_token"] = t.AccessToken
	tokens["refresh_token"] = t.RefreshToken
	if t.IDToken != "" {
		tokens["id_token"] = t.IDToken
	}
	doc["tokens"] = tokens
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".dial-tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
