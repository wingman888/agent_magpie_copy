package provider

// Agent accounts: a provider can be the login of an agent the user already
// signed in to (Codex, Copilot). dial reads that agent's own credential file
// each time it needs it and never copies the secret anywhere of its own. The
// only thing dial stores for such a provider is the user's model picks.

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Account describes a signed-in agent whose subscription dial offers as a provider.
type Account struct {
	Agent string `json:"agent"`          // the agent it comes from: "codex" or "copilot"
	User  string `json:"user"`           // ChatGPT email, or GitHub login
	Plan  string `json:"plan,omitempty"` // subscription plan, when the login reports one
}

// CodexBase is the ChatGPT Codex backend. Requests for codex/<model> go to
// CodexBase + "/responses". Tests point it at a local fake server.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// copilotBase is the Copilot API host, used until a session names another.
const copilotBase = "https://api.githubcopilot.com"

// claudeID is Claude Code's agent id. Its login is detected, never offered.
const claudeID = "claude"

// accountIDs are the agent-backed providers, in display order.
var accountIDs = []string{"codex", "copilot"}

// IsAccount reports whether id names a provider backed by an agent's login.
func IsAccount(id string) bool {
	for _, a := range accountIDs {
		if id == a {
			return true
		}
	}
	return false
}

// codexCreds is what dial reads from Codex's login file.
type codexCreds struct {
	Access    string
	Refresh   string
	IDToken   string
	AccountID string
	Email     string
	Plan      string
	Expires   time.Time // when the access token runs out
}

// codexLogin reads ~/.codex/auth.json. It is false when Codex is not signed in
// with ChatGPT: no file, API-key mode, or no tokens.
func codexLogin() (codexCreds, bool) {
	b, err := os.ReadFile(codexFile())
	if err != nil {
		return codexCreds{}, false
	}
	var f struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			IDToken      string `json:"id_token"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(b, &f) != nil || f.AuthMode != "chatgpt" {
		return codexCreds{}, false
	}
	t := f.Tokens
	if t.AccessToken == "" && t.RefreshToken == "" {
		return codexCreds{}, false
	}
	c := codexCreds{
		Access:    t.AccessToken,
		Refresh:   t.RefreshToken,
		IDToken:   t.IDToken,
		AccountID: t.AccountID,
	}
	if claims := jwtPayload(t.IDToken); claims != nil {
		c.Email, _ = claims["email"].(string)
		auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
		c.Plan, _ = auth["chatgpt_plan_type"].(string)
	}
	if claims := jwtPayload(t.AccessToken); claims != nil {
		if exp, ok := claims["exp"].(float64); ok {
			c.Expires = time.Unix(int64(exp), 0)
		}
	}
	return c, true
}

// jwtPayload decodes the claims in the middle of a JWT. It does not verify
// the signature; nil when the token is not a JWT.
func jwtPayload(tok string) map[string]any {
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

// copilotCreds is the GitHub identity behind a Copilot login.
type copilotCreds struct {
	User       string
	OAuthToken string
}

// copilotLogin reads GitHub Copilot's apps.json, under $XDG_CONFIG_HOME when
// that is set. The entry is keyed "github.com…".
func copilotLogin() (copilotCreds, bool) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	b, err := os.ReadFile(filepath.Join(dir, "github-copilot", "apps.json"))
	if err != nil {
		return copilotCreds{}, false
	}
	var apps map[string]struct {
		User       string `json:"user"`
		OAuthToken string `json:"oauth_token"`
	}
	if json.Unmarshal(b, &apps) != nil {
		return copilotCreds{}, false
	}
	keys := make([]string, 0, len(apps))
	for k := range apps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a := apps[k]
		if strings.HasPrefix(k, "github.com") && a.OAuthToken != "" {
			return copilotCreds{User: a.User, OAuthToken: a.OAuthToken}, true
		}
	}
	return copilotCreds{}, false
}

// claudeSignedIn reports whether Claude Code has a login on disk. It checks
// only that the file exists; the contents are never read.
func claudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	st, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	return err == nil && !st.IsDir()
}

// ClaudeNotice explains why a signed-in Claude Code is not offered as a
// provider. It is empty when Claude Code is not signed in.
func ClaudeNotice() string {
	if !claudeSignedIn() {
		return ""
	}
	return "Claude Code 已登录，但不作为 Provider 提供：它的登录令牌只供 Claude Code 自己使用，dial 不读取也不转发。"
}

// signedInAs reports the account behind an agent-backed provider, if that
// agent is signed in right now.
func signedInAs(id string) (Account, bool) {
	switch id {
	case "codex":
		c, ok := codexLogin()
		if !ok {
			return Account{}, false
		}
		user := c.Email
		if user == "" {
			user = c.AccountID
		}
		return Account{Agent: "codex", User: user, Plan: c.Plan}, true
	case "copilot":
		c, ok := copilotLogin()
		if !ok {
			return Account{}, false
		}
		return Account{Agent: "copilot", User: c.User}, true
	}
	return Account{}, false
}

// accountProvider is the provider view of a signed-in agent. It carries no
// key: the agent's login is used at call time, and Ready() is true.
func accountProvider(acc Account, models []string) Provider {
	p := Provider{ID: acc.Agent, Models: models, Account: &acc}
	switch acc.Agent {
	case "codex":
		p.Name, p.Icon, p.Website = "Codex", "codex-color", "https://chatgpt.com"
		p.Responses = CodexBase
	case "copilot":
		p.Name, p.Icon, p.Website = "GitHub Copilot", "githubcopilot", "https://github.com/features/copilot"
		p.Chat = copilotBase
	}
	return p
}

// setAccountModels records the user's model picks for an agent-backed
// provider. Nothing else is written; an empty list removes the record.
func setAccountModels(id string, models []string) error {
	models = cleanList(models)
	f := load()
	idx := -1
	for i, p := range f.Providers {
		if strings.ToLower(strings.TrimSpace(p.ID)) == id {
			idx = i
			break
		}
	}
	switch {
	case len(models) == 0 && idx >= 0:
		f.Providers = append(f.Providers[:idx], f.Providers[idx+1:]...)
	case len(models) == 0:
		return nil
	case idx >= 0:
		f.Providers[idx] = Provider{ID: id, Models: models}
	default:
		f.Providers = append(f.Providers, Provider{ID: id, Models: models})
	}
	return store(f)
}
