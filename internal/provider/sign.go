package provider

// Signing and request preparation for account providers. Everything here is
// read from the agent's own login at the moment it is needed; the only state
// kept between calls is the in-memory Copilot session token and the refresh
// lock. Tokens never reach dial's config.

import (
	"bytes"
	"context"
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

// Endpoints. They are variables so tests can point them at stand-in servers.
var (
	CodexBase       = "https://chatgpt.com/backend-api/codex"
	CodexRefreshURL = "https://auth.openai.com/oauth/token"
	CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
)

const (
	codexClientID      = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexRelogin       = "codex: 登录已失效，请重新登录 (codex login)"
	copilotRelogin     = "copilot: 登录已失效，请重新登录 (在编辑器或 Copilot CLI 中重新登录 GitHub)"
	copilotIntegration = "vscode-chat"
	copilotEditor      = "vscode/1.99.3"
	copilotPlugin      = "copilot-chat/0.26.7"
	copilotUA          = "GitHubCopilotChat/0.26.7"

	// refreshWithin is how close to expiry a token is renewed before use.
	refreshWithin = 5 * time.Minute
)

// signClient carries the token exchange and refresh calls.
var signClient = &http.Client{Timeout: 30 * time.Second}

// Sign attaches what the provider needs to authenticate req. An ordinary
// provider gets its API key as headers. An account provider gets the agent's
// token, and its URL is rewritten to the backend the account is used through.
// body is the request body the caller will send; it is only read.
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if p.Account == nil {
		for k, v := range AuthHeaders(p, proto) {
			req.Header.Set(k, v)
		}
		return nil
	}
	switch p.Account.Agent {
	case "codex":
		return signCodex(ctx, req)
	case "copilot":
		return signCopilot(ctx, req, body)
	}
	return fmt.Errorf("%s: 不支持该账号的签名", p.Name)
}

// Prepare adjusts a request body for the provider's backend. An ordinary
// provider's body is returned as it is. The model prefix ("codex/") is removed
// here for Codex; the gateway already passes the bare model name for Copilot,
// so Copilot bodies are returned unchanged.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account == nil {
		return body
	}
	if p.Account.Agent == "codex" {
		return prepareCodex(body)
	}
	return body
}

// ---- Codex -----------------------------------------------------------------

// codexMu serialises Codex token refreshes and reads, so concurrent requests
// refresh at most once and never write auth.json at the same time.
var codexMu sync.Mutex

func signCodex(ctx context.Context, req *http.Request) error {
	access, accountID, err := codexAccessToken(ctx)
	if err != nil {
		return err
	}
	u, err := url.Parse(CodexBase + "/responses")
	if err != nil {
		return err
	}
	req.URL = u
	req.Host = ""
	req.Header.Set("Authorization", "Bearer "+access)
	if accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}
	return nil
}

// codexAccessToken returns a usable access token and the account id. When the
// token is within refreshWithin of expiry it is refreshed first, under codexMu.
// The file is read again inside the lock, so a refresh another caller (or the
// Codex CLI) already made is picked up rather than repeated.
func codexAccessToken(ctx context.Context) (string, string, error) {
	codexMu.Lock()
	defer codexMu.Unlock()

	l, ok := codexLogin()
	if !ok {
		return "", "", errors.New(codexRelogin)
	}
	if !expiresWithin(l.token, refreshWithin) {
		return l.token, l.accountID, nil
	}

	raw, err := readCodexFile()
	if err != nil {
		return "", "", errors.New(codexRelogin)
	}
	tokens, _ := raw["tokens"].(map[string]any)
	refreshToken, _ := tokens["refresh_token"].(string)
	if refreshToken == "" {
		return "", "", errors.New(codexRelogin)
	}

	r, err := refreshCodex(ctx, refreshToken)
	if err != nil {
		return "", "", fmt.Errorf("%s（刷新失败：%v）", codexRelogin, err)
	}
	tokens["access_token"] = r.AccessToken
	if r.IDToken != "" {
		tokens["id_token"] = r.IDToken
	}
	if r.RefreshToken != "" {
		tokens["refresh_token"] = r.RefreshToken
	}
	if err := writeCodexFile(raw, tokens); err != nil {
		return "", "", fmt.Errorf("codex: 刷新后无法写回 %s：%w", codexAuthPath(), err)
	}
	return r.AccessToken, l.accountID, nil
}

// expiresWithin reports whether a JWT's exp claim falls within d from now. A
// token without a readable exp is never treated as expiring.
func expiresWithin(tok string, d time.Duration) bool {
	exp := parseJWTClaims(tok).Exp
	return exp > 0 && time.Until(time.Unix(exp, 0)) < d
}

// readCodexFile parses auth.json as generic JSON. Numbers are kept as
// json.Number so rewriting the file does not change them.
func readCodexFile() (map[string]any, error) {
	b, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("auth.json is not an object")
	}
	return raw, nil
}

// writeCodexFile replaces auth.json with raw, updating tokens and last_refresh
// and leaving every other field as it was. It writes a temporary file in the
// same directory and renames it into place, so a crash never leaves a partial
// file. The result is mode 0600.
func writeCodexFile(raw, tokens map[string]any) error {
	raw["tokens"] = tokens
	raw["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	path := codexAuthPath()
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auth-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // harmless once the rename has moved it
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

type codexRefreshed struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
}

// refreshCodex exchanges a refresh token for new tokens at CodexRefreshURL.
// Error text carries the status and the server's message, never the tokens.
func refreshCodex(ctx context.Context, refreshToken string) (codexRefreshed, error) {
	body, err := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	})
	if err != nil {
		return codexRefreshed{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexRefreshURL, bytes.NewReader(body))
	if err != nil {
		return codexRefreshed{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := signClient.Do(req)
	if err != nil {
		return codexRefreshed{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return codexRefreshed{}, fmt.Errorf("HTTP %d: %s", res.StatusCode, APIError(b, res.Status))
	}
	var out codexRefreshed
	if json.Unmarshal(b, &out) != nil || out.AccessToken == "" {
		return codexRefreshed{}, errors.New("响应无效")
	}
	return out, nil
}

// prepareCodex turns a Chat-style or Responses body into the form the ChatGPT
// backend accepts: streamed, not stored, input as a list of items, no sampling
// or output-limit fields, and the model without its "codex/" prefix. Reasoning
// settings are kept. A body that is not a JSON object is returned unchanged.
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
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": s,
			}},
		}}
	}
	for _, k := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens", "temperature", "top_p"} {
		delete(m, k)
	}
	if model, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(model, "codex/")
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// ---- Copilot ---------------------------------------------------------------

// copilotSession is the cached Copilot session token. It is valid only for the
// GitHub token it was exchanged from, so a changed or removed login is noticed.
type copilotSession struct {
	sync.Mutex
	oauth string
	token string
	api   string
	exp   time.Time
}

var copilotCache copilotSession

type copilotToken struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
}

func signCopilot(ctx context.Context, req *http.Request, body []byte) error {
	l, ok := copilotLogin()
	if !ok {
		return errors.New(copilotRelogin)
	}
	session, api, err := copilotSessionFor(ctx, l.token)
	if err != nil {
		return err
	}
	u, err := url.Parse(strings.TrimRight(api, "/") + "/chat/completions")
	if err != nil {
		return err
	}
	req.URL = u
	req.Host = ""
	h := req.Header
	h.Set("Authorization", "Bearer "+session)
	h.Set("Content-Type", "application/json")
	h.Set("Copilot-Integration-Id", copilotIntegration)
	h.Set("Editor-Version", copilotEditor)
	h.Set("Editor-Plugin-Version", copilotPlugin)
	h.Set("User-Agent", copilotUA)
	h.Set("OpenAI-Intent", "conversation-panel")
	h.Set("X-Initiator", copilotInitiator(body))
	if copilotHasImage(body) {
		h.Set("Copilot-Vision-Request", "true")
	} else {
		h.Del("Copilot-Vision-Request")
	}
	return nil
}

// copilotSessionFor returns the cached session token and API base for the
// given GitHub token, exchanging again when none is cached or it is near expiry.
// Holding the lock across the exchange means concurrent callers make one call.
func copilotSessionFor(ctx context.Context, oauth string) (string, string, error) {
	copilotCache.Lock()
	defer copilotCache.Unlock()
	c := &copilotCache
	if c.oauth == oauth && c.token != "" && time.Until(c.exp) > refreshWithin {
		return c.token, c.api, nil
	}
	t, err := exchangeCopilot(ctx, oauth)
	if err != nil {
		return "", "", err
	}
	c.oauth = oauth
	c.token = t.Token
	c.api = t.Endpoints.API
	c.exp = time.Unix(t.ExpiresAt, 0)
	return c.token, c.api, nil
}

// exchangeCopilot trades a GitHub OAuth token for a short-lived Copilot session.
func exchangeCopilot(ctx context.Context, oauth string) (copilotToken, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotToken{}, err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", copilotUA)
	res, err := signClient.Do(req)
	if err != nil {
		return copilotToken{}, fmt.Errorf("copilot: 令牌交换失败：%w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return copilotToken{}, errors.New(copilotRelogin)
	}
	if res.StatusCode != http.StatusOK {
		return copilotToken{}, fmt.Errorf("copilot: 令牌交换失败（%s）：%s", res.Status, APIError(b, res.Status))
	}
	var t copilotToken
	if json.Unmarshal(b, &t) != nil || t.Token == "" || t.Endpoints.API == "" {
		return copilotToken{}, errors.New("copilot: 令牌响应无效")
	}
	return t, nil
}

// chatBody is the part of a Chat Completions body the signer inspects.
type chatBody struct {
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

func parseChatBody(body []byte) chatBody {
	var c chatBody
	json.Unmarshal(body, &c)
	return c
}

// copilotInitiator is "user" when the last message is the user's, otherwise
// "agent" (a tool reply or the assistant continuing its own work).
func copilotInitiator(body []byte) string {
	c := parseChatBody(body)
	if n := len(c.Messages); n > 0 && c.Messages[n-1].Role == "user" {
		return "user"
	}
	return "agent"
}

// copilotHasImage reports whether any message carries an image_url part.
func copilotHasImage(body []byte) bool {
	for _, m := range parseChatBody(body).Messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) != nil {
			continue
		}
		for _, p := range parts {
			if p.Type == "image_url" {
				return true
			}
		}
	}
	return false
}
