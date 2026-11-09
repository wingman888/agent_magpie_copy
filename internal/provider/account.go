package provider

// Account providers borrow a login the user already made in another agent:
// Codex (ChatGPT sign-in) and GitHub Copilot. They carry no key and nothing
// secret is copied into dial's own files: the credentials are read from the
// agent's files each time they are needed. Only the user's model picks are
// stored (see Save and Delete).
//
// Claude Code is deliberately not a provider. dial only checks that its
// login exists, so it can say so; it never reads the credentials.

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
	"sort"
	"strings"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// Provider ids of the account providers, and the one that is never offered.
const (
	codexID   = "codex"
	copilotID = "copilot"
	claudeID  = "claude"
)

// CodexBase is the base URL of the ChatGPT Codex backend. Requests for
// codex/<model> go to CodexBase + "/responses". Tests point it at a fake.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// copilotBase is the default Copilot API address. The session the GitHub
// token is exchanged for names the real one; that exchange is not done here.
const copilotBase = "https://api.githubcopilot.com"

// codexRefreshWithin is how close to expiry a Codex token is refreshed.
const codexRefreshWithin = 5 * time.Minute

// codexClientID and codexRefreshURL are the ones the Codex CLI uses to refresh.
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

var codexRefreshURL = "https://auth.openai.com/oauth/token"

// Messages the user sees when a borrowed login no longer works.
var (
	ErrCodexLoggedOut  = errors.New("Codex 已登出，请运行 `codex login` 重新登录")
	ErrCopilotSession  = errors.New("Copilot 会话已失效，请重新登录 Copilot")
	errCodexWriteBack  = errors.New("写回 Codex 登录文件失败")
	claudeReason       = "Claude Code 的订阅登录只供 Claude Code 自身使用，Anthropic 不允许把它当作 API 提供给其他工具，所以 dial 不将其作为 Provider 提供。"
	claudeNoticeHeader = "Claude Code 已登录，但不提供为 Provider。原因："
)

// Account says whose login an account provider uses.
type Account struct {
	User    string    `json:"user"`             // GitHub login, or ChatGPT email
	Plan    string    `json:"plan,omitempty"`   // subscription plan, when the login says
	Source  string    `json:"source"`           // the agent the login was read from
	Expires time.Time `json:"expires,omitzero"` // when the access token runs out, if known
}

// Notice is something dial says about an agent it does not offer.
type Notice struct {
	Agent  string `json:"agent"`
	Signed bool   `json:"signed"`
	Text   string `json:"text"`
}

// isAccountID reports whether id names an account provider.
func isAccountID(id string) bool { return id == codexID || id == copilotID }

// reservedID is true for ids that never come from the user's own providers.
func reservedID(id string) bool { return id == claudeID || isAccountID(id) }

// accountProviders builds the account providers whose agents are signed in.
// Each call reads the agents' files afresh.
func accountProviders(f file) []Provider {
	stored := func(id string) []string {
		for _, a := range f.Accounts {
			if a.ID == id {
				return cleanList(a.Models)
			}
		}
		return nil
	}
	var out []Provider
	if a, ok := detectCodex(); ok {
		out = append(out, Provider{ID: codexID, Name: "Codex", Responses: CodexBase, Models: stored(codexID), Account: a})
	}
	if a, ok := detectCopilot(); ok {
		out = append(out, Provider{ID: copilotID, Name: "GitHub Copilot", Chat: copilotBase, Models: stored(copilotID), Account: a})
	}
	return out
}

// Notices lists what dial says about agents it does not offer as providers.
func Notices() []Notice {
	var out []Notice
	if claudeSignedIn() {
		out = append(out, Notice{Agent: "Claude Code", Signed: true, Text: claudeNoticeHeader + claudeReason})
	}
	return out
}

// ---- Codex -----------------------------------------------------------------

type codexTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

type codexFile struct {
	AuthMode string      `json:"auth_mode"`
	Tokens   codexTokens `json:"tokens"`
}

// codexPath is Codex's own login file.
func codexPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// detectCodex reports the ChatGPT login of Codex, if there is one. An
// API-key login is not an account.
func detectCodex() (*Account, bool) {
	b, err := os.ReadFile(codexPath())
	if err != nil {
		return nil, false
	}
	var f codexFile
	if json.Unmarshal(b, &f) != nil || f.AuthMode != "chatgpt" || f.Tokens.AccessToken == "" {
		return nil, false
	}
	id, _ := jwtClaims(f.Tokens.IDToken)
	email := claimString(id, "email")
	if email == "" {
		if prof, ok := id["https://api.openai.com/profile"].(map[string]any); ok {
			email = claimString(prof, "email")
		}
	}
	if email == "" {
		email = f.Tokens.AccountID
	}
	plan := planOf(id)
	acc, _ := jwtClaims(f.Tokens.AccessToken)
	if plan == "" {
		plan = planOf(acc)
	}
	exp, _ := jwtExp(f.Tokens.AccessToken)
	return &Account{User: email, Plan: plan, Source: "Codex", Expires: exp}, true
}

// planOf reads the ChatGPT plan out of a Codex token's claims.
func planOf(claims map[string]any) string {
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		return claimString(auth, "chatgpt_plan_type")
	}
	return ""
}

// CodexToken returns the access token and account id to call the Codex
// backend with. It reads auth.json on every call. A token close to expiry is
// refreshed the way Codex does it, and the new tokens are written back with
// the rest of the file left as it was.
func CodexToken(ctx context.Context) (token, accountID string, err error) {
	b, err := os.ReadFile(codexPath())
	if err != nil {
		return "", "", ErrCodexLoggedOut
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil || doc["auth_mode"] != "chatgpt" {
		return "", "", ErrCodexLoggedOut
	}
	tokens, _ := doc["tokens"].(map[string]any)
	access := claimString(tokens, "access_token")
	accountID = claimString(tokens, "account_id")
	if access == "" {
		return "", "", ErrCodexLoggedOut
	}
	if exp, ok := jwtExp(access); !ok || time.Until(exp) > codexRefreshWithin {
		return access, accountID, nil
	}
	refresh := claimString(tokens, "refresh_token")
	if refresh == "" {
		return "", "", ErrCodexLoggedOut
	}
	fresh, err := refreshCodex(ctx, refresh)
	if err != nil {
		return "", "", ErrCodexLoggedOut
	}
	for k, v := range map[string]string{"id_token": fresh.IDToken, "access_token": fresh.AccessToken, "refresh_token": fresh.RefreshToken} {
		if v != "" {
			tokens[k] = v
		}
	}
	doc["tokens"] = tokens
	doc["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", errCodexWriteBack, err)
	}
	if err := os.WriteFile(codexPath(), append(out, '\n'), 0o600); err != nil {
		return "", "", fmt.Errorf("%w: %v", errCodexWriteBack, err)
	}
	return fresh.AccessToken, accountID, nil
}

// refreshCodex trades a refresh token for a new set of tokens.
func refreshCodex(ctx context.Context, refresh string) (codexTokens, error) {
	body, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token", "refresh_token": refresh})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexRefreshURL, bytes.NewReader(body))
	if err != nil {
		return codexTokens{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return codexTokens{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return codexTokens{}, fmt.Errorf("refresh: HTTP %d", resp.StatusCode)
	}
	var t struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
		return codexTokens{}, err
	}
	if t.AccessToken == "" {
		return codexTokens{}, errors.New("refresh: no access token in reply")
	}
	return codexTokens{IDToken: t.IDToken, AccessToken: t.AccessToken, RefreshToken: t.RefreshToken}, nil
}

// ---- Copilot ---------------------------------------------------------------

type copilotApp struct {
	User       string `json:"user"`
	OAuthToken string `json:"oauth_token"`
}

// copilotPath is where the Copilot plugin keeps its GitHub login.
func copilotPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "github-copilot", "apps.json")
}

// detectCopilot reports the GitHub login Copilot uses, if there is one.
func detectCopilot() (*Account, bool) {
	b, err := os.ReadFile(copilotPath())
	if err != nil {
		return nil, false
	}
	var apps map[string]json.RawMessage
	if json.Unmarshal(b, &apps) != nil {
		return nil, false
	}
	keys := make([]string, 0, len(apps))
	for k := range apps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !strings.HasPrefix(k, "github.com") {
			continue
		}
		var app copilotApp
		if json.Unmarshal(apps[k], &app) != nil || app.OAuthToken == "" {
			continue
		}
		user := app.User
		if user == "" {
			user = "GitHub"
		}
		return &Account{User: user, Source: "GitHub Copilot"}, true
	}
	return nil, false
}

// CopilotGitHubToken returns the GitHub token Copilot signs in with. It reads
// the file on every call. A missing token gives ErrCopilotSession.
func CopilotGitHubToken() (string, error) {
	b, err := os.ReadFile(copilotPath())
	if err != nil {
		return "", ErrCopilotSession
	}
	var apps map[string]json.RawMessage
	if json.Unmarshal(b, &apps) != nil {
		return "", ErrCopilotSession
	}
	for k, raw := range apps {
		if !strings.HasPrefix(k, "github.com") {
			continue
		}
		var app copilotApp
		if json.Unmarshal(raw, &app) == nil && app.OAuthToken != "" {
			return app.OAuthToken, nil
		}
	}
	return "", ErrCopilotSession
}

// ---- Claude Code -----------------------------------------------------------

// claudeSignedIn only checks that Claude Code's credentials file exists. The
// contents are never read.
func claudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	_, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	return err == nil
}

// ---- models ----------------------------------------------------------------

// accountModels lists the models of an account provider: the live list when
// one was fetched, else the built-in list. "auto" is never offered.
func (p Provider) accountModels() []catalog.Model {
	src, _, ok := catalog.Live(p.ID)
	if !ok {
		src = catalog.Builtin(p.ID)
	}
	var out []catalog.Model
	for _, m := range src {
		if m.ID != "auto" {
			out = append(out, m)
		}
	}
	return out
}

// ---- JWT helpers -----------------------------------------------------------

// jwtClaims decodes the payload of a JWT without checking its signature.
func jwtClaims(tok string) (map[string]any, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return map[string]any{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return map[string]any{}, false
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return map[string]any{}, false
	}
	return m, true
}

// jwtExp is the exp claim of a JWT.
func jwtExp(tok string) (time.Time, bool) {
	m, ok := jwtClaims(tok)
	if !ok {
		return time.Time{}, false
	}
	n, ok := m["exp"].(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(n), 0), true
}

// claimString reads a string field from a decoded map.
func claimString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
