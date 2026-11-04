package provider

// Signed-in agent accounts as providers. dial never stores their credentials:
// each use reads the agent's own files (Codex's auth.json, Copilot's
// github-copilot/apps.json), so logging out in the agent logs the provider out
// too. Claude Code logins are detected only to say they are not offered.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// Account describes the agent login behind an account provider. It holds no
// tokens.
type Account struct {
	Agent string `json:"agent"`          // "codex" or "copilot"
	User  string `json:"user"`           // email or GitHub user name
	Plan  string `json:"plan,omitempty"` // subscription tier, when the agent says
}

// Exclusion is a login dial recognises but does not offer as a provider.
type Exclusion struct {
	Agent string `json:"agent"`
	Why   string `json:"why"`
}

// accountIDs are the provider ids an agent account can take.
var accountIDs = map[string]bool{"codex": true, "copilot": true}

var accountNames = map[string]string{"codex": "Codex", "copilot": "Copilot"}

// claudeWhy is shown in place of a Claude Code account. It states no
// credentials.
const claudeWhy = "Anthropic's terms allow a Claude subscription to be used only by Claude Code itself, " +
	"so dial does not offer it as a provider. Add an Anthropic API key as a provider instead."

// accountByID finds the signed-in account provider with this id.
func accountByID(id string) (Provider, bool) {
	for _, a := range Accounts() {
		if a.ID == id {
			return a, true
		}
	}
	return Provider{}, false
}

// Accounts lists the signed-in agent accounts that dial offers, read from the
// agents' files as they are right now.
func Accounts() []Provider {
	var out []Provider
	if c, ok := readCodex(); ok {
		user, plan := codexAccount(c)
		out = append(out, accountProvider("codex", Account{Agent: "codex", User: user, Plan: plan}))
	}
	if c, ok := readCopilot(); ok {
		out = append(out, accountProvider("copilot", Account{Agent: "copilot", User: c.user}))
	}
	return out
}

func accountProvider(id string, a Account) Provider {
	p := Provider{ID: id, Name: accountNames[id], Account: &a}
	if id == "codex" {
		p.Icon = "openai"
	}
	return p
}

// Excluded lists logins that dial recognises but does not offer. The files
// are checked on every call; the macOS keychain, which is slow to ask, is
// asked at most once a minute. The keychain is only asked for existence and
// its content is never read.
func Excluded() []Exclusion {
	var out []Exclusion
	if claudeSignedIn() {
		out = append(out, Exclusion{Agent: "claude", Why: claudeWhy})
	}
	return out
}

func claudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func claudeSignedIn() bool {
	if _, err := os.Stat(filepath.Join(claudeDir(), ".credentials.json")); err == nil {
		return true
	}
	return claudeKeychain()
}

var claudeKC struct {
	sync.Mutex
	at time.Time
	ok bool
}

// claudeKeychain reports whether Claude Code has a keychain item. Only the
// exit status of `security` is used: no -w flag, so no secret is read and no
// access prompt is raised.
func claudeKeychain() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	claudeKC.Lock()
	defer claudeKC.Unlock()
	if !claudeKC.at.IsZero() && time.Since(claudeKC.at) < time.Minute {
		return claudeKC.ok
	}
	cmd := exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials")
	claudeKC.ok = cmd.Run() == nil
	claudeKC.at = time.Now()
	return claudeKC.ok
}

// builtinModels is the model list of an account before it is refreshed.
// Copilot's "auto" is a router, not a model, so it is never offered.
func builtinModels(agent string) []catalog.Model {
	switch agent {
	case "codex":
		return catalog.Builtin("codex")
	case "copilot":
		var out []catalog.Model
		for _, m := range catalog.Builtin("copilot") {
			if m.ID != "auto" {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// accountModels is what an account provider can serve: the last refreshed
// list if there is one, else the built-in list.
func (p Provider) accountModels() []catalog.Model {
	if live, _, ok := catalog.Live(p.ID); ok {
		return live
	}
	return builtinModels(p.Account.Agent)
}

// refreshAccount re-reads the account's model list and remembers it.
func (p Provider) refreshAccount(ctx context.Context) ([]catalog.Model, error) {
	switch p.Account.Agent {
	case "codex":
		ms := builtinModels("codex")
		return ms, catalog.SaveLive(p.ID, CodexBase, ms)
	case "copilot":
		return p.refreshCopilot(ctx)
	}
	return nil, fmt.Errorf("%s: unknown account", p.ID)
}

// Sign adds what the upstream needs to know who is asking. A plain provider
// gets its key, as before. An account provider gets the agent's token and
// the URL the agent's requests go to.
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if p.Account == nil {
		for k, v := range AuthHeaders(p, proto) {
			req.Header.Set(k, v)
		}
		return nil
	}
	switch p.Account.Agent {
	case "codex":
		return signCodex(ctx, req)
	case "copilot":
		return signCopilot(ctx, req, body)
	}
	return fmt.Errorf("%s: unknown account", p.ID)
}

// Prepare adjusts a request body to what the upstream accepts. A plain
// provider's body is returned as it is.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account == nil {
		return body
	}
	switch p.Account.Agent {
	case "codex":
		return prepareCodex(body)
	case "copilot":
		return stripModelPrefix(body, "copilot/")
	}
	return body
}

// stripModelPrefix removes "<prefix>" from the body's model id.
func stripModelPrefix(body []byte, prefix string) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m == nil {
		return body
	}
	var model string
	if json.Unmarshal(m["model"], &model) != nil || !strings.HasPrefix(model, prefix) {
		return body
	}
	m["model"], _ = json.Marshal(strings.TrimPrefix(model, prefix))
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
