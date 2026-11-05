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
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// CodexBase is the ChatGPT backend Codex talks to. Requests for codex/<model>
// go to CodexBase + "/responses". Tests point it at a local server.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// CodexAuthBase is the OAuth issuer that refreshes Codex's ChatGPT tokens.
var CodexAuthBase = "https://auth.openai.com"

const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// codexBuiltin is the model list used until the user refreshes it.
var codexBuiltin = []string{"gpt-5.5", "gpt-5.4", "gpt-5.3-codex"}

var errCodexOut = errors.New("已登出，请运行 `codex login`")

// codexMu serialises refreshes: two requests must not spend the same refresh token.
var codexMu sync.Mutex

// codexLogin is the ChatGPT login Codex keeps in ~/.codex/auth.json. raw is
// the whole file, so a refresh can write it back with every other field intact.
type codexLogin struct {
	raw       map[string]any
	tokens    map[string]any
	access    string
	refresh   string
	accountID string
	email     string
	plan      string
	expires   time.Time
}

func codexAuthPath() string { return homeJoin(".codex", "auth.json") }

// readCodex reads the login file. Only a ChatGPT login counts; an API-key
// login is not an account.
func readCodex() (*codexLogin, error) {
	b, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return nil, errNotSignedIn
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var raw map[string]any
	if dec.Decode(&raw) != nil || raw == nil {
		return nil, errNotSignedIn
	}
	if mode, _ := raw["auth_mode"].(string); mode != "chatgpt" {
		return nil, errNotSignedIn
	}
	toks, _ := raw["tokens"].(map[string]any)
	str := func(k string) string { v, _ := toks[k].(string); return v }
	c := &codexLogin{raw: raw, tokens: toks, access: str("access_token"), refresh: str("refresh_token"), accountID: str("account_id")}
	if c.access == "" && c.refresh == "" {
		return nil, errNotSignedIn
	}
	id := jwtClaims(str("id_token"))
	c.email, _ = id["email"].(string)
	if auth, ok := id["https://api.openai.com/auth"].(map[string]any); ok {
		c.plan, _ = auth["chatgpt_plan_type"].(string)
	}
	if n, ok := jwtClaims(c.access)["exp"].(json.Number); ok {
		if sec, err := n.Int64(); err == nil {
			c.expires = time.Unix(sec, 0)
		}
	}
	return c, nil
}

func codexProvider() *Provider {
	c, err := readCodex()
	if err != nil {
		return nil
	}
	user := c.email
	if user == "" {
		user = "ChatGPT account"
	}
	return &Provider{
		ID: CodexID, Name: "Codex", Icon: "openai", Responses: CodexBase,
		Website: "https://chatgpt.com", KeysURL: "",
		Account: &Account{User: user, Plan: c.plan, Source: "Codex"},
	}
}

// codexToken returns a usable access token and the account id, refreshing
// the login first when the access token expires within five minutes.
func codexToken(ctx context.Context) (token, accountID string, err error) {
	codexMu.Lock()
	defer codexMu.Unlock()
	c, err := readCodex()
	if err != nil {
		return "", "", errCodexOut
	}
	// refresh only when the expiry is known and close; an unknown expiry
	// (no exp claim) means use the access token as it is
	if c.access == "" || (!c.expires.IsZero() && time.Until(c.expires) < 5*time.Minute) {
		if err := c.refreshCodex(ctx); err != nil {
			return "", "", errCodexOut
		}
	}
	return c.access, c.accountID, nil
}

// refreshCodex trades the refresh token for new tokens and writes them back
// into Codex's own file, leaving every other field as it was.
func (c *codexLogin) refreshCodex(ctx context.Context) error {
	if c.refresh == "" || c.tokens == nil {
		return errCodexOut
	}
	body, _ := json.Marshal(map[string]string{
		"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": c.refresh, "scope": "openid profile email",
	})
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexAuthBase+"/oauth/token", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("token refresh: %s", res.Status)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out) != nil || out.AccessToken == "" {
		return errors.New("token refresh: no access token in reply")
	}
	c.tokens["access_token"] = out.AccessToken
	if out.RefreshToken != "" {
		c.tokens["refresh_token"] = out.RefreshToken
	}
	if out.IDToken != "" {
		c.tokens["id_token"] = out.IDToken
	}
	c.raw["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.MarshalIndent(c.raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(codexAuthPath(), append(b, '\n'), 0o600); err != nil {
		return err
	}
	c.access = out.AccessToken
	if out.RefreshToken != "" {
		c.refresh = out.RefreshToken
	}
	return nil
}

// codexBody makes a Responses request acceptable to the ChatGPT backend: it
// only takes streams, does not store, rejects sampling and output limits,
// and wants input as a list of items.
func codexBody(body []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, errors.New("invalid request body")
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		delete(m, k)
	}
	m["stream"] = true
	m["store"] = false
	if s, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(s, CodexID+"/")
	}
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": s}},
		}}
	}
	return json.Marshal(m)
}

// codexBuiltinModels is the list used until the user refreshes it.
func codexBuiltinModels() []catalog.Model {
	out := make([]catalog.Model, 0, len(codexBuiltin))
	for _, id := range codexBuiltin {
		out = append(out, catalog.Model{ID: id, Name: id, Provider: "openai"})
	}
	return out
}
