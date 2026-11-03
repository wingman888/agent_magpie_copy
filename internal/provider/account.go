package provider

// Account providers ride on a login the agent already holds: Codex's ChatGPT
// subscription, GitHub Copilot's. dial reads the agent's credential file each
// time it needs it and never copies a secret into its own config, so logging
// out in the agent makes the provider disappear here too.

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// Account says whose subscription an account provider uses.
type Account struct {
	Agent string `json:"agent"`          // "codex" or "copilot"
	User  string `json:"user"`           // email (Codex) or GitHub user (Copilot)
	Plan  string `json:"plan,omitempty"` // subscription tier, e.g. "pro"; may be empty
}

// Exclusion is a login dial recognises but does not offer as a provider.
type Exclusion struct {
	Agent string `json:"agent"`
	Why   string `json:"why"`
}

// claudeWhy is fixed text: it names no account and carries no credentials.
const claudeWhy = "Anthropic's terms allow a Claude subscription only for Claude Code itself, " +
	"so dial does not offer it as a provider. Add an Anthropic API key as a provider instead."

// excludedTTL is how long Excluded results are reused.
const excludedTTL = time.Minute

// login is what detection found for one agent. The secret fields are kept in
// memory only, for the signer; they are never written to dial's config.
type login struct {
	Account
	token     string // Codex access token, or Copilot oauth_token
	accountID string // Codex ChatGPT account id
}

// accountSpec ties an account provider to its detection and built-in models.
type accountSpec struct {
	id      string
	name    string
	detect  func() (login, bool)
	builtin func() []catalog.Model
}

var accountSpecs = []accountSpec{
	{id: "codex", name: "Codex", detect: codexLogin, builtin: codexBuiltin},
	{id: "copilot", name: "Copilot", detect: copilotLogin, builtin: copilotBuiltin},
}

// Accounts lists the signed-in account providers dial offers: ready without a
// key, with the user's model selection or the whole list the login offers. A
// user-defined provider with the same id and an address takes precedence over
// the account.
func Accounts() []Provider {
	stored := load().Providers
	var out []Provider
	for _, spec := range accountSpecs {
		l, ok := spec.detect()
		if !ok {
			continue
		}
		rec, has := findStored(stored, spec.id)
		if has && hasAddress(rec) {
			continue
		}
		acct := l.Account
		p := Provider{ID: spec.id, Name: spec.name, Account: &acct}
		if has {
			p.Models = cleanList(rec.Models)
			p.Listed = slices.Clone(rec.Listed)
			p.Efforts = rec.Efforts
		}
		if len(p.Models) == 0 {
			p.Models = modelIDs(p.accountModels())
		}
		out = append(out, p)
	}
	return out
}

// findStored returns the stored record with the given id, if any.
func findStored(stored []Provider, id string) (Provider, bool) {
	for _, p := range stored {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

var excluded struct {
	sync.Mutex
	at  time.Time
	key string
	val []Exclusion
}

// Excluded lists logins dial recognises but does not offer. The result is
// cached for about a minute, keyed by the environment that locates the files.
func Excluded() []Exclusion {
	excluded.Lock()
	defer excluded.Unlock()
	key := detectKey()
	if !excluded.at.IsZero() && excluded.key == key && time.Since(excluded.at) < excludedTTL {
		return slices.Clone(excluded.val)
	}
	var out []Exclusion
	if claudeSignedIn() {
		out = append(out, Exclusion{Agent: "claude", Why: claudeWhy})
	}
	excluded.at, excluded.key, excluded.val = time.Now(), key, out
	return slices.Clone(out)
}

// resetExcluded drops the Excluded cache; tests use it to see fresh results.
func resetExcluded() {
	excluded.Lock()
	defer excluded.Unlock()
	excluded.at = time.Time{}
}

func detectKey() string {
	return strings.Join([]string{os.Getenv("HOME"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("CLAUDE_CONFIG_DIR")}, "\x00")
}

// ---- Codex -----------------------------------------------------------------

type codexAuth struct {
	AuthMode     string `json:"auth_mode"`
	OpenAIAPIKey string `json:"OPENAI_API_KEY"`
	Tokens       *struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

// codexAuthPath is ~/.codex/auth.json, located through HOME.
func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// codexLogin reports a ChatGPT-mode Codex login. API-key mode is not an account.
func codexLogin() (login, bool) {
	b, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return login{}, false
	}
	var a codexAuth
	if json.Unmarshal(b, &a) != nil || a.Tokens == nil || a.Tokens.AccessToken == "" {
		return login{}, false
	}
	mode := strings.ToLower(strings.TrimSpace(a.AuthMode))
	switch {
	case strings.HasPrefix(mode, "chatgpt"):
	case mode == "" && a.OpenAIAPIKey == "":
		// Older files omit auth_mode; tokens without an API key mean ChatGPT.
	default:
		return login{}, false
	}
	claims := parseJWTClaims(a.Tokens.IDToken)
	user := claims.Email
	if user == "" {
		user = "ChatGPT"
	}
	return login{
		Account:   Account{Agent: "codex", User: user, Plan: claims.Auth.Plan},
		token:     a.Tokens.AccessToken,
		accountID: a.Tokens.AccountID,
	}, true
}

// jwtClaims are the few claims dial shows. The token is decoded, not verified:
// it only supplies display text.
type jwtClaims struct {
	Email string `json:"email"`
	Exp   int64  `json:"exp"` // expiry, unix seconds; 0 when absent
	Auth  struct {
		Plan string `json:"chatgpt_plan_type"`
	} `json:"https://api.openai.com/auth"`
}

// parseJWTClaims decodes the payload of a JWT. A malformed token yields zero claims.
func parseJWTClaims(tok string) jwtClaims {
	var c jwtClaims
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return c
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return c
	}
	json.Unmarshal(payload, &c)
	return c
}

// codexBuiltin is the built-in Codex model list. Refreshing it never asks the
// network: the built-in list is what Codex is known to serve.
func codexBuiltin() []catalog.Model {
	return slices.Clone(catalog.Builtin("codex"))
}

// ---- Copilot ---------------------------------------------------------------

type copilotEntry struct {
	User  string `json:"user"`
	OAuth string `json:"oauth_token"`
}

// copilotDir is $XDG_CONFIG_HOME/github-copilot, defaulting to ~/.config.
func copilotDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "github-copilot")
}

// copilotLogin reads the first github.com entry with a token from apps.json,
// then hosts.json.
func copilotLogin() (login, bool) {
	for _, name := range []string{"apps.json", "hosts.json"} {
		b, err := os.ReadFile(filepath.Join(copilotDir(), name))
		if err != nil {
			continue
		}
		var entries map[string]copilotEntry
		if json.Unmarshal(b, &entries) != nil {
			continue
		}
		keys := make([]string, 0, len(entries))
		for k := range entries {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			e := entries[k]
			if !strings.HasPrefix(k, "github.com") || e.OAuth == "" {
				continue
			}
			user := e.User
			if user == "" {
				user = "GitHub"
			}
			return login{Account: Account{Agent: "copilot", User: user}, token: e.OAuth}, true
		}
	}
	return login{}, false
}

// copilotBuiltin is the built-in Copilot list, without the "auto" pseudo-model.
func copilotBuiltin() []catalog.Model {
	var out []catalog.Model
	for _, m := range catalog.Builtin("copilot") {
		if m.ID != "auto" {
			out = append(out, m)
		}
	}
	return out
}

// ---- Claude Code (detection only) ------------------------------------------

// claudeSignedIn reports whether Claude Code holds a login. It checks only
// existence: the credential file's contents are never read.
func claudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	if _, err := os.Stat(filepath.Join(dir, ".credentials.json")); err == nil {
		return true
	}
	return claudeKeychainHas()
}

// claudeKeychainHas would report a Claude Code item in the macOS keychain.
// Not implemented yet: it always reports false, and it never reads an item's
// contents or triggers an authorization prompt.
func claudeKeychainHas() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	return false
}

// ---- helpers ---------------------------------------------------------------

func modelIDs(ms []catalog.Model) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}
