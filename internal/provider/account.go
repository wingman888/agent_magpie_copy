package provider

// Account providers are the models of an agent the user already signed in to
// (Codex with a ChatGPT login, GitHub Copilot). dial never stores their
// credentials: each use reads the agent's own file. dial's providers.json only
// remembers which of their models the user picked.
//
// Claude Code is the one signed-in agent that is deliberately not offered. dial
// only checks that its credentials file exists; it never reads the contents.

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// CodexBase is the ChatGPT backend a signed-in Codex account talks to. Requests
// for codex/<model> go to CodexBase + "/responses". Tests point it at a fake.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// Account is the sign-in behind an account provider.
type Account struct {
	User   string `json:"user"`           // e-mail (Codex) or GitHub login (Copilot)
	Plan   string `json:"plan,omitempty"` // subscription plan, when the sign-in says so
	Source string `json:"source"`         // the agent that holds the sign-in
}

// accountOrder is the order account providers are listed in when they are not
// in the user's file yet.
var accountOrder = []string{"codex", "copilot"}

// accountNames maps the ids of account providers to their display names.
var accountNames = map[string]string{
	"codex":   "Codex",
	"copilot": "GitHub Copilot",
}

// isAccount reports whether id is the id of an account provider.
func isAccount(id string) bool {
	_, ok := accountNames[id]
	return ok
}

// Reserved reports ids that a custom provider may not take: the account
// providers, and Claude, which is never a provider.
func Reserved(id string) bool { return id == "claude" || isAccount(id) }

// signedIn reports the account behind an account provider, when the agent is
// signed in right now.
func signedIn(id string) (Account, bool) {
	switch id {
	case "codex":
		c, ok := readCodex()
		if !ok {
			return Account{}, false
		}
		return c.Account, true
	case "copilot":
		return copilotSignedIn()
	}
	return Account{}, false
}

// accountProvider builds the provider for a signed-in account. It has no base
// URL and no key; models are the user's picks from the file.
func accountProvider(id string, acc Account, models []string) Provider {
	return Provider{ID: id, Name: accountNames[id], Models: models, Account: &acc}
}

// accountModels is the model list of an account provider: the list saved by the
// last refresh when there is one, else the built-in list, minus "auto", which
// is not a model a client can pin.
func accountModels(id string) []catalog.Model {
	src := catalog.Builtin(id)
	if live, _, ok := catalog.Live(id); ok {
		src = catalog.Decorate(live, catalog.Builtin(id))
	}
	var out []catalog.Model
	for _, m := range src {
		if m.ID != "auto" {
			out = append(out, m)
		}
	}
	return out
}

// saveAccount stores only the user's model picks for an account provider.
// Nothing else (no key, no name, no credentials) goes into providers.json.
func saveAccount(id string, models []string) error {
	entry := Provider{ID: id, Models: cleanList(models)}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == id {
			f.Providers[i] = entry
			return store(f)
		}
	}
	f.Providers = append(f.Providers, entry)
	return store(f)
}

// Notice is something about a locally signed-in agent that dial does not offer
// as a provider, with the reason.
type Notice struct {
	Agent string `json:"agent"`
	Text  string `json:"text"`
}

// Notices lists the agents that are signed in but not offered. Today that is
// Claude Code only.
func Notices() []Notice {
	var out []Notice
	if claudeSignedIn() {
		out = append(out, Notice{
			Agent: "Claude Code",
			Text: "signed in, but not offered as a provider: its login belongs to Claude Code, " +
				"so dial neither reads nor forwards it. Add an Anthropic API key instead: dial provider add anthropic <key>",
		})
	}
	return out
}

// ---- Codex -----------------------------------------------------------------

// codexCreds is what dial needs from ~/.codex/auth.json for one request.
type codexCreds struct {
	Account
	AccessToken  string
	RefreshToken string
	IDToken      string
	AccountID    string
	Expires      time.Time // from the access token's exp claim; zero if absent
}

// readCodex reads ~/.codex/auth.json. Only a ChatGPT login counts; an API key
// login is not an account. Nothing is cached: the file is read every time.
func readCodex() (codexCreds, bool) {
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
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
	if json.Unmarshal(b, &f) != nil || f.AuthMode != "chatgpt" || f.Tokens.AccessToken == "" {
		return codexCreds{}, false
	}
	c := codexCreds{
		AccessToken:  f.Tokens.AccessToken,
		RefreshToken: f.Tokens.RefreshToken,
		IDToken:      f.Tokens.IDToken,
		AccountID:    f.Tokens.AccountID,
	}
	idClaims := jwtClaims(c.IDToken)
	c.User, _ = idClaims["email"].(string)
	if c.User == "" {
		c.User = c.AccountID
	}
	if c.User == "" {
		c.User = "ChatGPT account"
	}
	if auth, ok := idClaims["https://api.openai.com/auth"].(map[string]any); ok {
		c.Plan, _ = auth["chatgpt_plan_type"].(string)
	}
	if exp, ok := jwtClaims(c.AccessToken)["exp"].(float64); ok {
		c.Expires = time.Unix(int64(exp), 0)
	}
	c.Source = accountNames["codex"]
	return c, true
}

// jwtClaims returns the payload of a JWT. The signature is not checked: dial
// only reads the claims of its own agent's tokens. A bad token gives nil.
func jwtClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return nil
	}
	seg := strings.TrimRight(parts[1], "=")
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(seg)
		if err != nil {
			return nil
		}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// ---- Claude ----------------------------------------------------------------

// claudeSignedIn reports whether Claude Code has a credentials file. It checks
// existence only; the contents are never opened.
func claudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	_, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	return err == nil
}
