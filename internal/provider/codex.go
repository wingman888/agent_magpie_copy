package provider

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
	"time"
)

// CodexBase is the ChatGPT Codex backend; codex/<model> requests go to
// CodexBase + "/responses". Tests point it at a local fake server.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// CodexAuthBase is where a Codex login's tokens are refreshed.
var CodexAuthBase = "https://auth.openai.com"

// codexClientID is the public client id the Codex CLI refreshes tokens with.
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// ErrCodexLoggedOut is what a user sees when the Codex login cannot be used.
var ErrCodexLoggedOut = errors.New("Codex 已登出，请运行 `codex login` 重新登录")

// codexRefreshWithin is how close to expiry a token is refreshed, as Codex does.
const codexRefreshWithin = 5 * time.Minute

func codexPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

type codexAuth struct {
	AuthMode string `json:"auth_mode"`
	Tokens   *struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

// readCodexAuth reads Codex's own login file. Only a ChatGPT login is an
// account; an API-key login is not.
func readCodexAuth() (codexAuth, bool) {
	var a codexAuth
	b, err := os.ReadFile(codexPath())
	if err != nil || json.Unmarshal(b, &a) != nil {
		return a, false
	}
	if a.AuthMode != "chatgpt" || a.Tokens == nil || a.Tokens.AccessToken == "" {
		return a, false
	}
	return a, true
}

// codexLogin describes the Codex login: the e-mail from the id token and
// the plan from the access token. Both are read from the JWT payloads.
func codexLogin() (AccountInfo, bool) {
	a, ok := readCodexAuth()
	if !ok {
		return AccountInfo{}, false
	}
	info := AccountInfo{Source: "Codex"}
	if c := jwtClaims(a.Tokens.IDToken); c != nil {
		info.User, _ = c["email"].(string)
	}
	if c := jwtClaims(a.Tokens.AccessToken); c != nil {
		if auth, ok := c["https://api.openai.com/auth"].(map[string]any); ok {
			info.Plan, _ = auth["chatgpt_plan_type"].(string)
		}
	}
	return info, true
}

// jwtClaims reads the payload of a JWT. The signature is not checked: dial
// only needs what the login says about itself.
func jwtClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var c map[string]any
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	return c
}

// jwtExpiry is the exp claim of a JWT, when it has one.
func jwtExpiry(tok string) (time.Time, bool) {
	exp, ok := jwtClaims(tok)["exp"].(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(exp), 0), true
}

type codexToken struct {
	access    string
	accountID string
}

// codexAccess returns a usable access token. A token that expires within
// five minutes is refreshed first, and the new tokens are written back to
// Codex's file with every other field left as it was.
func codexAccess(ctx context.Context) (codexToken, error) {
	a, ok := readCodexAuth()
	if !ok {
		return codexToken{}, ErrCodexLoggedOut
	}
	t := a.Tokens
	out := codexToken{access: t.AccessToken, accountID: t.AccountID}
	exp, ok := jwtExpiry(t.AccessToken)
	if !ok || time.Until(exp) >= codexRefreshWithin {
		return out, nil
	}
	fresh, err := refreshCodex(ctx, t.RefreshToken)
	if err != nil {
		return codexToken{}, err
	}
	if err := writeCodexTokens(fresh); err != nil {
		return codexToken{}, fmt.Errorf("could not save the refreshed Codex login: %w", err)
	}
	out.access = fresh["access_token"]
	return out, nil
}

// refreshCodex trades a refresh token for new tokens. It returns the token
// fields the reply carried, by their names in Codex's file.
func refreshCodex(ctx context.Context, refresh string) (map[string]string, error) {
	if refresh == "" {
		return nil, ErrCodexLoggedOut
	}
	body, _ := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
		"scope":         "openid profile email",
	})
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexAuthBase+"/oauth/token", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refreshing the Codex login: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return nil, ErrCodexLoggedOut
	}
	var v struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(b, &v) != nil || v.AccessToken == "" {
		return nil, ErrCodexLoggedOut
	}
	out := map[string]string{"access_token": v.AccessToken}
	if v.IDToken != "" {
		out["id_token"] = v.IDToken
	}
	if v.RefreshToken != "" {
		out["refresh_token"] = v.RefreshToken
	}
	return out, nil
}

// writeCodexTokens updates the token fields of Codex's login file and keeps
// every other field, including ones dial does not know about.
func writeCodexTokens(updates map[string]string) error {
	path := codexPath()
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
	for k, v := range updates {
		raw, _ := json.Marshal(v)
		tokens[k] = raw
	}
	tb, _ := json.Marshal(tokens)
	top["tokens"] = tb
	last, _ := json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
	top["last_refresh"] = last
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// CodexBody turns a Responses request for codex/<model> into the body the
// ChatGPT backend accepts: the model without its prefix, streaming on, no
// storage, no sampling or output-length fields, and a string input turned
// into a list of items.
func CodexBody(body []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	var model string
	if raw, ok := m["model"]; ok {
		json.Unmarshal(raw, &model)
	}
	set := func(k string, v any) {
		b, _ := json.Marshal(v)
		m[k] = b
	}
	set("model", strings.TrimPrefix(model, "codex/"))
	set("stream", true)
	set("store", false)
	delete(m, "max_output_tokens")
	delete(m, "temperature")
	delete(m, "top_p")
	if raw, ok := m["input"]; ok {
		var s string
		if json.Unmarshal(bytes.TrimSpace(raw), &s) == nil {
			set("input", []map[string]any{{
				"type": "message", "role": "user",
				"content": []map[string]any{{"type": "input_text", "text": s}},
			}})
		}
	}
	return json.Marshal(m)
}

// CodexCall sends a Responses request to the Codex backend. The backend only
// streams, so the reply is an event stream whatever the client asked for.
// The caller forwards it, or gathers it with CodexComplete.
func CodexCall(ctx context.Context, p Provider, body []byte) (*http.Response, error) {
	b, err := CodexBody(body)
	if err != nil {
		return nil, err
	}
	base := p.Base(Responses)
	if base == "" {
		base = CodexBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/responses", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range AuthHeaders(p, Responses) {
		req.Header.Set(k, v)
	}
	return http.DefaultClient.Do(req)
}

// CodexComplete answers a non-streaming Responses request: it streams upstream
// and returns the final response object as JSON.
func CodexComplete(ctx context.Context, p Provider, body []byte) ([]byte, error) {
	res, err := CodexCall(ctx, p, body)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return nil, errors.New(APIError(b, res.Status))
	}
	return Aggregate(res.Body, res.Header.Get("Content-Type"))
}

// Aggregate reads a Responses reply and returns its final response object.
// A reply that is not an event stream is returned as it is.
func Aggregate(r io.Reader, contentType string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, 256<<20))
	if err != nil {
		return nil, err
	}
	if !IsEventStream(contentType, b) {
		return b, nil
	}
	for _, data := range sseData(b) {
		var ev struct {
			Type     string          `json:"type"`
			Message  string          `json:"message"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal(data, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.completed", "response.done", "response.incomplete":
			if len(ev.Response) > 0 {
				return ev.Response, nil
			}
		case "response.failed":
			var resp struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			json.Unmarshal(ev.Response, &resp)
			if resp.Error.Message != "" {
				return nil, errors.New(resp.Error.Message)
			}
			return nil, errors.New("the Codex backend reported a failed response")
		case "error":
			if ev.Message != "" {
				return nil, errors.New(ev.Message)
			}
			return nil, errors.New("the Codex backend reported an error")
		}
	}
	return nil, errors.New("the Codex stream ended without a final response")
}

// sseData returns the data payloads of an event stream, in order.
func sseData(b []byte) [][]byte {
	var out [][]byte
	for _, line := range bytes.Split(b, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			out = append(out, bytes.TrimSpace(rest))
		}
	}
	return out
}

// IsEventStream reports whether an upstream reply is an event stream. The
// Codex backend sometimes sends no Content-Type at all; then the first bytes
// of the body decide.
func IsEventStream(contentType string, body []byte) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "text/event-stream") {
		return true
	}
	if ct != "" {
		return false
	}
	s := bytes.TrimLeft(body, " \t\r\n\ufeff")
	return bytes.HasPrefix(s, []byte("event:")) || bytes.HasPrefix(s, []byte("data:"))
}
