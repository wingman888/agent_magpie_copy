package provider

// Accounts: agents the user already signed in to. Their models come with the
// subscription, so dial offers them as providers without asking for a key.
//
// dial never copies a sign-in. Every call reads the agent's own credential
// file, and nothing here is written to dial's providers.json except the
// user's model picks.

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
	codexID   = "codex"
	copilotID = "copilot"
	claudeID  = "claude"
)

// CodexBase is the ChatGPT Codex backend; requests for codex/<model> go to
// CodexBase + "/responses". Tests point it at a local fake server.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// CopilotTokenURL exchanges a GitHub token for a short-lived Copilot session.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

// copilotDefaultBase is the Copilot API host, used until a session names one.
const copilotDefaultBase = "https://api.githubcopilot.com"

// claudeNote is what dial says about a signed-in Claude Code.
const claudeNote = "Claude Code 已登录但不提供：其登录仅供 Claude Code 自身使用，dial 不转发它的令牌。"

// errCopilotSignedOut is the message when GitHub or Copilot refuses the sign-in.
var errCopilotSignedOut = errors.New("请重新登录 Copilot：在 Copilot CLI 或编辑器中重新以 GitHub 账号登录")

// Account says whose subscription a signed-in provider uses.
type Account struct {
	User   string `json:"user"`           // email for Codex, GitHub login for Copilot
	Plan   string `json:"plan,omitempty"` // subscription plan, when the sign-in says
	Source string `json:"source"`         // the agent the sign-in belongs to
}

// Note is a line about something dial sees but does not offer. It carries no
// provider id, so nothing that is not a provider can look like one.
type Note struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

// IsAccount reports whether an id names an agent-account provider.
func IsAccount(id string) bool { return id == codexID || id == copilotID }

// ---- credential readers ----------------------------------------------------

// CodexLogin is the part of ~/.codex/auth.json dial reads each time it needs it.
type CodexLogin struct {
	Email        string
	Plan         string
	AccountID    string
	IDToken      string
	AccessToken  string
	RefreshToken string
	Expires      time.Time // from the access token's exp claim; zero when unknown
	Path         string
}

// CodexAuthPath is Codex's login file.
func CodexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// ReadCodex reads Codex's login now. ok is false unless Codex is signed in
// with a ChatGPT account: API-key mode and a missing file both count as not.
func ReadCodex() (CodexLogin, bool) {
	path := CodexAuthPath()
	b, err := os.ReadFile(path)
	if err != nil {
		return CodexLogin{}, false
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
		return CodexLogin{}, false
	}
	l := CodexLogin{
		AccountID: f.Tokens.AccountID, IDToken: f.Tokens.IDToken,
		AccessToken: f.Tokens.AccessToken, RefreshToken: f.Tokens.RefreshToken, Path: path,
	}
	if c, err := jwtClaims(f.Tokens.IDToken); err == nil {
		l.Email, _ = c["email"].(string)
		if auth, ok := c["https://api.openai.com/auth"].(map[string]any); ok {
			l.Plan, _ = auth["chatgpt_plan_type"].(string)
		}
	}
	if c, err := jwtClaims(f.Tokens.AccessToken); err == nil {
		if exp, ok := c["exp"].(float64); ok {
			l.Expires = time.Unix(int64(exp), 0)
		}
	}
	return l, true
}

// CopilotLogin is the GitHub sign-in Copilot keeps in apps.json.
type CopilotLogin struct {
	User  string // GitHub login
	Token string // GitHub OAuth token; exchanged for a Copilot session per use
	Path  string
}

// CopilotAppsPath is where Copilot keeps its sign-in ($XDG_CONFIG_HOME first).
func CopilotAppsPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "github-copilot", "apps.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "github-copilot", "apps.json")
}

// ReadCopilot reads the GitHub sign-in Copilot keeps, if there is one.
func ReadCopilot() (CopilotLogin, bool) {
	path := CopilotAppsPath()
	b, err := os.ReadFile(path)
	if err != nil {
		return CopilotLogin{}, false
	}
	var apps map[string]json.RawMessage
	if json.Unmarshal(b, &apps) != nil {
		return CopilotLogin{}, false
	}
	keys := make([]string, 0, len(apps))
	for k := range apps {
		if strings.HasPrefix(k, "github.com") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		var v struct {
			User  string `json:"user"`
			OAuth string `json:"oauth_token"`
		}
		if json.Unmarshal(apps[k], &v) == nil && v.OAuth != "" {
			return CopilotLogin{User: v.User, Token: v.OAuth, Path: path}, true
		}
	}
	return CopilotLogin{}, false
}

// ClaudeSignedIn reports whether Claude Code has a login on this machine. It
// only checks that the credential file exists; the contents are never read.
func ClaudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	_, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	return err == nil
}

// Notes lists what dial sees but does not offer. Claude Code appears here
// when it is signed in, so the CLI and the API can say why it is absent.
func Notes() []Note {
	if ClaudeSignedIn() {
		return []Note{{Name: "Claude Code", Note: claudeNote}}
	}
	return nil
}

// jwtClaims decodes the payload of a JWT without verifying it: dial only
// reads the claims it displays or schedules around.
func jwtClaims(tok string) (map[string]any, error) {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return nil, errors.New("not a JWT")
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, err
	}
	var c map[string]any
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return c, nil
}

// ---- providers built from sign-ins -----------------------------------------

// accounts builds the account providers that are signed in right now. saved
// holds the user's stored picks by id; a sign-in that has gone away takes
// its picks with it, since they are ignored unless the sign-in is present.
func accounts(saved map[string]Provider) []Provider {
	var out []Provider
	if l, ok := ReadCodex(); ok {
		user := l.Email
		if user == "" {
			user = "ChatGPT account"
		}
		out = append(out, Provider{
			ID: codexID, Name: "Codex", Icon: "codex-color",
			Responses: CodexBase, Models: saved[codexID].Models,
			Account: &Account{User: user, Plan: l.Plan, Source: "Codex"},
		})
	}
	if l, ok := ReadCopilot(); ok {
		user := l.User
		if user == "" {
			user = "GitHub account"
		}
		out = append(out, Provider{
			ID: copilotID, Name: "GitHub Copilot", Icon: "githubcopilot",
			Chat: copilotDefaultBase, Models: saved[copilotID].Models,
			Account: &Account{User: user, Source: "Copilot CLI"},
		})
	}
	return out
}

// accountModels is what a signed-in account can use when no list was fetched.
func accountModels(id string) []catalog.Model {
	switch id {
	case codexID:
		return catalog.Codex()
	case copilotID:
		return catalog.Builtin("copilot")
	}
	return nil
}

// fetchAccount refreshes an account's model list. Codex keeps its list in its
// own cache, so dial only reads it; Copilot is asked, and the answer is saved.
func (p Provider) fetchAccount(ctx context.Context) ([]catalog.Model, error) {
	switch p.ID {
	case copilotID:
		base, ms, err := RefreshCopilot(ctx)
		if err != nil {
			return nil, err
		}
		return ms, catalog.SaveLive(p.ID, base, ms)
	case codexID:
		return accountModels(codexID), nil
	}
	return nil, fmt.Errorf("%s has no model list to fetch", p.Name)
}

// ---- Copilot ----------------------------------------------------------------

// copilotSession is a short-lived token and the API host it is good for.
type copilotSession struct {
	Token string
	API   string
}

// RefreshCopilot asks Copilot which models this account may use now. It
// returns the session's API base and the filtered list.
func RefreshCopilot(ctx context.Context) (string, []catalog.Model, error) {
	login, ok := ReadCopilot()
	if !ok {
		return "", nil, errors.New("Copilot is not signed in — sign in with GitHub in Copilot CLI or your editor")
	}
	sess, err := copilotSessionFor(ctx, login.Token)
	if err != nil {
		return "", nil, err
	}
	status, body, err := httpGet(ctx, sess.API+"/models", map[string]string{
		"Authorization":          "Bearer " + sess.Token,
		"Copilot-Integration-Id": "vscode-chat",
		"Editor-Version":         "vscode/1.99.0",
	})
	if err != nil {
		return "", nil, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "", nil, errCopilotSignedOut
	}
	if status != http.StatusOK {
		return "", nil, fmt.Errorf("copilot models: %d", status)
	}
	ms, err := FilterCopilot(body)
	if err != nil {
		return "", nil, fmt.Errorf("copilot models: %w", err)
	}
	if len(ms) == 0 {
		return "", nil, errors.New("Copilot lists no chat models for this account")
	}
	return sess.API, ms, nil
}

// copilotSessionFor trades the GitHub token for a Copilot session.
func copilotSessionFor(ctx context.Context, oauth string) (copilotSession, error) {
	status, body, err := httpGet(ctx, CopilotTokenURL, map[string]string{
		"Authorization": "token " + oauth,
		"Accept":        "application/json",
	})
	if err != nil {
		return copilotSession{}, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound {
		return copilotSession{}, errCopilotSignedOut
	}
	if status != http.StatusOK {
		return copilotSession{}, fmt.Errorf("copilot session: %d", status)
	}
	var v struct {
		Token     string `json:"token"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(body, &v) != nil || v.Token == "" {
		return copilotSession{}, errCopilotSignedOut
	}
	api := strings.TrimRight(v.Endpoints.API, "/")
	if api == "" {
		api = copilotDefaultBase
	}
	return copilotSession{Token: v.Token, API: api}, nil
}

// FilterCopilot keeps the models of Copilot's /models answer that belong in
// the picker: chat models the account has enabled, minus the automatic
// router, internal models, and embeddings.
func FilterCopilot(body []byte) ([]catalog.Model, error) {
	var v struct {
		Data []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Picker       *bool  `json:"model_picker_enabled"`
			Capabilities struct {
				Type string `json:"type"`
			} `json:"capabilities"`
			Policy *struct {
				State string `json:"state"`
			} `json:"policy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	var out []catalog.Model
	seen := map[string]bool{}
	for _, r := range v.Data {
		switch {
		case r.ID == "" || r.ID == "auto" || seen[r.ID]:
			continue
		case r.Capabilities.Type != "chat":
			continue // embeddings, completions, and the like
		case r.Picker != nil && !*r.Picker:
			continue // internal: not for the picker
		case r.Policy != nil && r.Policy.State != "enabled":
			continue // the account has not enabled it
		case strings.Contains(r.ID, "embed"):
			continue
		}
		seen[r.ID] = true
		name := r.Name
		if name == "" {
			name = r.ID
		}
		out = append(out, catalog.Model{ID: r.ID, Name: name})
	}
	return out, nil
}

// CopilotHeaders are the request headers a Copilot upstream needs. A turn the
// user typed is billed as a user request; a turn that only returns tool
// results continues the agent's own work and is billed differently. Images
// mark the request as a vision request.
func CopilotHeaders(toolReturn, hasImage bool) map[string]string {
	h := map[string]string{"X-Initiator": "user"}
	if toolReturn {
		h["X-Initiator"] = "agent"
	}
	if hasImage {
		h["Copilot-Vision-Request"] = "true"
	}
	return h
}

// httpGet is a GET with a timeout; it returns the status and the body.
func httpGet(ctx context.Context, url string, headers map[string]string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", "dial")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	return res.StatusCode, b, err
}
