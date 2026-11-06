package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	codexClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexRenewWithin = 5 * time.Minute
	codexLoggedOut   = "Codex 已登出，请运行 `codex login` 重新登录"
)

// codexRefreshURL is where Codex renews its tokens; tests point it at a fake.
var codexRefreshURL = "https://auth.openai.com/oauth/token"

// codexLogin is one read of ~/.codex/auth.json. Fields the dial does not own
// are kept in top and tokens as they were, so a renewal writes them back intact.
type codexLogin struct {
	path       string
	top        map[string]json.RawMessage
	tokens     map[string]json.RawMessage
	access     string
	refreshTok string
	idTok      string
	accountID  string
	email      string
	plan       string
	expires    time.Time // zero when the access token carries no exp
}

func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// readCodex reads the ChatGPT login. An API-key login is not an account.
func readCodex() (*codexLogin, bool) {
	p := codexAuthPath()
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(b, &top) != nil || top == nil {
		return nil, false
	}
	var mode string
	json.Unmarshal(top["auth_mode"], &mode)
	if mode != "chatgpt" {
		return nil, false
	}
	var tokens map[string]json.RawMessage
	if json.Unmarshal(top["tokens"], &tokens) != nil || tokens == nil {
		return nil, false
	}
	str := func(k string) string {
		var s string
		json.Unmarshal(tokens[k], &s)
		return s
	}
	l := &codexLogin{path: p, top: top, tokens: tokens,
		access: str("access_token"), refreshTok: str("refresh_token"),
		idTok: str("id_token"), accountID: str("account_id")}
	if l.access == "" {
		return nil, false
	}
	l.parse()
	return l, true
}

// parse reads the email, plan and expiry out of the tokens' JWT payloads.
func (l *codexLogin) parse() {
	id := jwtClaims(l.idTok)
	l.email, _ = id["email"].(string)
	if l.email == "" {
		if prof, ok := id["https://api.openai.com/profile"].(map[string]any); ok {
			l.email, _ = prof["email"].(string)
		}
	}
	acc := jwtClaims(l.access)
	if exp, ok := acc["exp"].(float64); ok {
		l.expires = time.Unix(int64(exp), 0)
	}
	for _, c := range []map[string]any{id, acc} {
		if auth, ok := c["https://api.openai.com/auth"].(map[string]any); ok && l.plan == "" {
			l.plan, _ = auth["chatgpt_plan_type"].(string)
		}
	}
}

func (l *codexLogin) who() string {
	if l.email != "" {
		return l.email
	}
	return "ChatGPT account"
}

// jwtClaims returns the payload of a JWT without checking its signature;
// dial only reads what Codex already wrote for itself.
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

// renew trades the refresh token for a new set, as Codex does, and writes the
// set back into the Codex file. A refusal means the login is gone.
func (l *codexLogin) renew(ctx context.Context) error {
	body, err := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token", "refresh_token": l.refreshTok})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexRefreshURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s（%v）", codexLoggedOut, err)
	}
	defer res.Body.Close()
	var v struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&v) != nil || v.AccessToken == "" {
		return fmt.Errorf("%s（刷新令牌返回 %s）", codexLoggedOut, res.Status)
	}
	if v.RefreshToken == "" {
		v.RefreshToken = l.refreshTok
	}
	if v.IDToken == "" {
		v.IDToken = l.idTok
	}
	l.access, l.refreshTok, l.idTok = v.AccessToken, v.RefreshToken, v.IDToken
	for k, s := range map[string]string{"access_token": l.access, "refresh_token": l.refreshTok, "id_token": l.idTok} {
		b, _ := json.Marshal(s)
		l.tokens[k] = b
	}
	tb, err := json.Marshal(l.tokens)
	if err != nil {
		return err
	}
	lr, err := json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	l.top["tokens"] = tb
	l.top["last_refresh"] = lr
	out, err := json.MarshalIndent(l.top, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(l.path, append(out, '\n'), 0o600); err != nil {
		return fmt.Errorf("cannot write the renewed Codex login: %w", err)
	}
	l.parse()
	return nil
}

// routeCodex sends a Responses request to the ChatGPT Codex backend.
func routeCodex(ctx context.Context, path string, body []byte) (Upstream, error) {
	l, ok := readCodex()
	if !ok {
		return Upstream{}, errors.New(codexLoggedOut)
	}
	if !l.expires.IsZero() && time.Until(l.expires) < codexRenewWithin {
		if err := l.renew(ctx); err != nil {
			return Upstream{}, err
		}
	}
	b, err := codexBody(body)
	if err != nil {
		return Upstream{}, err
	}
	h := map[string]string{"Authorization": "Bearer " + l.access}
	if l.accountID != "" {
		h["chatgpt-account-id"] = l.accountID
	}
	return Upstream{URL: CodexBase + path, Header: h, Body: b}, nil
}

// codexBody shapes a Responses request for the ChatGPT backend: it streams
// only, keeps nothing, takes no sampling or output-length fields, and wants
// its input as a list of items.
func codexBody(body []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("request body is not JSON: %v", err)
	}
	if s, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(s, codexID+"/")
	}
	m["stream"] = true
	m["store"] = false
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		delete(m, k)
	}
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": s}}}}
	}
	return json.Marshal(m)
}

// CollectResponse turns a streamed Codex reply into the single Response object
// a non-streaming client expects. A plain JSON reply is returned as it is.
func CollectResponse(raw []byte) ([]byte, error) {
	t := bytes.TrimSpace(raw)
	if len(t) > 0 && t[0] == '{' {
		return t, nil
	}
	var final json.RawMessage
	var failure string
	for _, line := range strings.Split(string(raw), "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
		if !ok {
			continue
		}
		var ev struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
			Message  string          `json:"message"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.completed", "response.incomplete":
			final = ev.Response
		case "response.failed":
			failure = streamFailure(ev.Response)
		case "error":
			failure = ev.Message
		}
	}
	if final != nil {
		return final, nil
	}
	if failure == "" {
		failure = "the stream ended without a response"
	}
	return nil, errors.New(failure)
}

// streamFailure is the message of a failed response object.
func streamFailure(resp json.RawMessage) string {
	var v struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(resp, &v)
	if v.Error.Message == "" {
		return "the response failed"
	}
	return v.Error.Message
}
