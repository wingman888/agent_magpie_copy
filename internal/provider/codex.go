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
	"sync"
	"time"
)

// CodexBase is the ChatGPT backend a signed-in Codex account talks to. Its
// Responses API is at CodexBase + "/responses". Tests point it at a fake.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// CodexRefreshURL is where a Codex refresh token is exchanged for new tokens.
var CodexRefreshURL = "https://auth.openai.com/oauth/token"

const (
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann" // the client Codex itself refreshes with
	codexRelogin  = "已登出，请运行 `codex login`"
	codexSkew     = 5 * time.Minute // refresh when the access token has less than this left
)

// codexMu serialises refreshes: refresh tokens rotate, so two at once would
// invalidate each other.
var codexMu sync.Mutex

// codexAuth is Codex's own sign-in file, ~/.codex/auth.json, kept whole so a
// refresh can write it back with every other field untouched.
type codexAuth struct {
	path string
	doc  map[string]any
}

func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// readCodex reads the sign-in. Only a ChatGPT login counts; an API-key login
// is not an account.
func readCodex() (*codexAuth, bool) {
	a := &codexAuth{path: codexAuthPath()}
	b, err := os.ReadFile(a.path)
	if err != nil {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if dec.Decode(&a.doc) != nil {
		return nil, false
	}
	if mode, _ := a.doc["auth_mode"].(string); mode != "chatgpt" {
		return nil, false
	}
	if a.tok("access_token") == "" && a.tok("refresh_token") == "" {
		return nil, false
	}
	return a, true
}

func (a *codexAuth) tok(k string) string {
	t, _ := a.doc["tokens"].(map[string]any)
	s, _ := t[k].(string)
	return s
}

func (a *codexAuth) setTok(k, v string) {
	t, ok := a.doc["tokens"].(map[string]any)
	if !ok {
		t = map[string]any{}
		a.doc["tokens"] = t
	}
	t[k] = v
}

func (a *codexAuth) write() error {
	b, err := json.MarshalIndent(a.doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.path, append(b, '\n'), 0o600)
}

// codexAccount is the Codex account as the sign-in describes it.
func codexAccount() (Provider, bool) {
	a, ok := readCodex()
	if !ok {
		return Provider{}, false
	}
	idc := jwtClaims(a.tok("id_token"))
	user := claimEmail(idc)
	if user == "" {
		user = claimEmail(jwtClaims(a.tok("access_token")))
	}
	if user == "" {
		user = "ChatGPT account"
	}
	return Provider{ID: "codex", Name: "Codex", Icon: "openai", Account: "codex", Source: "Codex",
		User: user, Plan: codexPlan(idc), Catalog: "codex"}, true
}

// codexSession gives the request a current access token and the account id.
func codexSession(ctx context.Context, p Provider) (Provider, error) {
	codexMu.Lock()
	defer codexMu.Unlock()
	a, ok := readCodex()
	if !ok {
		return p, errors.New(codexRelogin)
	}
	if exp, ok := jwtExp(a.tok("access_token")); ok && time.Until(exp) < codexSkew {
		if err := refreshCodex(ctx, a); err != nil {
			return p, err
		}
	}
	p.Key = a.tok("access_token")
	p.Responses = CodexBase
	p.Headers = map[string]string{}
	if id := a.tok("account_id"); id != "" {
		p.Headers["chatgpt-account-id"] = id
	}
	return p, nil
}

// refreshCodex exchanges the refresh token the way Codex does and writes the
// new tokens back into Codex's file.
func refreshCodex(ctx context.Context, a *codexAuth) error {
	rt := a.tok("refresh_token")
	if rt == "" {
		return errors.New(codexRelogin)
	}
	body, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": rt, "scope": "openid profile email"})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexRefreshURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s（%v）", codexRelogin, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var v struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal(b, &v) != nil || v.AccessToken == "" {
		return errors.New(codexRelogin)
	}
	a.setTok("access_token", v.AccessToken)
	if v.RefreshToken != "" {
		a.setTok("refresh_token", v.RefreshToken)
	}
	if v.IDToken != "" {
		a.setTok("id_token", v.IDToken)
	}
	a.doc["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	if err := a.write(); err != nil {
		return fmt.Errorf("刷新后无法写回 Codex 的登录文件：%v", err)
	}
	return nil
}

// CodexBody rewrites a Responses request for the ChatGPT backend: the codex/
// prefix goes, it must stream and must not store, output caps and temperature
// are dropped, and a bare string input becomes a list of items.
func CodexBody(body []byte) []byte {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if dec.Decode(&m) != nil {
		return body
	}
	if s, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(s, "codex/")
	}
	m["stream"] = true
	m["store"] = false
	delete(m, "max_output_tokens")
	delete(m, "temperature")
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": s}}}}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// CodexFinal reads a Codex reply, which is always an event stream, and
// returns the finished response object the stream ends with. A client that
// asked for plain JSON gets that object.
func CodexFinal(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return nil, err
	}
	var final []byte
	var failure string
	for _, data := range sseData(b) {
		var ev struct {
			Type     string          `json:"type"`
			Message  string          `json:"message"`
			Error    json.RawMessage `json:"error"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal(data, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.completed", "response.incomplete":
			final = []byte(ev.Response)
		case "response.failed", "error":
			failure = firstNonEmpty(errMessage(ev.Error), errMessage(ev.Response), ev.Message, "Codex 返回错误")
		}
	}
	if len(final) > 0 {
		return final, nil
	}
	if failure != "" {
		return nil, errors.New(failure)
	}
	return nil, errors.New("Codex 没有返回完整的回复")
}

// sseData returns the data of each event in a stream, multi-line data joined.
func sseData(b []byte) [][]byte {
	var out [][]byte
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			out = append(out, cur)
		}
		cur = nil
	}
	for _, line := range bytes.Split(b, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if len(line) == 0 {
			flush()
			continue
		}
		if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			if len(cur) > 0 {
				cur = append(cur, '\n')
			}
			cur = append(cur, bytes.TrimSpace(rest)...)
		}
	}
	flush()
	return out
}

// errMessage pulls a message out of an {"message": …} or {"error": {"message": …}} object.
func errMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var o struct {
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return ""
	}
	if o.Message != "" {
		return o.Message
	}
	return errMessage(o.Error)
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// jwtClaims reads the payload of a JWT without checking its signature: dial
// only needs what the sign-in says about itself.
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

// jwtExp is the expiry of a JWT, when it has one.
func jwtExp(tok string) (time.Time, bool) {
	n, ok := jwtClaims(tok)["exp"].(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(n), 0), true
}

func claimEmail(c map[string]any) string {
	if e, ok := c["email"].(string); ok && e != "" {
		return e
	}
	if prof, ok := c["https://api.openai.com/profile"].(map[string]any); ok {
		if e, ok := prof["email"].(string); ok {
			return e
		}
	}
	return ""
}

func codexPlan(c map[string]any) string {
	if auth, ok := c["https://api.openai.com/auth"].(map[string]any); ok {
		if s, ok := auth["chatgpt_plan_type"].(string); ok {
			return s
		}
	}
	return ""
}
