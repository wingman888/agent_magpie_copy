package provider

// Account providers borrow the login an agent already keeps on this machine:
// Codex's ChatGPT sign-in and Copilot's GitHub sign-in. dial never copies
// those credentials anywhere. Each use reads the agent's own file again, so a
// logout removes the provider and a token refresh by the agent is picked up.
//
// Claude Code's login is only detected, so the CLI can say why it is not
// offered. Its contents are never read.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// CodexBase is the ChatGPT backend Codex talks to. It is a variable so tests
// can point it at a stand-in server.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// CopilotTokenURL is where a GitHub OAuth token is exchanged for a short-lived
// Copilot session token. A variable so tests can point it at a stand-in.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

// defaultCopilotAPI is used for Base until a session tells us the real one.
const defaultCopilotAPI = "https://api.githubcopilot.com"

const claudeWhy = "Anthropic's terms only allow a Claude subscription to be used by Claude Code itself, " +
	"so dial does not offer it as a provider. Add an Anthropic API key as a provider instead."

// Account says whose login a provider borrows.
type Account struct {
	Agent string `json:"agent"` // "codex" or "copilot"
	User  string `json:"user"`  // who is signed in, for display
	Plan  string `json:"plan,omitempty"`
}

// Exclusion is a login dial recognises but does not offer as a provider.
type Exclusion struct {
	Agent string `json:"agent"`
	Why   string `json:"why"`
}

// Owner names whom a provider stands for: the signed-in user for a borrowed
// login, else the provider's own name. Catalog notes show this.
func (p Provider) Owner() string {
	if p.Account != nil && p.Account.User != "" {
		return p.Account.User
	}
	return p.Name
}

// isAccountID reports whether an id is reserved for a borrowed login.
func isAccountID(id string) bool { return id == "codex" || id == "copilot" }

// hasBase reports whether the provider has at least one address of its own.
func (p Provider) hasBase() bool {
	return p.Chat != "" || p.Responses != "" || p.Anthropic != ""
}

// isAccountRecord is a stored entry that only holds a model selection for an
// account provider.
func isAccountRecord(p Provider) bool { return isAccountID(p.ID) && !p.hasBase() }

// customStored reports whether the user built their own provider with this id.
// That one wins over the borrowed login.
func customStored(f file, id string) bool {
	for _, p := range f.Providers {
		if p.ID == id && p.hasBase() {
			return true
		}
	}
	return false
}

// accountSignedIn reports whether the agent behind an account id is signed in.
func accountSignedIn(id string) bool {
	switch id {
	case "codex":
		_, ok := loadCodex()
		return ok
	case "copilot":
		_, ok := loadCopilot()
		return ok
	}
	return false
}

// SignedIn reports whether the agent behind a borrowed-login id is signed in.
// It is false for any other id.
func SignedIn(id string) bool { return accountSignedIn(id) }

// Accounts lists the borrowed logins that are signed in and offered as
// providers. A user-built provider with the same id takes precedence.
func Accounts() []Provider {
	f := load()
	var out []Provider
	if c, ok := loadCodex(); ok && !customStored(f, "codex") {
		out = append(out, accountProvider(f, "codex", "Codex", c.acct))
	}
	if c, ok := loadCopilot(); ok && !customStored(f, "copilot") {
		out = append(out, accountProvider(f, "copilot", "Copilot", c.acct))
	}
	return out
}

func accountProvider(f file, id, name string, acct Account) Provider {
	var models []string
	for _, p := range f.Providers {
		if p.ID == id && !p.hasBase() {
			models = cleanList(p.Models)
		}
	}
	return Provider{ID: id, Name: name, Models: models, Account: &acct}
}

// storedAccountModels is what the user picked for an account provider.
func storedAccountModels(id string) []string {
	f := load()
	for _, p := range f.Providers {
		if p.ID == id && !p.hasBase() {
			return cleanList(p.Models)
		}
	}
	return nil
}

// accountBase is the address an account provider talks to for a protocol.
func accountBase(p Provider, proto Protocol) string {
	switch {
	case p.Account.Agent == "codex" && proto == Responses:
		return CodexBase
	case p.Account.Agent == "copilot" && proto == Chat:
		return copilotAPIBase()
	}
	return ""
}

// accountSpeaks lists the protocols an account provider speaks.
func accountSpeaks(agent string) []Protocol {
	switch agent {
	case "codex":
		return []Protocol{Responses}
	case "copilot":
		return []Protocol{Chat}
	}
	return nil
}

// Excluded lists logins dial recognises but does not offer, with the reason.
// Every call checks the credentials again, so a login or logout is seen at once.
func Excluded() []Exclusion {
	var out []Exclusion
	if claudeSignedIn() {
		out = append(out, Exclusion{Agent: "claude", Why: claudeWhy})
	}
	return out
}

// ---- reading the agents' own credential files ------------------------------

func homeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

// codexLogin is a usable ChatGPT sign-in for Codex. The token stays here and
// is never stored by dial.
type codexLogin struct {
	acct      Account
	access    string
	accountID string
}

// codexHome is the Codex CLI's directory, always $HOME/.codex.
func codexHome() string {
	return filepath.Join(homeDir(), ".codex")
}

// loadCodex reads ~/.codex/auth.json. API-key mode is not an account.
func loadCodex() (codexLogin, bool) {
	b, err := os.ReadFile(filepath.Join(codexHome(), "auth.json"))
	if err != nil {
		return codexLogin{}, false
	}
	var a struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			IDToken     string `json:"id_token"`
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(b, &a) != nil {
		return codexLogin{}, false
	}
	if strings.EqualFold(strings.TrimSpace(a.AuthMode), "apikey") || a.Tokens.AccessToken == "" {
		return codexLogin{}, false
	}
	claims := jwtClaims(a.Tokens.IDToken)
	acct := Account{Agent: "codex", User: "ChatGPT"}
	if email, _ := claims["email"].(string); email != "" {
		acct.User = email
	}
	if oa, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		acct.Plan, _ = oa["chatgpt_plan_type"].(string)
	}
	return codexLogin{acct: acct, access: a.Tokens.AccessToken, accountID: a.Tokens.AccountID}, true
}

// jwtClaims returns the payload of a JWT without checking its signature. dial
// only reads display details from it.
func jwtClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// copilotLogin is a GitHub sign-in that Copilot uses.
type copilotLogin struct {
	acct  Account
	oauth string
}

func copilotDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(homeDir(), ".config")
	}
	return filepath.Join(base, "github-copilot")
}

// loadCopilot reads the github.com entry from apps.json or hosts.json.
func loadCopilot() (copilotLogin, bool) {
	for _, name := range []string{"apps.json", "hosts.json"} {
		b, err := os.ReadFile(filepath.Join(copilotDir(), name))
		if err != nil {
			continue
		}
		var entries map[string]json.RawMessage
		if json.Unmarshal(b, &entries) != nil {
			continue
		}
		var keys []string
		for k := range entries {
			if strings.HasPrefix(k, "github.com") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			var e struct {
				User  string `json:"user"`
				OAuth string `json:"oauth_token"`
			}
			if json.Unmarshal(entries[k], &e) != nil || e.OAuth == "" {
				continue
			}
			user := e.User
			if user == "" {
				user = "GitHub"
			}
			return copilotLogin{acct: Account{Agent: "copilot", User: user}, oauth: e.OAuth}, true
		}
	}
	return copilotLogin{}, false
}

// claudeSignedIn only checks that Claude Code's credentials exist. Their
// contents are never read. The credential file is checked first; on macOS,
// where Claude Code may keep its login in the keychain instead, the keychain
// item is looked up by name only. That lookup runs without -w, so no secret
// is printed, and only its exit status is read. It never prompts for access
// because the item's data is not requested.
func claudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(homeDir(), ".claude")
	}
	info, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	if err == nil && info.Mode().IsRegular() {
		return true
	}
	return runtime.GOOS == "darwin" && claudeKeychainItem()
}

// claudeKeychainItem reports whether the macOS keychain has Claude Code's
// credential item. Only the exit status matters: the output is discarded.
func claudeKeychainItem() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}
