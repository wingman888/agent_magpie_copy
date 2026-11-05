package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// Account providers are agents the user is already signed in to: Codex
// (ChatGPT login) and Copilot (GitHub login). Their subscription's models
// become dial models. dial never stores their login: every use reads the
// agent's own credential file again.
const (
	CodexID   = "codex"
	CopilotID = "copilot"
	ClaudeID  = "claude"
)

// CodexBase is the ChatGPT backend Codex models are served from. A variable
// so tests can point it at a local fake server.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// claudeReason is shown wherever Claude Code is signed in but not offered.
const claudeReason = "Claude Code 已登录但不提供：Claude 的登录凭据只属于 Claude Code 自身，" +
	"dial 不读取也不转发它，因此不作为 Provider 或模型提供。 " +
	"(Claude Code is signed in but not offered as a provider: its login is not allowed to be forwarded to other providers)"

// IsAccount reports whether id names an account provider.
func IsAccount(id string) bool { return id == CodexID || id == CopilotID }

// Account is the identity behind a signed-in account provider.
type Account struct {
	User    string    `json:"user"`              // email (Codex) or GitHub login (Copilot)
	Plan    string    `json:"plan,omitempty"`    // subscription plan, when the login says
	Agent   string    `json:"agent"`             // the agent the login belongs to
	Expires time.Time `json:"expires,omitempty"` // Codex access token expiry; zero when unknown
}

// accountProvider builds the provider for an account id from the agent's
// live login. ok is false when that agent is not signed in (or, for Codex,
// signed in with an API key), in which case any saved picks are ignored.
func accountProvider(id string, picks []string) (Provider, bool) {
	var p Provider
	switch id {
	case CodexID:
		a, ok := codexAccount()
		if !ok {
			return p, false
		}
		p = Provider{ID: CodexID, Name: "Codex", Icon: "openai", Responses: CodexBase, Account: a}
	case CopilotID:
		a, ok := copilotAccount()
		if !ok {
			return p, false
		}
		p = Provider{ID: CopilotID, Name: "GitHub Copilot", Icon: "githubcopilot", Chat: copilotDefaultAPI, Account: a}
	default:
		return p, false
	}
	p.Models = cleanList(picks)
	return p, true
}

// saveAccount stores only the user's model picks for an account provider.
// Key, name and the rest are not kept.
func saveAccount(p Provider) error {
	f := load()
	rec := Provider{ID: p.ID, Models: cleanList(p.Models)}
	for i := range f.Providers {
		if f.Providers[i].ID == p.ID {
			f.Providers[i] = rec
			return store(f)
		}
	}
	f.Providers = append(f.Providers, rec)
	return store(f)
}

// deleteAccount clears the user's picks for an account provider. It does
// not sign the agent out, and the provider stays listed while signed in.
func deleteAccount(id string) error {
	f := load()
	keep := f.Providers[:0]
	for _, p := range f.Providers {
		if p.ID != id {
			keep = append(keep, p)
		}
	}
	removed := len(keep) != len(f.Providers)
	if !removed {
		if _, ok := accountProvider(id, nil); !ok {
			return fmt.Errorf("no provider %q", id)
		}
	}
	f.Providers = keep
	return store(f)
}

// ---- Codex -----------------------------------------------------------------

// codexAuth is the part of ~/.codex/auth.json dial reads.
type codexAuth struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

func codexPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// readCodex reads the Codex login file as it is now.
func readCodex() (codexAuth, error) {
	var a codexAuth
	b, err := os.ReadFile(codexPath())
	if err != nil {
		return a, err
	}
	err = json.Unmarshal(b, &a)
	return a, err
}

// codexAccount reports the ChatGPT login. API-key logins are not accounts.
func codexAccount() (*Account, bool) {
	a, err := readCodex()
	if err != nil || a.AuthMode != "chatgpt" || a.Tokens.AccessToken == "" {
		return nil, false
	}
	idClaims := jwtPayload(a.Tokens.IDToken)
	accClaims := jwtPayload(a.Tokens.AccessToken)
	user := claimString(idClaims, "email")
	if user == "" {
		user = claimString(profileClaim(accClaims), "email")
	}
	if user == "" {
		user = "ChatGPT account"
	}
	plan := claimString(authClaim(idClaims), "chatgpt_plan_type")
	if plan == "" {
		plan = claimString(authClaim(accClaims), "chatgpt_plan_type")
	}
	acc := &Account{User: user, Plan: plan, Agent: "Codex"}
	if exp, ok := accClaims["exp"].(float64); ok {
		acc.Expires = time.Unix(int64(exp), 0)
	}
	return acc, true
}

// jwtPayload decodes the payload of a JWT without checking its signature.
// dial only reads display facts from it; the gateway never trusts it.
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

func claimString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func authClaim(m map[string]any) map[string]any {
	a, _ := m["https://api.openai.com/auth"].(map[string]any)
	return a
}

func profileClaim(m map[string]any) map[string]any {
	a, _ := m["https://api.openai.com/profile"].(map[string]any)
	return a
}

// ---- Copilot ---------------------------------------------------------------

// copilotAppsPath is where the Copilot CLI and editor plugins keep the GitHub
// login. XDG_CONFIG_HOME wins when set.
func copilotAppsPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "github-copilot", "apps.json")
}

// copilotLogin reads the GitHub login from apps.json as it is now.
func copilotLogin() (user, oauth string, ok bool) {
	b, err := os.ReadFile(copilotAppsPath())
	if err != nil {
		return "", "", false
	}
	var apps map[string]json.RawMessage
	if json.Unmarshal(b, &apps) != nil {
		return "", "", false
	}
	keys := make([]string, 0, len(apps))
	for k := range apps {
		if strings.HasPrefix(k, "github.com") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		var v struct {
			User  string `json:"user"`
			OAuth string `json:"oauth_token"`
		}
		if json.Unmarshal(apps[k], &v) == nil && v.OAuth != "" {
			return v.User, v.OAuth, true
		}
	}
	return "", "", false
}

func copilotAccount() (*Account, bool) {
	user, _, ok := copilotLogin()
	if !ok {
		return nil, false
	}
	if user == "" {
		user = "GitHub user"
	}
	return &Account{User: user, Agent: "Copilot"}, true
}

// ---- Claude Code (detection only) ------------------------------------------

// claudeCredentials is where Claude Code keeps its login. Dial only checks
// that the file exists; it never opens it.
func claudeCredentials() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}

// Notice explains an agent that is signed in but deliberately not offered.
type Notice struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Notices lists the signed-in agents that are not offered as providers.
func Notices() []Notice {
	var out []Notice
	if info, err := os.Stat(claudeCredentials()); err == nil && !info.IsDir() {
		out = append(out, Notice{ID: ClaudeID, Name: "Claude Code", Reason: claudeReason})
	}
	return out
}

// ---- account models --------------------------------------------------------

// accountModels is the model list of an account provider: the last live
// list when one was fetched, else the built-in list.
func (p Provider) accountModels() []catalog.Model {
	if live, _, ok := catalog.Live(p.ID); ok {
		return live
	}
	return builtinAccountModels(p.ID)
}

// builtinAccountModels is the list used before any refresh. Copilot's
// built-in list never offers "auto".
func builtinAccountModels(id string) []catalog.Model {
	var out []catalog.Model
	for _, m := range catalog.Builtin(id) {
		if id == CopilotID && m.ID == "auto" {
			continue
		}
		out = append(out, m)
	}
	return out
}

// fetchAccount refreshes an account provider's model list.
func (p Provider) fetchAccount(ctx context.Context) ([]catalog.Model, error) {
	switch p.ID {
	case CodexID:
		// Codex's models come with the built-in list; there is nothing to ask.
		return p.Available(), nil
	case CopilotID:
		base, ms, err := fetchCopilotModels(ctx)
		if err != nil {
			return nil, err
		}
		return ms, catalog.SaveLive(p.ID, base, ms)
	}
	return nil, errors.New("no model list for " + p.Name)
}
