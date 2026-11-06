package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Account providers are the subscriptions a user is already signed in to in
// another agent: Codex (a ChatGPT login) and Copilot (a GitHub login). dial
// reads each agent's own login file at every use, keeps no copy of the
// token, and stores only the models the user picked in its own config.
//
// Claude Code is deliberately not one of them. dial only checks that its
// login file exists, says so in Notices, and never reads what is in it.

// accountIDs lists the account providers, in the order they are listed.
var accountIDs = []string{"codex", "copilot"}

// AccountID reports whether id names an account provider.
func AccountID(id string) bool {
	return id == "codex" || id == "copilot"
}

// IsAccount reports whether p is an account provider.
func (p Provider) IsAccount() bool { return AccountID(p.ID) }

// AccountInfo says whose login an account provider uses.
type AccountInfo struct {
	User   string `json:"user,omitempty"` // e-mail (Codex) or GitHub login (Copilot)
	Plan   string `json:"plan,omitempty"` // subscription plan, when the login says so
	Source string `json:"source"`         // the agent the login belongs to
}

// Account reports the login behind an account provider; false when the
// provider is not an account, or its agent is not signed in.
func (p Provider) Account() (AccountInfo, bool) {
	switch p.ID {
	case "codex":
		return codexLogin()
	case "copilot":
		return copilotLogin()
	}
	return AccountInfo{}, false
}

// accountProvider builds the provider for a signed-in agent. It reads the
// login only to see that it is there; no token leaves this function.
func accountProvider(id string) (Provider, bool) {
	switch id {
	case "codex":
		if _, ok := codexLogin(); !ok {
			return Provider{}, false
		}
		return Provider{
			ID: "codex", Name: "Codex", Icon: "openai", Catalog: "codex",
			Website:   "https://chatgpt.com/codex",
			Responses: CodexBase,
		}, true
	case "copilot":
		if _, ok := copilotLogin(); !ok {
			return Provider{}, false
		}
		return Provider{
			ID: "copilot", Name: "Copilot", Catalog: "copilot",
			Website: "https://github.com/features/copilot",
			// the real API address comes with the session; see Prepare.
			Chat: copilotDefaultAPI,
		}, true
	}
	return Provider{}, false
}

// authState is what Prepare resolves for one upstream call. It lives only
// in the Provider value Prepare returns.
type authState struct {
	headers map[string]string
}

// Prepare resolves an account provider for one upstream call: it reads the
// agent's login now (refreshing a Codex token that is about to expire) and
// points the provider at the endpoint that login uses. Any other provider
// is returned unchanged.
func Prepare(ctx context.Context, p Provider) (Provider, error) {
	switch p.ID {
	case "codex":
		tok, err := codexAccess(ctx)
		if err != nil {
			return p, err
		}
		h := map[string]string{"Authorization": "Bearer " + tok.access}
		if tok.accountID != "" {
			h["chatgpt-account-id"] = tok.accountID
		}
		p.Responses = CodexBase
		p.auth = &authState{headers: h}
	case "copilot":
		s, err := copilotSession(ctx)
		if err != nil {
			return p, err
		}
		p.Chat = s.api
		p.auth = &authState{headers: map[string]string{
			"Authorization":          "Bearer " + s.token,
			"Copilot-Integration-Id": "vscode-chat",
			"Editor-Version":         "vscode/1.99.3",
		}}
	}
	return p, nil
}

// RequestHeaders are the headers one upstream call to p needs: its auth and,
// for Copilot, the turn and vision markers that depend on the body.
func RequestHeaders(p Provider, proto Protocol, body []byte) map[string]string {
	h := AuthHeaders(p, proto)
	if p.ID == "copilot" {
		for k, v := range copilotTurnHeaders(body) {
			h[k] = v
		}
	}
	return h
}

// saveAccount stores the model picks of an account provider and nothing else:
// no key, no name, no login. Saving while signed out is allowed; the picks
// simply wait until the agent is signed in again.
func saveAccount(p Provider) error {
	rec := Provider{ID: p.ID, Models: cleanList(p.Models)}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == p.ID {
			f.Providers[i] = rec
			return store(f)
		}
	}
	f.Providers = append(f.Providers, rec)
	return store(f)
}

// deleteAccount clears the model picks of an account provider. It never
// signs anything out: the provider stays listed while the agent is signed in.
func deleteAccount(id string) error {
	f := load()
	found := false
	for i := range f.Providers {
		if f.Providers[i].ID == id {
			f.Providers[i] = Provider{ID: id}
			found = true
		}
	}
	if found {
		return store(f)
	}
	if _, ok := accountProvider(id); ok {
		return nil // signed in, with nothing picked: nothing to clear
	}
	return fmt.Errorf("no provider %q", id)
}

// Notice is a line about an agent that is signed in but not offered as a provider.
type Notice struct {
	Agent string `json:"agent"`
	Name  string `json:"name"`
	Note  string `json:"note"`
}

// Notices lists the agents that are signed in but deliberately not offered.
func Notices() []Notice {
	var out []Notice
	if claudeSignedIn() {
		out = append(out, Notice{
			Agent: "claude", Name: "Claude Code",
			Note: "已登录但不提供：Claude Code 的订阅登录只供 Claude Code 自己使用，dial 不读取其凭据，也不把它作为 Provider 提供。",
		})
	}
	return out
}

// claudeDir is where Claude Code keeps its settings and login.
func claudeDir() string {
	if x := os.Getenv("CLAUDE_CONFIG_DIR"); x != "" {
		return x
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// claudeSignedIn only checks that the login file exists. Its content is never read.
func claudeSignedIn() bool {
	_, err := os.Stat(filepath.Join(claudeDir(), ".credentials.json"))
	return err == nil
}
