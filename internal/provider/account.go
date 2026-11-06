package provider

// Account providers are logins the user already made in an agent: the
// ChatGPT account behind `codex login`, and the GitHub account behind Copilot.
// Their credentials stay in the agent's own files and are read each time they
// are needed; dial never copies them. The only thing dial stores for them is
// which models the user picked (see Save and Delete).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

const (
	// Codex is the provider id for the ChatGPT login of Codex.
	Codex = "codex"
	// Copilot is the provider id for the GitHub login behind Copilot.
	Copilot = "copilot"
	// Claude is the login dial deliberately never offers as a provider.
	Claude = "claude"
)

// CodexBase is the ChatGPT Codex backend. Requests for codex/<model> go to
// CodexBase + "/responses". Tests point it at a local fake server.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// CopilotTokenURL exchanges a GitHub token for a short-lived Copilot session.
// Tests point it at a local fake server.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

// ClaudeNote is what dial says about a signed-in Claude Code.
const ClaudeNote = "Claude Code is signed in but not offered (已登录但不提供): dial does not read its credentials, and only Codex and Copilot accounts are offered as providers."

const claudeRefusal = "Claude Code is not offered as a provider: dial does not read its login"

// errCopilotSignIn is returned when GitHub or Copilot rejects the stored login.
var errCopilotSignIn = errors.New("Copilot rejected the stored login — 请重新登录 Copilot (sign in to GitHub Copilot again)")

// AccountInfo is what dial knows about a signed-in agent account.
type AccountInfo struct {
	User    string    // e-mail for Codex, GitHub login for Copilot
	Plan    string    // subscription plan, when the login states one
	Source  string    // the agent the login belongs to
	Host    string    // where requests go, for display
	Expires time.Time // when the access token lapses, if the login states it
}

// IsAccount is true for the provider ids that come from agent logins.
func IsAccount(id string) bool { return id == Codex || id == Copilot }

// Notes lists lines to show about logins dial sees but does not offer.
func Notes() []string {
	if ClaudeSignedIn() {
		return []string{ClaudeNote}
	}
	return nil
}

// ClaudeSignedIn reports whether Claude Code has a login on disk. It checks
// that the credentials file exists; the contents are never read.
func ClaudeSignedIn() bool {
	_, err := os.Stat(claudeCredentials())
	return err == nil
}

func claudeCredentials() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(userHome(), ".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}

func userHome() string {
	h, _ := os.UserHomeDir()
	return h
}

// signedInAccounts lists the account providers whose agent is signed in now.
func signedInAccounts() []Provider {
	var out []Provider
	if a, ok := codexAccount(); ok {
		out = append(out, Provider{ID: Codex, Name: "Codex", Icon: "openai", Account: a})
	}
	if a, ok := copilotAccount(); ok {
		out = append(out, Provider{ID: Copilot, Name: "GitHub Copilot", Icon: "githubcopilot", Account: a})
	}
	return out
}

// signedIn reports whether an account provider's agent is signed in now.
func signedIn(id string) bool {
	for _, a := range signedInAccounts() {
		if a.ID == id {
			return true
		}
	}
	return false
}

// ---- Codex ----------------------------------------------------------------

type codexAuth struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

func codexAuthPath() string { return filepath.Join(userHome(), ".codex", "auth.json") }

// codexAccount reads the ChatGPT login of Codex. An API-key login is not an
// account and is ignored.
func codexAccount() (*AccountInfo, bool) {
	b, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return nil, false
	}
	var f codexAuth
	if json.Unmarshal(b, &f) != nil || f.AuthMode != "chatgpt" || f.Tokens.AccessToken == "" {
		return nil, false
	}
	idClaims := jwtClaims(f.Tokens.IDToken)
	user, _ := idClaims["email"].(string)
	if user == "" {
		user = f.Tokens.AccountID
	}
	plan := ""
	if auth, ok := idClaims["https://api.openai.com/auth"].(map[string]any); ok {
		plan, _ = auth["chatgpt_plan_type"].(string)
	}
	a := &AccountInfo{User: user, Plan: plan, Source: "Codex", Host: HostOf(CodexBase)}
	if exp, ok := jwtClaims(f.Tokens.AccessToken)["exp"].(float64); ok {
		a.Expires = time.Unix(int64(exp), 0)
	}
	return a, true
}

// jwtClaims reads the payload of a JWT. The signature is not checked: dial
// only wants the e-mail, plan and expiry the token states about itself.
func jwtClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return map[string]any{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return map[string]any{}
	}
	return m
}

// ---- Copilot --------------------------------------------------------------

type copilotLogin struct {
	User  string `json:"user"`
	OAuth string `json:"oauth_token"`
}

func copilotAppsPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(userHome(), ".config")
	}
	return filepath.Join(base, "github-copilot", "apps.json")
}

// readCopilotLogin finds the GitHub login the Copilot clients keep.
func readCopilotLogin() (copilotLogin, bool) {
	b, err := os.ReadFile(copilotAppsPath())
	if err != nil {
		return copilotLogin{}, false
	}
	var apps map[string]json.RawMessage
	if json.Unmarshal(b, &apps) != nil {
		return copilotLogin{}, false
	}
	var keys []string
	for k := range apps {
		if strings.HasPrefix(k, "github.com") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		var l copilotLogin
		if json.Unmarshal(apps[k], &l) == nil && l.User != "" && l.OAuth != "" {
			return l, true
		}
	}
	return copilotLogin{}, false
}

func copilotAccount() (*AccountInfo, bool) {
	l, ok := readCopilotLogin()
	if !ok {
		return nil, false
	}
	return &AccountInfo{User: l.User, Source: "GitHub Copilot", Host: "api.githubcopilot.com"}, true
}

// accountBuiltin is the built-in list of an account provider, without "auto".
func accountBuiltin(id string) []catalog.Model {
	var out []catalog.Model
	for _, m := range catalog.Builtin(id) {
		if m.ID != "auto" {
			out = append(out, m)
		}
	}
	return out
}

// accountModels is what an account provider can serve: the list saved by the
// last refresh, else the built-in list.
func (p Provider) accountModels() []catalog.Model {
	if live, _, ok := catalog.Live(p.ID); ok {
		return live
	}
	return accountBuiltin(p.ID)
}

// RefreshCopilot asks GitHub which chat models this Copilot account may use,
// saves that list, and returns it.
func RefreshCopilot(ctx context.Context) ([]catalog.Model, error) {
	login, ok := readCopilotLogin()
	if !ok {
		return nil, errors.New("not signed in to GitHub Copilot — sign in from your editor or the copilot CLI")
	}
	session, api, err := copilotSession(ctx, login.OAuth)
	if err != nil {
		return nil, err
	}
	raw, err := copilotModelList(ctx, api, session)
	if err != nil {
		return nil, err
	}
	ms := chatModels(raw)
	if len(ms) == 0 {
		return nil, errors.New("Copilot lists no chat models for this account")
	}
	if err := catalog.SaveLive(Copilot, api, ms); err != nil {
		return nil, err
	}
	return ms, nil
}

// copilotSession trades the GitHub token for a Copilot session token and the
// API base the session returns.
func copilotSession(ctx context.Context, oauth string) (string, string, error) {
	var out struct {
		Token     string `json:"token"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	status, err := getJSON(ctx, CopilotTokenURL, map[string]string{"Authorization": "token " + oauth}, &out)
	if err != nil {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return "", "", errCopilotSignIn
		}
		return "", "", fmt.Errorf("copilot session: %w", err)
	}
	if out.Token == "" {
		return "", "", errCopilotSignIn
	}
	api := out.Endpoints.API
	if api == "" {
		api = "https://api.githubcopilot.com"
	}
	return out.Token, strings.TrimRight(api, "/"), nil
}

// copilotEntry is one model as Copilot's /models lists it.
type copilotEntry struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Picker       bool   `json:"model_picker_enabled"`
	Capabilities struct {
		Type string `json:"type"`
	} `json:"capabilities"`
	Policy struct {
		State string `json:"state"`
	} `json:"policy"`
}

func copilotModelList(ctx context.Context, api, session string) ([]copilotEntry, error) {
	var out struct {
		Data []copilotEntry `json:"data"`
	}
	status, err := getJSON(ctx, api+"/models", map[string]string{
		"Authorization":          "Bearer " + session,
		"Editor-Version":         "vscode/1.99.0",
		"Copilot-Integration-Id": "vscode-chat",
	}, &out)
	if err != nil {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return nil, errCopilotSignIn
		}
		return nil, fmt.Errorf("copilot models: %w", err)
	}
	return out.Data, nil
}

// chatModels keeps the models this account can pick for chat: chat-type,
// shown in the picker, switched on for the account, and not "auto" or internal.
func chatModels(raw []copilotEntry) []catalog.Model {
	var out []catalog.Model
	seen := map[string]bool{}
	for _, m := range raw {
		switch {
		case m.ID == "" || m.ID == "auto" || seen[m.ID]:
		case m.Capabilities.Type != "chat": // embeddings and friends
		case !m.Picker: // internal: not offered to users
		case strings.Contains(m.ID, "internal"):
		case m.Policy.State != "" && m.Policy.State != "enabled": // not switched on for this account
		default:
			seen[m.ID] = true
			name := m.Name
			if name == "" {
				name = m.ID
			}
			out = append(out, catalog.Model{ID: m.ID, Name: name, Provider: Copilot})
		}
	}
	return out
}

// getJSON GETs a URL and decodes the JSON body. It returns the HTTP status
// (0 when no response came back).
func getJSON(ctx context.Context, url string, headers map[string]string, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return res.StatusCode, err
	}
	if res.StatusCode >= 300 {
		return res.StatusCode, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return res.StatusCode, json.Unmarshal(body, out)
}

// saveAccount keeps only the user's picks for an account provider. The login
// itself stays where the agent put it.
func saveAccount(p Provider) error {
	entry := Provider{ID: p.ID, Models: p.Models}
	f := load()
	for i := range f.Providers {
		if f.Providers[i].ID == p.ID {
			f.Providers[i] = entry
			return store(f)
		}
	}
	f.Providers = append(f.Providers, entry)
	return store(f)
}

// MarshalJSON writes an account provider as its id and picks only, so no
// key, name or other field reaches the config file. Other providers are
// written exactly as before.
func (p Provider) MarshalJSON() ([]byte, error) {
	if IsAccount(p.ID) {
		return json.Marshal(struct {
			ID     string   `json:"id"`
			Models []string `json:"models,omitempty"`
		}{p.ID, p.Models})
	}
	type plain Provider
	return json.Marshal(plain(p))
}
