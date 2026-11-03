package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// Account providers are the sign-ins the user already has in another agent:
// a Codex (ChatGPT) login and a GitHub Copilot login. dial never copies the
// credentials. It reads the agent's own files each time it needs them, and
// the only thing dial keeps is which of the account's models to expose.

// Account describes the sign-in behind an account provider. It carries no
// tokens.
type Account struct {
	Agent string `json:"agent"` // "codex" or "copilot"
	User  string `json:"user"`  // who is signed in
	Plan  string `json:"plan"`  // subscription tier; "" when unknown
}

// Exclusion is a sign-in dial recognizes but does not offer as a provider.
type Exclusion struct {
	Agent string `json:"agent"`
	Why   string `json:"why"`
}

// Where the upstreams live. Variables so tests can point them at a stand-in.
var (
	CodexBase       = "https://chatgpt.com/backend-api/codex"
	CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
)

const claudeWhy = "Anthropic's terms allow a Claude subscription to be used only by " +
	"Claude Code itself, not as a provider for other agents. Add an Anthropic API key " +
	"as a provider instead."

// accountIDs are the provider ids account providers use, in list order.
var accountIDs = []string{"codex", "copilot"}

func isAccountID(id string) bool {
	for _, a := range accountIDs {
		if a == id {
			return true
		}
	}
	return false
}

// Origin is what a model's note shows as its source: the signed-in user for
// an account, the provider's name otherwise.
func (p Provider) Origin() string {
	if p.Account != nil && p.Account.User != "" {
		return p.Account.User
	}
	return p.Name
}

func homeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

// ---- reading the agents' sign-ins -----------------------------------------

type codexLogin struct {
	user      string
	plan      string
	token     string
	accountID string
}

type codexAuthFile struct {
	AuthMode string `json:"auth_mode"`
	Tokens   *struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

func codexPath() string { return filepath.Join(homeDir(), ".codex", "auth.json") }

// readCodex reports the ChatGPT sign-in in ~/.codex/auth.json. An API-key
// login, or a file without an access token, is not an account.
func readCodex() (codexLogin, bool) {
	b, err := os.ReadFile(codexPath())
	if err != nil {
		return codexLogin{}, false
	}
	var f codexAuthFile
	if json.Unmarshal(b, &f) != nil || f.Tokens == nil {
		return codexLogin{}, false
	}
	if strings.EqualFold(f.AuthMode, "apikey") || f.Tokens.AccessToken == "" {
		return codexLogin{}, false
	}
	out := codexLogin{user: "ChatGPT", token: f.Tokens.AccessToken, accountID: f.Tokens.AccountID}
	claims := jwtClaims(f.Tokens.IDToken)
	if email, _ := claims["email"].(string); email != "" {
		out.user = email
	}
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		out.plan, _ = auth["chatgpt_plan_type"].(string)
	}
	return out, true
}

// jwtClaims returns a JWT's payload. The signature is not checked: dial only
// reads what the agent wrote, for display.
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

type copilotLogin struct {
	user  string
	oauth string
}

func copilotDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(homeDir(), ".config")
	}
	return filepath.Join(base, "github-copilot")
}

// readCopilot finds the github.com entry in the Copilot CLI/editor files.
func readCopilot() (copilotLogin, bool) {
	for _, name := range []string{"apps.json", "hosts.json"} {
		b, err := os.ReadFile(filepath.Join(copilotDir(), name))
		if err != nil {
			continue
		}
		var entries map[string]json.RawMessage
		if json.Unmarshal(b, &entries) != nil {
			continue
		}
		keys := make([]string, 0, len(entries))
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
			out := copilotLogin{user: e.User, oauth: e.OAuth}
			if out.user == "" {
				out.user = "GitHub"
			}
			return out, true
		}
	}
	return copilotLogin{}, false
}

// claudeSignedIn only checks that Claude Code has a sign-in. Its content is
// never read.
func claudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(homeDir(), ".claude")
	}
	if fi, err := os.Stat(filepath.Join(dir, ".credentials.json")); err == nil && !fi.IsDir() {
		return true
	}
	return claudeKeychainHas()
}

// claudeKeychainHas asks macOS whether a Claude Code item exists. Without -w
// the tool prints attributes only, so the secret is not read and no
// access prompt is raised.
func claudeKeychainHas() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials").Run() == nil
}

// ---- providers --------------------------------------------------------------

// signedIn is the account provider for an agent, when that agent is signed in.
// It has no models and no tokens.
func signedIn(id string) (Provider, bool) {
	switch id {
	case "codex":
		c, ok := readCodex()
		if !ok {
			return Provider{}, false
		}
		return Provider{ID: "codex", Name: "Codex", Icon: "codex-color",
			Account: &Account{Agent: "codex", User: c.user, Plan: c.plan}}, true
	case "copilot":
		c, ok := readCopilot()
		if !ok {
			return Provider{}, false
		}
		return Provider{ID: "copilot", Name: "Copilot",
			Account: &Account{Agent: "copilot", User: c.user}}, true
	}
	return Provider{}, false
}

// Accounts lists the signed-in account providers dial offers. An id the user
// has given a base URL of their own is left to that provider.
func Accounts() []Provider {
	var out []Provider
	for _, p := range All() {
		if p.Account != nil {
			out = append(out, p)
		}
	}
	return out
}

var excluded struct {
	sync.Mutex
	key string
	at  time.Time
	val []Exclusion
}

// Excluded lists the sign-ins dial recognizes but does not offer. The answer
// is kept for about a minute.
func Excluded() []Exclusion {
	key := homeDir() + "\x00" + os.Getenv("CLAUDE_CONFIG_DIR")
	excluded.Lock()
	defer excluded.Unlock()
	if excluded.key == key && time.Since(excluded.at) < time.Minute {
		return append([]Exclusion(nil), excluded.val...)
	}
	var out []Exclusion
	if claudeSignedIn() {
		out = append(out, Exclusion{Agent: "claude", Why: claudeWhy})
	}
	excluded.key, excluded.at, excluded.val = key, time.Now(), out
	return append([]Exclusion(nil), out...)
}

// ---- models -----------------------------------------------------------------

// accountModels is what an account can use: the refreshed list when there is
// one, else the built-in list. Copilot's built-in list never offers "auto".
func (p Provider) accountModels() []catalog.Model {
	var known []catalog.Model
	switch p.ID {
	case "codex":
		known = catalog.Builtin("codex")
	case "copilot":
		for _, m := range catalog.Builtin("copilot") {
			if m.ID != "auto" {
				known = append(known, m)
			}
		}
	}
	if live, _, ok := catalog.Live(p.ID); ok {
		return catalog.Decorate(live, known)
	}
	return known
}

// ---- requests ---------------------------------------------------------------

// Sign sets the headers that prove the request comes from this provider.
// Account providers use the agent's token, read now.
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if p.Account == nil {
		for k, v := range AuthHeaders(p, proto) {
			req.Header.Set(k, v)
		}
		return nil
	}
	switch p.ID {
	case "codex":
		return signCodex(ctx, req)
	case "copilot":
		return signCopilot(ctx, req, body)
	}
	return fmt.Errorf("%s: unknown account provider", p.ID)
}

// Upstream lists the protocols a request is sent to this provider in. For an
// account that is the protocol its backend takes; for a provider with
// addresses it is Speaks.
func (p Provider) Upstream() []Protocol {
	if p.Account == nil {
		return p.Speaks()
	}
	switch p.ID {
	case "codex":
		return []Protocol{Responses}
	case "copilot":
		return []Protocol{Chat}
	}
	return nil
}

// SignInHint tells the user how to get an account provider working again.
func (p Provider) SignInHint() string {
	if p.ID == "codex" {
		return "sign in again with `codex login`"
	}
	return "sign in again in " + p.Name
}

// IsSignedOut reports whether err means an account's sign-in is gone or was
// rejected, so the caller should ask the user to sign in again.
func IsSignedOut(err error) bool {
	return errors.Is(err, ErrSignedOut) || errors.Is(err, ErrCopilotSignedOut)
}

func signCodex(ctx context.Context, req *http.Request) error {
	c, err := codexTokenFresh(ctx)
	if err != nil {
		return err
	}
	u, err := url.Parse(CodexBase + "/responses")
	if err != nil {
		return err
	}
	req.URL = u
	req.Host = u.Host
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("chatgpt-account-id", c.accountID)
	return nil
}

// codexDropped are the sampling and limit parameters the ChatGPT backend
// refuses. Reasoning settings are kept.
var codexDropped = []string{"temperature", "top_p", "max_output_tokens", "max_tokens",
	"max_completion_tokens", "presence_penalty", "frequency_penalty", "seed"}

// Prepare adjusts a request body for the backend it is going to. Ordinary
// providers get the body back unchanged.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account == nil || p.ID != "codex" {
		return body
	}
	return prepareCodex(body)
}

// prepareCodex makes a Responses body acceptable to the ChatGPT backend: it
// must stream, must not be stored, and takes input as a list of items.
func prepareCodex(body []byte) []byte {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	m["stream"] = true
	m["store"] = false
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": s}}}}
	}
	for _, k := range codexDropped {
		delete(m, k)
	}
	if mdl, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(mdl, "codex/")
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
