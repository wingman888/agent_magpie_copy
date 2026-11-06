package provider

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Account describes a subscription the user is signed in to inside another
// agent (Codex, Copilot). dial never stores its tokens: every read goes to the
// agent's own credential file, so the agent stays the only source of truth.
type Account struct {
	Agent string `json:"agent"` // "codex" or "copilot"
	User  string `json:"user"`  // display name: email, GitHub login, or a fallback
	Plan  string `json:"plan"`  // subscription tier, "" when unknown
}

// Exclusion is a sign-in dial recognises but deliberately does not offer.
type Exclusion struct {
	Agent string `json:"agent"` // "claude"
	Why   string `json:"why"`   // fixed, credential-free explanation
}

// claudeWhy is shown wherever a Claude Code sign-in is reported as not offered.
const claudeWhy = "Anthropic 条款只允许 Claude 订阅用于 Claude Code 本身，dial 不会把 Claude Code 的登录提供给其他 Agent 或网关。请改用 Anthropic API Key 作为 Provider。"

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// ---- Codex ----------------------------------------------------------------

type codexAuthFile struct {
	AuthMode     string  `json:"auth_mode"`
	OpenAIAPIKey *string `json:"OPENAI_API_KEY"`
	Tokens       struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

// codexCred is what dial needs from ~/.codex/auth.json. The tokens are only
// held in memory for the duration of a request.
type codexCred struct {
	User        string
	Plan        string
	AccessToken string
	AccountID   string
}

// readCodex reports a ChatGPT-mode Codex sign-in. API-key mode is not an
// account and yields false.
func readCodex() (codexCred, bool) {
	b, err := os.ReadFile(filepath.Join(homeDir(), ".codex", "auth.json"))
	if err != nil {
		return codexCred{}, false
	}
	var f codexAuthFile
	if json.Unmarshal(b, &f) != nil || !codexModeOK(f) {
		return codexCred{}, false
	}
	access := strings.TrimSpace(f.Tokens.AccessToken)
	if access == "" {
		return codexCred{}, false
	}
	email, plan := codexIdentity(f.Tokens.IDToken)
	if email == "" {
		email = "ChatGPT"
	}
	return codexCred{User: email, Plan: plan, AccessToken: access, AccountID: f.Tokens.AccountID}, true
}

// codexModeOK reports whether an auth file is a ChatGPT sign-in rather than an
// API-key login.
func codexModeOK(f codexAuthFile) bool {
	mode := strings.ToLower(strings.TrimSpace(f.AuthMode))
	switch {
	case mode == "apikey" || mode == "api_key":
		return false
	case mode == "chatgpt" || mode == "chatgptauthtokens":
		return true
	case mode == "":
		// Older files omit auth_mode; treat them as ChatGPT unless a key is set.
		return f.OpenAIAPIKey == nil || strings.TrimSpace(*f.OpenAIAPIKey) == ""
	}
	return false
}

// codexIdentity reads the email and plan from the id_token payload. The
// signature is not checked: the token came from the agent's own file.
func codexIdentity(idToken string) (email, plan string) {
	claims := jwtPayload(idToken)
	if claims == nil {
		return "", ""
	}
	email, _ = claims["email"].(string)
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		plan, _ = auth["chatgpt_plan_type"].(string)
	}
	return strings.TrimSpace(email), strings.TrimSpace(plan)
}

// jwtPayload decodes the middle segment of a JWT, or returns nil.
func jwtPayload(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return nil
	}
	seg := strings.TrimRight(parts[1], "=")
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(seg)
		if err != nil {
			return nil
		}
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// ---- Copilot --------------------------------------------------------------

// copilotCred is a GitHub OAuth token from the Copilot editor plugins.
type copilotCred struct {
	User  string
	OAuth string
}

func copilotConfigDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "github-copilot")
	}
	return filepath.Join(homeDir(), ".config", "github-copilot")
}

// readCopilot finds the first github.com entry, apps.json before hosts.json.
func readCopilot() (copilotCred, bool) {
	dir := copilotConfigDir()
	for _, name := range []string{"apps.json", "hosts.json"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var entries map[string]struct {
			User  string `json:"user"`
			OAuth string `json:"oauth_token"`
		}
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
			if !strings.HasPrefix(k, "github.com") || strings.TrimSpace(e.OAuth) == "" {
				continue
			}
			user := strings.TrimSpace(e.User)
			if user == "" {
				user = "GitHub"
			}
			return copilotCred{User: user, OAuth: e.OAuth}, true
		}
	}
	return copilotCred{}, false
}

// ---- Claude Code (detection only) ----------------------------------------

// claudeKeychainPresent reports whether a Claude Code item sits in the macOS
// keychain. It only checks existence and never reads the item. It is a
// variable so it can be replaced; the default finds nothing.
var claudeKeychainPresent = func() bool { return false }

func claudeConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(homeDir(), ".claude")
}

// claudeFileSignedIn checks only that the credential file exists; it is never
// opened.
func claudeFileSignedIn(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	return err == nil && !st.IsDir()
}

// ---- public API -----------------------------------------------------------

// Accounts lists the signed-in agent accounts dial offers as providers.
// Claude Code is never among them; see Excluded.
func Accounts() []Provider {
	var out []Provider
	if c, ok := readCodex(); ok {
		out = append(out, Provider{
			ID:      "codex",
			Name:    "Codex",
			Account: &Account{Agent: "codex", User: c.User, Plan: c.Plan},
		})
	}
	if c, ok := readCopilot(); ok {
		out = append(out, Provider{
			ID:      "copilot",
			Name:    "Copilot",
			Account: &Account{Agent: "copilot", User: c.User},
		})
	}
	return out
}

var excludedCache struct {
	mu  sync.Mutex
	key string
	at  time.Time
	out []Exclusion
}

const excludedTTL = time.Minute

// Excluded lists the sign-ins dial recognises but does not offer, with the
// reason. The answer is cached for about a minute; the cache is keyed on the
// config directory and whether its credential file exists, so a sign-in or
// sign-out shows up at once.
func Excluded() []Exclusion {
	dir := claudeConfigDir()
	file := claudeFileSignedIn(dir)
	key := fmt.Sprintf("%s|%t", dir, file)

	excludedCache.mu.Lock()
	defer excludedCache.mu.Unlock()
	if excludedCache.key == key && time.Since(excludedCache.at) < excludedTTL {
		return append([]Exclusion(nil), excludedCache.out...)
	}
	var out []Exclusion
	if file || claudeKeychainPresent() {
		out = []Exclusion{{Agent: "claude", Why: claudeWhy}}
	}
	excludedCache.key = key
	excludedCache.at = time.Now()
	excludedCache.out = out
	return append([]Exclusion(nil), out...)
}
