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
	"time"
)

// This file is how dial talks to the upstreams of account providers. Every
// call reads the agent's own login again; nothing here caches a credential.

// CodexRefreshURL is where a Codex refresh token is exchanged. A variable so
// tests can point it at a local fake server.
var CodexRefreshURL = "https://auth.openai.com/oauth/token"

// codexClientID is the OAuth client Codex itself refreshes with.
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// codexRefreshWindow is how close to expiry a Codex access token is renewed.
const codexRefreshWindow = 5 * time.Minute

// errCodexSignedOut means the Codex login is gone or can no longer be renewed.
var errCodexSignedOut = errors.New("Codex 已登出，请运行 `codex login`")

// Upstream is where and how to reach an account provider right now.
type Upstream struct {
	Base    string            // API base, without the endpoint path
	Headers map[string]string // credentials and identity headers
}

// SignedOut is the error to report when the upstream rejects the login.
func (p Provider) SignedOut() error {
	if p.ID == CopilotID {
		return errCopilotSignedOut
	}
	return errCodexSignedOut
}

// AccountUpstream resolves the upstream for an account provider from the
// agent's login as it is now, refreshing the Codex token when it is near
// expiry.
func (p Provider) AccountUpstream(ctx context.Context) (Upstream, error) {
	switch p.ID {
	case CodexID:
		tok, acct, err := codexAccessToken(ctx)
		if err != nil {
			return Upstream{}, err
		}
		return Upstream{Base: CodexBase, Headers: map[string]string{
			"Authorization":      "Bearer " + tok,
			"chatgpt-account-id": acct,
		}}, nil
	case CopilotID:
		_, oauth, ok := copilotLogin()
		if !ok {
			return Upstream{}, errCopilotSignedOut
		}
		sess, err := copilotSessionFor(ctx, oauth)
		if err != nil {
			return Upstream{}, err
		}
		return Upstream{Base: strings.TrimRight(sess.Endpoints.API, "/"), Headers: map[string]string{
			"Authorization": "Bearer " + sess.Token,
		}}, nil
	}
	return Upstream{}, fmt.Errorf("%s is not an account provider", p.Name)
}

// ---- Codex credentials -------------------------------------------------------

// codexAccessToken returns a usable access token and the account id. It
// refreshes the token, writing the new tokens back to Codex's own file, when
// the current one expires within codexRefreshWindow.
func codexAccessToken(ctx context.Context) (string, string, error) {
	raw, err := os.ReadFile(codexPath())
	if err != nil {
		return "", "", errCodexSignedOut
	}
	var a codexAuth
	if json.Unmarshal(raw, &a) != nil || a.AuthMode != "chatgpt" || a.Tokens.AccessToken == "" {
		return "", "", errCodexSignedOut
	}
	if !codexExpiresSoon(a.Tokens.AccessToken, time.Now()) {
		return a.Tokens.AccessToken, a.Tokens.AccountID, nil
	}
	fresh, err := refreshCodex(ctx, a.Tokens.RefreshToken)
	if err != nil {
		return "", "", err
	}
	if err := writeCodexTokens(codexPath(), raw, fresh); err != nil {
		return "", "", fmt.Errorf("Codex 令牌已刷新但无法写回 %s: %v", codexPath(), err)
	}
	return fresh.AccessToken, a.Tokens.AccountID, nil
}

// codexExpiresSoon reports whether the access token's exp claim falls inside
// the refresh window. A token without a readable exp is used as it is.
func codexExpiresSoon(accessToken string, now time.Time) bool {
	exp, ok := jwtPayload(accessToken)["exp"].(float64)
	if !ok {
		return false
	}
	return time.Unix(int64(exp), 0).Sub(now) < codexRefreshWindow
}

// codexTokens is what a refresh returns. Fields the server leaves out keep
// their old value.
type codexTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// refreshCodex trades a refresh token for new tokens, the way Codex does.
func refreshCodex(ctx context.Context, refresh string) (codexTokens, error) {
	var t codexTokens
	if refresh == "" {
		return t, errCodexSignedOut
	}
	body, _ := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
	})
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexRefreshURL, bytes.NewReader(body))
	if err != nil {
		return t, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return t, fmt.Errorf("%w（刷新失败：%v）", errCodexSignedOut, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return t, errCodexSignedOut
	}
	if json.Unmarshal(b, &t) != nil || t.AccessToken == "" {
		return t, errCodexSignedOut
	}
	return t, nil
}

// writeCodexTokens puts fresh tokens into Codex's auth file. Every other
// field, at the top level or inside tokens, is kept byte for byte.
func writeCodexTokens(path string, raw []byte, fresh codexTokens) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return err
	}
	tokens := map[string]json.RawMessage{}
	if t, ok := top["tokens"]; ok {
		if err := json.Unmarshal(t, &tokens); err != nil {
			return err
		}
	}
	set := func(key, val string) error {
		if val == "" {
			return nil
		}
		b, err := json.Marshal(val)
		if err != nil {
			return err
		}
		tokens[key] = b
		return nil
	}
	if err := set("id_token", fresh.IDToken); err != nil {
		return err
	}
	if err := set("access_token", fresh.AccessToken); err != nil {
		return err
	}
	if err := set("refresh_token", fresh.RefreshToken); err != nil {
		return err
	}
	tb, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	top["tokens"] = tb
	if now, err := json.Marshal(time.Now().UTC().Format(time.RFC3339Nano)); err == nil {
		top["last_refresh"] = now
	}
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	// write to a sibling and rename, so a crash never leaves a half file
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".auth-*.json")
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

// ---- Codex request shape ------------------------------------------------------

// codexDropped are the sampling and output-length fields the ChatGPT backend
// refuses.
var codexDropped = []string{
	"max_output_tokens", "max_completion_tokens", "max_tokens",
	"temperature", "top_p", "top_k", "presence_penalty", "frequency_penalty",
}

// CodexBody reshapes a Responses request for the ChatGPT Codex backend: the
// model loses its codex/ prefix, the request streams and is not stored, the
// refused fields go, and a bare string input becomes a list of items. model
// overrides the body's own model when it is not empty.
func CodexBody(body []byte, model string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if model == "" {
		model, _ = m["model"].(string)
	}
	m["model"] = strings.TrimPrefix(model, CodexID+"/")
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
	return json.Marshal(m)
}

// ---- Copilot request headers ----------------------------------------------------

// CopilotHeaders are the per-request headers Copilot wants for a Chat
// Completions body. X-Initiator is "agent" when the turn returns a tool
// result (the last message is a tool message, or an assistant message that
// carries tool calls) and "user" when the user typed it. A request with an
// image is flagged as a vision request.
func CopilotHeaders(body []byte) map[string]string {
	h := map[string]string{"X-Initiator": "user"}
	var v struct {
		Messages []struct {
			Role      string          `json:"role"`
			ToolCalls json.RawMessage `json:"tool_calls"`
			Content   json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil || len(v.Messages) == 0 {
		return h
	}
	last := v.Messages[len(v.Messages)-1]
	if last.Role == "tool" || (last.Role == "assistant" && hasJSONItems(last.ToolCalls)) {
		h["X-Initiator"] = "agent"
	}
	for _, m := range v.Messages {
		if hasImagePart(m.Content) {
			h["Copilot-Vision-Request"] = "true"
			break
		}
	}
	return h
}

// hasJSONItems is true for a non-empty JSON array.
func hasJSONItems(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && string(t) != "null" && string(t) != "[]"
}

// hasImagePart is true when message content is a list holding an image.
func hasImagePart(raw json.RawMessage) bool {
	var parts []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return false
	}
	for _, p := range parts {
		switch p.Type {
		case "image_url", "input_image", "image":
			return true
		}
	}
	return false
}
