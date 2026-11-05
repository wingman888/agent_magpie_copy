package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Account providers are agents the user already signed in to. Their models
// are served through the gateway with the agent's own login, so nobody has
// to create an API key. dial reads the agent's credential file on every use
// and keeps none of it: the config file only records which models the user
// picked for the account.

const (
	CodexID   = "codex"
	CopilotID = "copilot"
	ClaudeID  = "claude"
)

// accountIDs are the providers backed by an agent login, in display order.
var accountIDs = []string{CodexID, CopilotID}

// claudeNote is shown instead of a provider for a signed-in Claude Code.
const claudeNote = "Claude Code 已登录但不提供：它的订阅令牌只供 Claude Code 自己使用，" +
	"不能转交给其他 Agent 经由 dial 使用。"

var errNotSignedIn = errors.New("not signed in")

func isAccountID(id string) bool { return id == CodexID || id == CopilotID }

// isReservedID is true for ids dial never treats as a user's own vendor.
func isReservedID(id string) bool { return isAccountID(id) || id == ClaudeID }

// IsAccount reports whether the provider is an agent's own login.
func (p Provider) IsAccount() bool { return p.Account != nil }

// Account describes the login behind an account provider.
type Account struct {
	User   string `json:"user"`           // e-mail (Codex) or GitHub login (Copilot)
	Plan   string `json:"plan,omitempty"` // subscription, when the login says so
	Source string `json:"source"`         // the agent the login belongs to
}

// accountProvider builds the provider for a signed-in account, or nil when
// the agent is not signed in (or is signed in some other way dial cannot use).
func accountProvider(id string) *Provider {
	switch id {
	case CodexID:
		return codexProvider()
	case CopilotID:
		return copilotProvider()
	}
	return nil
}

// storedModels returns the model picks saved for an account provider.
func storedModels(f file, id string) []string {
	for _, p := range f.Providers {
		if p.ID == id {
			return cleanList(p.Models)
		}
	}
	return nil
}

// saveAccount writes the only thing dial keeps for an account: its picks.
func saveAccount(id string, models []string) error {
	f := load()
	entry := Provider{ID: id, Models: cleanList(models)}
	for i := range f.Providers {
		if f.Providers[i].ID == id {
			f.Providers[i] = entry
			return store(f)
		}
	}
	f.Providers = append(f.Providers, entry)
	return store(f)
}

// homeJoin is a path under the user's home directory.
func homeJoin(parts ...string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(append([]string{home}, parts...)...)
}

// jwtClaims reads the payload of a JWT without checking its signature: dial
// only wants the e-mail and expiry the agent itself wrote there.
func jwtClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return map[string]any{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return map[string]any{}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out map[string]any
	if dec.Decode(&out) != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// claudeSignedIn only checks that Claude Code's credential file exists. Its
// contents are never read.
func claudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = homeJoin(".claude")
	}
	_, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	return err == nil
}

// Notes lists things about the agents on this machine that dial deliberately
// does not offer as providers, with the reason.
func Notes() []string {
	var out []string
	if claudeSignedIn() {
		out = append(out, claudeNote)
	}
	return out
}
