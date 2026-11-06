package provider

// Accounts are logins the agents already hold: a ChatGPT subscription signed
// in through the Codex CLI, a GitHub Copilot login made in an editor. dial
// never asks for them and never copies them. Each use reads the agent's own
// credential file, so a logout in the agent removes the provider at once and
// a refreshed token is picked up without any action here.

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
)

// CodexBase is the ChatGPT backend the Codex login talks to. A variable so
// tests can point it at a stand-in server.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// CopilotTokenURL exchanges a GitHub OAuth token for a short-lived Copilot
// session token. A variable so tests can point it at a stand-in server.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

// Account describes the login behind an account provider. It carries no
// credentials.
type Account struct {
	Agent string `json:"agent"`          // "codex" or "copilot"
	User  string `json:"user"`           // who is signed in, for display
	Plan  string `json:"plan,omitempty"` // subscription tier, when the login says
}

// Exclusion is a login dial recognises but does not offer as a provider.
type Exclusion struct {
	Agent string `json:"agent"` // "claude"
	Why   string `json:"why"`
}

const claudeExclusionWhy = "Anthropic's terms only allow a Claude subscription to be used by Claude Code itself, " +
	"so dial does not offer it. Add an Anthropic API key as a provider instead."

// accountAgents are the provider ids a signed-in agent login is offered under.
var accountAgents = map[string]bool{"codex": true, "copilot": true}

// Accounts lists the signed-in agent logins that dial offers as providers.
// A user's own provider with the same id (one that has an address) wins, and
// the account version is left out.
func Accounts() []Provider { return accountsIn(load()) }

// accountsIn is Accounts for a configuration already read. Each account
// carries the model choice stored for it, if any.
func accountsIn(f file) []Provider {
	own := f.Providers
	var out []Provider
	if a, ok := readCodex(); ok && !hasOwnProvider(own, "codex") {
		email, plan := codexClaims(a.Tokens.IDToken)
		if email == "" {
			email = "ChatGPT"
		}
		out = append(out, Provider{
			ID: "codex", Name: "Codex", Icon: "openai",
			Models:  selectionOf(own, "codex"),
			Account: &Account{Agent: "codex", User: email, Plan: plan},
		})
	}
	if e, ok := readCopilot(); ok && !hasOwnProvider(own, "copilot") {
		user := e.User
		if user == "" {
			user = "GitHub"
		}
		out = append(out, Provider{
			ID: "copilot", Name: "Copilot",
			Models:  selectionOf(own, "copilot"),
			Account: &Account{Agent: "copilot", User: user},
		})
	}
	return out
}

// isSelection reports whether a stored entry is only the model choice for an
// agent login: an account id with no address and no account of its own.
func isSelection(p Provider) bool {
	return accountAgents[p.ID] && p.Account == nil && p.Chat == "" && p.Responses == "" && p.Anthropic == ""
}

func selectionOf(ps []Provider, id string) []string {
	for _, p := range ps {
		if p.ID == id && isSelection(p) {
			return cleanList(p.Models)
		}
	}
	return nil
}

func hasSelection(ps []Provider, id string) bool {
	for _, p := range ps {
		if p.ID == id && isSelection(p) {
			return true
		}
	}
	return false
}

// accountOpen reports whether id names a signed-in login that is offered
// (no user provider takes its place), so writes to it only touch the choice.
func accountOpen(f file, id string) bool {
	if !accountAgents[id] {
		return false
	}
	for _, a := range accountsIn(f) {
		if a.ID == id {
			return true
		}
	}
	return false
}

// selectionRecord is the stored form of a model choice for an agent login.
// It has no address, key or name, only the id and the chosen models.
type selectionRecord struct {
	ID     string   `json:"id"`
	Models []string `json:"models,omitempty"`
}

// accountProtocols are the wire APIs a login is served on: Responses for the
// Codex login (the ChatGPT backend), Chat Completions for Copilot.
func accountProtocols(agent string) []Protocol {
	switch agent {
	case "codex":
		return []Protocol{Responses}
	case "copilot":
		return []Protocol{Chat}
	}
	return nil
}

// AccountBase is the base URL signed requests to an account provider go to:
// the ChatGPT backend for Codex, the API endpoint named by the Copilot session
// token for Copilot. Sign points the request at the same place.
func AccountBase(ctx context.Context, p Provider) (string, error) {
	if p.Account == nil {
		return "", fmt.Errorf("%s is not a sign-in", p.Name)
	}
	switch p.Account.Agent {
	case "codex":
		return CodexBase, nil
	case "copilot":
		s, err := copilotSessionFor(ctx)
		if err != nil {
			return "", err
		}
		return s.api, nil
	}
	return "", fmt.Errorf("%s: this login cannot be used to sign requests", p.Name)
}

// saveSelection stores only the chosen models for an agent login.
func saveSelection(f file, id string, models []string) error {
	rec := Provider{ID: id, Models: cleanList(models)}
	for i := range f.Providers {
		if f.Providers[i].ID == id && isSelection(f.Providers[i]) {
			f.Providers[i] = rec
			return store(f)
		}
	}
	f.Providers = append(f.Providers, rec)
	return store(f)
}

// hasOwnProvider reports whether the user configured a provider with this id
// that has its own address. Stored account selections have none and do not
// count.
func hasOwnProvider(ps []Provider, id string) bool {
	for _, p := range ps {
		if p.ID == id && (p.Chat != "" || p.Responses != "" || p.Anthropic != "") {
			return true
		}
	}
	return false
}

// excludedTTL bounds how often the Claude login is looked for again.
const excludedTTL = time.Minute

var excludedCache struct {
	sync.Mutex
	key string
	at  time.Time
	val []Exclusion
}

// Excluded lists logins dial recognises but does not offer. The answer is
// cached for about a minute.
func Excluded() []Exclusion {
	key := claudeDir() + "\x00" + os.Getenv("HOME")
	excludedCache.Lock()
	defer excludedCache.Unlock()
	if excludedCache.key != key || time.Since(excludedCache.at) >= excludedTTL {
		excludedCache.key = key
		excludedCache.at = time.Now()
		excludedCache.val = detectExcluded()
	}
	return append([]Exclusion(nil), excludedCache.val...)
}

func detectExcluded() []Exclusion {
	var out []Exclusion
	if claudeSignedIn() {
		out = append(out, Exclusion{Agent: "claude", Why: claudeExclusionWhy})
	}
	return out
}

// ---- Codex -----------------------------------------------------------------

type codexAuth struct {
	AuthMode  string  `json:"auth_mode"`
	OpenAIKey *string `json:"OPENAI_API_KEY"`
	Tokens    *struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// readCodex reports the Codex login when it is a ChatGPT login (not an API
// key) that holds an access token.
func readCodex() (codexAuth, bool) {
	b, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return codexAuth{}, false
	}
	var a codexAuth
	if json.Unmarshal(b, &a) != nil {
		return codexAuth{}, false
	}
	switch strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(a.AuthMode)) {
	case "chatgpt", "chatgptauthtokens":
	case "":
		// Older files have no mode; an API key in the file means API-key use.
		if a.OpenAIKey != nil && *a.OpenAIKey != "" {
			return codexAuth{}, false
		}
	default:
		return codexAuth{}, false
	}
	if a.Tokens == nil || a.Tokens.AccessToken == "" {
		return codexAuth{}, false
	}
	return a, true
}

// codexClaims reads the email and plan from the id_token payload. The token
// is not verified; dial only displays these values.
func codexClaims(idToken string) (email, plan string) {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", ""
	}
	var c struct {
		Email string `json:"email"`
		Auth  struct {
			Plan string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	json.Unmarshal(payload, &c)
	return c.Email, c.Auth.Plan
}

// signCodex points the request at the ChatGPT backend and attaches the
// login's access token and account id. The token is read fresh from the file
// and refreshed first when it is about to expire.
func signCodex(ctx context.Context, req *http.Request) error {
	a, err := codexToken(ctx)
	if err != nil {
		return err
	}
	u, err := url.Parse(CodexBase + "/responses")
	if err != nil {
		return err
	}
	req.URL = u
	req.Host = u.Host
	req.Header.Set("Authorization", "Bearer "+a.Tokens.AccessToken)
	if a.Tokens.AccountID != "" {
		req.Header.Set("chatgpt-account-id", a.Tokens.AccountID)
	}
	return nil
}

// ---- Copilot ---------------------------------------------------------------

type copilotEntry struct {
	User       string `json:"user"`
	OAuthToken string `json:"oauth_token"`
}

func copilotDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "github-copilot")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "github-copilot")
}

// readCopilot finds the github.com entry in apps.json or hosts.json that
// carries an OAuth token. An entry without one is not a sign-in.
func readCopilot() (copilotEntry, bool) {
	for _, name := range []string{"apps.json", "hosts.json"} {
		b, err := os.ReadFile(filepath.Join(copilotDir(), name))
		if err != nil {
			continue
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !strings.HasPrefix(k, "github.com") {
				continue
			}
			var e copilotEntry
			if json.Unmarshal(m[k], &e) != nil {
				continue
			}
			if e.OAuthToken != "" {
				return e, true
			}
		}
	}
	return copilotEntry{}, false
}

// ---- Claude Code (detection only) ------------------------------------------

func claudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// claudeSignedIn only checks that a login exists. It never reads the content
// of the credential file or of the keychain item.
func claudeSignedIn() bool {
	if _, err := os.Stat(filepath.Join(claudeDir(), ".credentials.json")); err == nil {
		return true
	}
	return claudeKeychainHas()
}

// claudeKeychainHas asks the macOS keychain whether an item exists. Output is
// discarded and no secret is requested, so no authorisation prompt appears.
func claudeKeychainHas() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials")
	return cmd.Run() == nil
}

// ---- Provider methods for account providers --------------------------------

// Sign attaches the credentials the request needs. A plain provider uses its
// key as before. An account provider uses the agent's own token, read now.
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
		return signCopilot(ctx, req, proto, body)
	}
	return fmt.Errorf("%s: this login cannot be used to sign requests", p.Name)
}

// Prepare adjusts the request body to what the backend requires. Plain
// providers take it as is. The Codex backend gets a streamed, unstored
// Responses body (see prepareCodex).
func (p Provider) Prepare(body []byte) []byte {
	if p.Account != nil && p.Account.Agent == "codex" {
		return prepareCodex(body)
	}
	return body
}

// AccountFor resolves a model reference of the form "<account id>/<model>"
// (for example "codex/gpt-5.5") to a signed-in account provider. It reports
// false when the prefix names no signed-in account.
func AccountFor(ref string) (Provider, string, bool) {
	pid, model, ok := strings.Cut(strings.TrimSpace(ref), "/")
	if !ok || model == "" {
		return Provider{}, "", false
	}
	for _, p := range Accounts() {
		if p.ID == pid && p.Ready() {
			return p, model, true
		}
	}
	return Provider{}, "", false
}
