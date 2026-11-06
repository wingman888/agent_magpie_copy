package provider

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

// CodexBase is the ChatGPT backend that serves Codex sign-ins. A variable so
// tests can point it at a stand-in.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// CodexRefreshURL is where a Codex refresh token is traded for fresh tokens.
var CodexRefreshURL = "https://auth.openai.com/oauth/token"

const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// tokenSlack is how close to expiry a token is renewed before a request.
const tokenSlack = 5 * time.Minute

const (
	codexReloginHint   = "请重新登录（运行 codex login）"
	copilotReloginHint = "请重新登录（在编辑器中重新登录 GitHub Copilot）"
)

var refreshHTTP = &http.Client{Timeout: 30 * time.Second}

// codexMu serialises Codex token reads and refreshes so concurrent requests
// do not refresh the same token twice.
var codexMu sync.Mutex

// codexDroppedParams are sampling and limit parameters the ChatGPT backend
// refuses. Reasoning parameters are kept.
var codexDroppedParams = []string{
	"max_output_tokens", "max_completion_tokens", "max_tokens", "temperature", "top_p", "truncation",
}

// Sign attaches the credentials for this provider to an outgoing request.
// Ordinary providers send their key; account providers send the agent's own
// token, read from the agent's credential file at the time of the call.
// When body is non-nil it becomes the request body.
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if req.Header == nil {
		req.Header = http.Header{}
	}
	if p.Account == nil {
		for k, v := range AuthHeaders(p, proto) {
			req.Header.Set(k, v)
		}
		return nil
	}
	switch p.Account.Agent {
	case "codex":
		return signCodex(ctx, req, body)
	case "copilot":
		return signCopilot(ctx, req, body)
	}
	return fmt.Errorf("%s: unsupported sign-in %q", p.Name, p.Account.Agent)
}

// Prepare adapts a request body to what the upstream requires. Ordinary
// providers get the body back unchanged.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account == nil || p.Account.Agent != "codex" {
		return body
	}
	m, ok := decodeObject(body)
	if !ok {
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
	for _, k := range codexDroppedParams {
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

// decodeObject parses a JSON object, keeping numbers exactly as written.
func decodeObject(b []byte) (map[string]any, bool) {
	m := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}

func setBody(req *http.Request, body []byte) {
	if body == nil {
		return
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	req.ContentLength = int64(len(body))
}

func setURL(req *http.Request, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	req.URL = u
	req.Host = u.Host
	return nil
}

// ---- Codex ----------------------------------------------------------------

func signCodex(ctx context.Context, req *http.Request, body []byte) error {
	access, accountID, err := codexToken(ctx)
	if err != nil {
		return err
	}
	if err := setURL(req, strings.TrimRight(CodexBase, "/")+"/responses"); err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("chatgpt-account-id", accountID)
	req.Header.Set("Content-Type", "application/json")
	setBody(req, body)
	return nil
}

// codexToken returns a usable access token and the account id, refreshing
// the token first when it is within tokenSlack of expiring.
func codexToken(ctx context.Context) (string, string, error) {
	codexMu.Lock()
	defer codexMu.Unlock()

	// Read the file under the lock so a refresh made by another request is seen.
	raw, f, err := readCodexFile()
	if err != nil {
		return "", "", err
	}
	if !codexExpiring(f.Tokens.AccessToken) {
		return f.Tokens.AccessToken, f.Tokens.AccountID, nil
	}
	if strings.TrimSpace(f.Tokens.RefreshToken) == "" {
		return "", "", codexLoggedOut("凭据文件中没有刷新令牌")
	}
	fresh, err := requestCodexRefresh(ctx, f.Tokens.RefreshToken)
	if err != nil {
		return "", "", err
	}
	if err := writeCodexTokens(raw, fresh); err != nil {
		return "", "", fmt.Errorf("保存刷新后的 Codex 令牌失败：%w", err)
	}
	return fresh.AccessToken, f.Tokens.AccountID, nil
}

func codexAuthPath() string {
	return filepath.Join(homeDir(), ".codex", "auth.json")
}

// readCodexFile returns the auth file both as a generic map (so every field
// can be written back) and as the typed fields dial reads.
func readCodexFile() (map[string]any, codexAuthFile, error) {
	b, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return nil, codexAuthFile{}, codexLoggedOut("找不到 ~/.codex/auth.json")
	}
	var f codexAuthFile
	if json.Unmarshal(b, &f) != nil || !codexModeOK(f) || strings.TrimSpace(f.Tokens.AccessToken) == "" {
		return nil, codexAuthFile{}, codexLoggedOut("不是有效的 ChatGPT 登录")
	}
	raw, ok := decodeObject(b)
	if !ok {
		return nil, codexAuthFile{}, codexLoggedOut("凭据文件无法解析")
	}
	return raw, f, nil
}

// codexExpiring reports whether the access token's exp claim is within
// tokenSlack. A token without a readable exp is treated as still valid.
func codexExpiring(access string) bool {
	exp, ok := jwtPayload(access)["exp"].(float64)
	if !ok {
		return false
	}
	return time.Until(time.Unix(int64(exp), 0)) <= tokenSlack
}

func codexLoggedOut(detail string) error {
	return fmt.Errorf("Codex 登录已失效，已登出（%s）；%s", detail, codexReloginHint)
}

type codexRefreshed struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
}

// requestCodexRefresh trades a refresh token for new tokens.
func requestCodexRefresh(ctx context.Context, refresh string) (codexRefreshed, error) {
	payload, err := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
	})
	if err != nil {
		return codexRefreshed{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexRefreshURL, bytes.NewReader(payload))
	if err != nil {
		return codexRefreshed{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := refreshHTTP.Do(req)
	if err != nil {
		return codexRefreshed{}, codexLoggedOut("刷新请求失败: " + err.Error())
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return codexRefreshed{}, codexLoggedOut(fmt.Sprintf("刷新返回 HTTP %d", resp.StatusCode))
	}
	var out codexRefreshed
	if json.Unmarshal(body, &out) != nil || out.AccessToken == "" {
		return codexRefreshed{}, codexLoggedOut("刷新响应中没有访问令牌")
	}
	return out, nil
}

// writeCodexTokens stores refreshed tokens in the auth file, leaving every
// other field as it was, and keeps the file private.
func writeCodexTokens(raw map[string]any, fresh codexRefreshed) error {
	tokens, _ := raw["tokens"].(map[string]any)
	if tokens == nil {
		tokens = map[string]any{}
	}
	tokens["access_token"] = fresh.AccessToken
	if fresh.IDToken != "" {
		tokens["id_token"] = fresh.IDToken
	}
	if fresh.RefreshToken != "" {
		tokens["refresh_token"] = fresh.RefreshToken
	}
	raw["tokens"] = tokens
	raw["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)

	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(codexAuthPath(), append(b, '\n'))
}

// writePrivate replaces path atomically with data, mode 0600.
func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auth-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// ---- Copilot --------------------------------------------------------------

// copilotSessions caches the session token for the last OAuth token used.
var copilotSessions struct {
	mu    sync.Mutex
	oauth string
	sess  copilotSession
}

// copilotSessionFor returns a cached session token, exchanging again when it
// is within tokenSlack of expiring.
func copilotSessionFor(ctx context.Context, oauth string) (copilotSession, error) {
	copilotSessions.mu.Lock()
	defer copilotSessions.mu.Unlock()
	if copilotSessions.oauth == oauth && copilotSessions.sess.Token != "" &&
		time.Until(time.Unix(copilotSessions.sess.ExpiresAt, 0)) > tokenSlack {
		return copilotSessions.sess, nil
	}
	s, err := copilotExchange(ctx, oauth)
	if err != nil {
		return copilotSession{}, fmt.Errorf("GitHub Copilot 登录已失效，已登出（%w）；%s", err, copilotReloginHint)
	}
	copilotSessions.oauth = oauth
	copilotSessions.sess = s
	return s, nil
}

func signCopilot(ctx context.Context, req *http.Request, body []byte) error {
	cred, ok := readCopilot()
	if !ok {
		return errors.New("GitHub Copilot 已登出（找不到 github.com 登录）；" + copilotReloginHint)
	}
	sess, err := copilotSessionFor(ctx, cred.OAuth)
	if err != nil {
		return err
	}
	if err := setURL(req, strings.TrimRight(sess.Endpoints.API, "/")+"/chat/completions"); err != nil {
		return err
	}
	h := req.Header
	h.Set("Authorization", "Bearer "+sess.Token)
	h.Set("Content-Type", "application/json")
	h.Set("Copilot-Integration-Id", copilotIntegrationID)
	h.Set("Editor-Version", "vscode/1.99.3")
	h.Set("Editor-Plugin-Version", "copilot-chat/0.26.7")
	h.Set("User-Agent", "GitHubCopilotChat/0.26.7")
	h.Set("Openai-Intent", "conversation-panel")
	h.Set("X-Initiator", copilotInitiator(body))
	if copilotHasImage(body) {
		h.Set("Copilot-Vision-Request", "true")
	} else {
		h.Del("Copilot-Vision-Request")
	}
	setBody(req, body)
	return nil
}

type chatMessages struct {
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

// copilotInitiator is "user" when the last message is from the user, else
// "agent" (tool results, assistant turns and so on).
func copilotInitiator(body []byte) string {
	var cm chatMessages
	if json.Unmarshal(body, &cm) != nil || len(cm.Messages) == 0 {
		return "user"
	}
	if cm.Messages[len(cm.Messages)-1].Role == "user" {
		return "user"
	}
	return "agent"
}

// copilotHasImage reports whether any message carries an image part.
func copilotHasImage(body []byte) bool {
	var cm chatMessages
	if json.Unmarshal(body, &cm) != nil {
		return false
	}
	for _, m := range cm.Messages {
		var parts []map[string]any
		if json.Unmarshal(m.Content, &parts) != nil {
			continue
		}
		for _, part := range parts {
			if _, ok := part["image_url"]; ok {
				return true
			}
			if t, _ := part["type"].(string); t == "image" || t == "image_url" {
				return true
			}
		}
	}
	return false
}
