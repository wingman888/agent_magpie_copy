package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Some agents hold a signed-in account whose models other agents may
// borrow through the gateway: Codex (a ChatGPT login) and Copilot (a GitHub
// login). dial never stores those credentials. The agent's own files are
// the only source and are read again every time they are needed, so a
// sign-out in the agent removes the provider and a token the agent
// refreshed is the one dial uses.

// Account says which agent login a provider stands for. It carries no token.
type Account struct {
	Agent string `json:"agent"` // "codex" or "copilot"
	User  string `json:"user"`
	Plan  string `json:"plan,omitempty"`
}

// AgentName is the agent's display name.
func (a Account) AgentName() string { return AgentName(a.Agent) }

// AgentName names an agent for display.
func AgentName(agent string) string {
	switch agent {
	case "codex":
		return "Codex"
	case "copilot":
		return "Copilot"
	case "claude":
		return "Claude Code"
	}
	return agent
}

// Exclusion is a login dial recognises but deliberately does not offer.
type Exclusion struct {
	Agent string `json:"agent"`
	Why   string `json:"why"`
}

const claudeWhy = "Anthropic's terms only allow a Claude subscription to be used with Claude Code itself, " +
	"so dial does not use it as a provider; add an Anthropic API key as a provider instead (dial provider add anthropic <key>)"

func isAccountID(id string) bool { return id == "codex" || id == "copilot" }

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// ---- Codex ------------------------------------------------------------------

func codexAuthPath() string { return filepath.Join(homeDir(), ".codex", "auth.json") }

type codexCreds struct {
	Access, Refresh, IDToken, AccountID string
}

// readCodex reads Codex's sign-in. Only a ChatGPT login counts; an API key
// login (auth_mode apikey, or a key and no tokens) is not an account.
func readCodex() (codexCreds, bool) {
	b, err := os.ReadFile(codexAuthPath())
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
	if json.Unmarshal(b, &f) != nil {
		return codexCreds{}, false
	}
	mode := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(f.AuthMode), "_", ""))
	if mode == "apikey" || f.Tokens.AccessToken == "" {
		return codexCreds{}, false
	}
	c := codexCreds{Access: f.Tokens.AccessToken, Refresh: f.Tokens.RefreshToken, IDToken: f.Tokens.IDToken, AccountID: f.Tokens.AccountID}
	if c.AccountID == "" {
		if a, _ := jwtClaims(c.IDToken)["https://api.openai.com/auth"].(map[string]any); a != nil {
			c.AccountID, _ = a["chatgpt_account_id"].(string)
		}
	}
	return c, true
}

// jwtClaims decodes a JWT payload without checking the signature; dial only
// uses it to show who is signed in and when the token runs out.
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

func codexAccount() (Provider, bool) {
	c, ok := readCodex()
	if !ok {
		return Provider{}, false
	}
	acc := Account{Agent: "codex", User: "ChatGPT"}
	claims := jwtClaims(c.IDToken)
	if e, _ := claims["email"].(string); e != "" {
		acc.User = e
	}
	if a, _ := claims["https://api.openai.com/auth"].(map[string]any); a != nil {
		acc.Plan, _ = a["chatgpt_plan_type"].(string)
	}
	return Provider{ID: "codex", Name: "Codex", Icon: "openai", Responses: CodexBase, Account: &acc,
		Website: "https://chatgpt.com"}, true
}

// ---- Copilot ----------------------------------------------------------------

func copilotDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "github-copilot")
	}
	return filepath.Join(homeDir(), ".config", "github-copilot")
}

// readCopilot returns the GitHub user and OAuth token Copilot signed in with.
func readCopilot() (user, token string, ok bool) {
	for _, name := range []string{"apps.json", "hosts.json"} {
		b, err := os.ReadFile(filepath.Join(copilotDir(), name))
		if err != nil {
			continue
		}
		var m map[string]struct {
			User  string `json:"user"`
			Token string `json:"oauth_token"`
		}
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if strings.HasPrefix(k, "github.com") && m[k].Token != "" {
				return m[k].User, m[k].Token, true
			}
		}
	}
	return "", "", false
}

func copilotAccount() (Provider, bool) {
	user, _, ok := readCopilot()
	if !ok {
		return Provider{}, false
	}
	if user == "" {
		user = "GitHub"
	}
	return Provider{ID: "copilot", Name: "Copilot", Icon: "githubcopilot", Chat: copilotPlaceholder,
		Account: &Account{Agent: "copilot", User: user}, Website: "https://github.com/features/copilot"}, true
}

// copilotPlaceholder stands in for the API address until Sign learns the
// real one from the session token.
const copilotPlaceholder = "https://api.githubcopilot.com"

// ---- listing ----------------------------------------------------------------

// Accounts lists the agent logins dial offers as providers right now.
func Accounts() []Provider {
	var out []Provider
	if p, ok := codexAccount(); ok {
		out = append(out, p)
	}
	if p, ok := copilotAccount(); ok {
		out = append(out, p)
	}
	return out
}

func accountFor(id string) (Provider, bool) {
	switch id {
	case "codex":
		return codexAccount()
	case "copilot":
		return copilotAccount()
	}
	return Provider{}, false
}

var keychain struct {
	sync.Mutex
	key string
	at  time.Time
	ok  bool
}

// Excluded lists the logins dial recognises but does not offer, with the
// reason. The credentials file is a cheap stat and always looked at afresh;
// the macOS keychain probe is kept for about a minute.
func Excluded() []Exclusion {
	if claudeSignedIn() {
		return []Exclusion{{Agent: "claude", Why: claudeWhy}}
	}
	return nil
}

// claudeSignedIn only asks whether a Claude Code login exists. The
// credentials are never opened and the keychain is asked for existence only
// (no -w/-g), which does not raise an authorisation prompt.
func claudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(homeDir(), ".claude")
	}
	if st, err := os.Stat(filepath.Join(dir, ".credentials.json")); err == nil && st.Mode().IsRegular() {
		return true
	}
	if runtime.GOOS == "darwin" {
		key := homeDir() + "\x00" + os.Getenv("CLAUDE_CONFIG_DIR")
		keychain.Lock()
		defer keychain.Unlock()
		if keychain.key == key && time.Since(keychain.at) < time.Minute {
			return keychain.ok
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ok := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials").Run() == nil
		keychain.key, keychain.at, keychain.ok = key, time.Now(), ok
		return ok
	}
	return false
}
