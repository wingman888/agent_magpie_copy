package provider

// Sign-in providers. An agent the user already logged into (Codex, Copilot)
// offers its subscription's models to every other agent through dial. dial
// keeps no copy of those credentials: each time it needs them it reads the
// agent's own file. The only thing dial stores is which of those models the
// user picked, in providers.json.
//
// Claude Code is detected but never offered: dial only checks that its
// credentials file exists and never reads what is inside.

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

// CodexBase is the ChatGPT backend Codex speaks to. Tests point it at a fake server.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// CopilotTokenURL exchanges a GitHub token for a short-lived Copilot session.
// CopilotBase is the API address used when the session names none.
// Both are variables so tests can aim them at a local server.
var (
	CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
	CopilotBase     = "https://api.githubcopilot.com"
)

// claudeID is the id Claude Code would have. It is never a provider.
const claudeID = "claude"

// codexDefaultModel is listed for Codex even before the user picks anything.
const codexDefaultModel = "gpt-5.5"

// signInIDs are the sign-in providers, in display order.
var signInIDs = []string{"codex", "copilot"}

var (
	errCodexAPIKey    = errors.New("codex: api key mode")
	errNotSignedIn    = errors.New("not signed in")
	errClaudeReserved = errors.New("Claude Code 不作为 Provider 提供：它的登录只供 Claude Code 自己使用")
)

// Account is who is signed in behind a sign-in provider.
type Account struct {
	User   string `json:"user"`           // email (Codex) or GitHub login (Copilot)
	Plan   string `json:"plan,omitempty"` // subscription plan, when the login names one
	Source string `json:"source"`         // the agent whose login this is
}

// IsAccount reports whether an id names a sign-in provider (codex, copilot).
func IsAccount(id string) bool {
	for _, x := range signInIDs {
		if x == id {
			return true
		}
	}
	return false
}

// Login is what dial knows about one agent's sign-in, for the CLI and API.
type Login struct {
	Agent    string `json:"agent"`
	Name     string `json:"name"`
	Signed   bool   `json:"signed"`   // signed in and usable
	Provided bool   `json:"provided"` // offered to agents as a provider
	User     string `json:"user,omitempty"`
	Plan     string `json:"plan,omitempty"`
	Note     string `json:"note,omitempty"` // why it is missing, or not offered
}

// Logins reports the sign-in state of every agent dial watches for.
func Logins() []Login {
	var out []Login
	for _, id := range signInIDs {
		acct, note, ok := signIn(id)
		out = append(out, Login{
			Agent: id, Name: signInName(id), Signed: ok, Provided: ok,
			User: acct.User, Plan: acct.Plan, Note: note,
		})
	}
	out = append(out, claudeLogin())
	return out
}

// claudeNote is shown wherever Claude Code is signed in but not offered.
const claudeNote = "Claude Code 已登录但不提供：它的登录凭据只供 Claude Code 自己使用，不能作为 Provider 对外提供。dial 只检测登录是否存在，从不读取凭据内容。"

// claudeLogin only checks that Claude Code's credentials file exists.
func claudeLogin() Login {
	l := Login{Agent: claudeID, Name: "Claude Code"}
	if claudeSignedIn() {
		l.Signed = true
		l.Note = claudeNote
	} else {
		l.Note = "未登录 Claude Code"
	}
	return l
}

// Notice tells the user about an agent login that dial sees but does not offer.
type Notice struct {
	Agent string `json:"agent"`
	Name  string `json:"name"`
	Text  string `json:"text"`
}

// Notices lists the signed-in logins that are not offered as providers, with
// the reason. Today that is Claude Code, and only when it is signed in.
func Notices() []Notice {
	var out []Notice
	for _, l := range Logins() {
		if l.Signed && !l.Provided {
			out = append(out, Notice{Agent: l.Agent, Name: l.Name, Text: l.Note})
		}
	}
	return out
}

// SourceName is the display name of the agent behind a sign-in.
func (a Account) SourceName() string { return signInName(a.Source) }

func signInName(id string) string {
	switch id {
	case "codex":
		return "Codex"
	case "copilot":
		return "GitHub Copilot"
	}
	return id
}

func signInIcon(id string) string {
	switch id {
	case "codex":
		return "codex-color"
	case "copilot":
		return "githubcopilot"
	}
	return ""
}

// signIn reads one agent's credentials fresh. note explains a missing login.
func signIn(id string) (acct Account, note string, ok bool) {
	switch id {
	case "codex":
		c, err := readCodex()
		if err == nil {
			return c.Account, "", true
		}
		if errors.Is(err, errCodexAPIKey) {
			return Account{}, "Codex 是 API Key 模式，不是 ChatGPT 账号，因此不作为账号提供", false
		}
		return Account{}, "未登录 Codex：运行 `codex login`", false
	case "copilot":
		app, err := readCopilot()
		if err == nil {
			return Account{User: app.User, Source: "copilot"}, "", true
		}
		return Account{}, "未登录 Copilot：在编辑器或 Copilot CLI 中登录 GitHub", false
	}
	return Account{}, "", false
}

// accountProviders builds the sign-in providers that are currently signed in.
// Only the user's model picks come from the config file.
func accountProviders(f file) []Provider {
	var out []Provider
	for _, id := range signInIDs {
		acct, _, ok := signIn(id)
		if !ok {
			continue
		}
		a := acct
		out = append(out, Provider{
			ID: id, Name: signInName(id), Icon: signInIcon(id),
			Models: cleanList(f.SignIns[id]), Account: &a,
		})
	}
	return out
}

// saveSignIn stores only the model picks of a sign-in provider.
func saveSignIn(p Provider) error {
	if p.Chat != "" || p.Responses != "" || p.Anthropic != "" || p.Key != "" {
		return fmt.Errorf("%s signs in through its own agent: it takes no URL or key, only the models you pick", signInName(p.ID))
	}
	f := load()
	if models := cleanList(p.Models); len(models) > 0 {
		if f.SignIns == nil {
			f.SignIns = map[string][]string{}
		}
		f.SignIns[p.ID] = models
	} else {
		delete(f.SignIns, p.ID)
	}
	return store(f)
}

// clearSignIn forgets the model picks of a sign-in provider. The login itself
// is untouched, so the provider stays listed while the agent is signed in.
func clearSignIn(id string) error {
	f := load()
	if _, ok := f.SignIns[id]; !ok {
		return nil
	}
	delete(f.SignIns, id)
	return store(f)
}

// accountModels is what a sign-in provider offers before any refresh.
func accountModels(id string) []catalog.Model {
	switch id {
	case "codex":
		var ms []catalog.Model
		for _, m := range catalog.Codex() {
			m.Provider = "codex"
			ms = append(ms, m)
		}
		for _, m := range ms {
			if m.ID == codexDefaultModel {
				return ms
			}
		}
		var def []catalog.Model
		for _, m := range catalog.Builtin("codex") {
			if m.ID == codexDefaultModel {
				m.Provider = "codex"
				def = append(def, m)
			}
		}
		return append(def, ms...)
	case "copilot":
		var out []catalog.Model
		for _, m := range catalog.Builtin("copilot") {
			if m.ID == "auto" || strings.Contains(m.ID, "embed") {
				continue
			}
			m.Provider = "copilot"
			out = append(out, m)
		}
		return out
	}
	return nil
}

// ---- Codex ------------------------------------------------------------------

type codexAuth struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

// codexCred is Codex's login, as read from its file just now.
type codexCred struct {
	Account
	AccessToken  string
	RefreshToken string
	AccountID    string
	Expires      time.Time // from the access token's exp claim; zero if unknown
}

func codexPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// readCodex reads ~/.codex/auth.json. Only a ChatGPT login counts.
func readCodex() (codexCred, error) {
	b, err := os.ReadFile(codexPath())
	if err != nil {
		return codexCred{}, errNotSignedIn
	}
	var f codexAuth
	if err := json.Unmarshal(b, &f); err != nil {
		return codexCred{}, errNotSignedIn
	}
	if f.AuthMode == "apikey" {
		return codexCred{}, errCodexAPIKey
	}
	if f.AuthMode != "chatgpt" {
		return codexCred{}, errNotSignedIn
	}
	c := codexCred{
		AccessToken:  f.Tokens.AccessToken,
		RefreshToken: f.Tokens.RefreshToken,
		AccountID:    f.Tokens.AccountID,
	}
	if c.AccessToken == "" && c.RefreshToken == "" {
		return codexCred{}, errNotSignedIn
	}
	c.Source = "codex"
	if claims, err := jwtClaims(f.Tokens.IDToken); err == nil {
		c.User, _ = claims["email"].(string)
		if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
			c.Plan, _ = auth["chatgpt_plan_type"].(string)
		}
	}
	if claims, err := jwtClaims(f.Tokens.AccessToken); err == nil {
		if exp, ok := claims["exp"].(float64); ok {
			c.Expires = time.Unix(int64(exp), 0)
		}
	}
	return c, nil
}

// jwtClaims decodes the payload of a JWT. The signature is not checked; dial
// only reads the claims it displays.
func jwtClaims(tok string) (map[string]any, error) {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return nil, errors.New("not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ---- Copilot ----------------------------------------------------------------

type copilotApp struct {
	User       string `json:"user"`
	OAuthToken string `json:"oauth_token"`
}

func copilotPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "github-copilot", "apps.json")
}

// readCopilot reads the GitHub login from the Copilot apps file.
func readCopilot() (copilotApp, error) {
	b, err := os.ReadFile(copilotPath())
	if err != nil {
		return copilotApp{}, errNotSignedIn
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return copilotApp{}, errNotSignedIn
	}
	var keys []string
	for k := range raw {
		if strings.HasPrefix(k, "github.com") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		var app copilotApp
		if json.Unmarshal(raw[k], &app) == nil && app.OAuthToken != "" {
			return app, nil
		}
	}
	return copilotApp{}, errNotSignedIn
}

// CopilotModel is the part of one row of Copilot's /models answer dial reads.
type CopilotModel struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Internal           bool   `json:"internal,omitempty"`
	ModelPickerEnabled *bool  `json:"model_picker_enabled,omitempty"`
	Policy             *struct {
		State string `json:"state"`
	} `json:"policy,omitempty"`
	Capabilities struct {
		Type string `json:"type"`
	} `json:"capabilities"`
}

// FilterCopilot keeps the models this account can really chat with: chat
// type only, no internal models, no embeddings, no "auto", and only those the
// account has enabled.
func FilterCopilot(rows []CopilotModel) []catalog.Model {
	var out []catalog.Model
	for _, r := range rows {
		id := strings.TrimSpace(r.ID)
		switch {
		case id == "" || id == "auto":
			continue
		case r.Internal:
			continue
		case strings.Contains(id, "embed"):
			continue
		case r.Capabilities.Type != "chat":
			continue
		case r.ModelPickerEnabled != nil && !*r.ModelPickerEnabled:
			continue
		case r.Policy != nil && r.Policy.State == "disabled":
			continue
		}
		name := r.Name
		if name == "" {
			name = id
		}
		out = append(out, catalog.Model{ID: id, Name: name, Provider: "copilot"})
	}
	return out
}

// RefreshCopilot asks Copilot which models this account can use, keeps the
// filtered list, and remembers it for the picker.
func RefreshCopilot(ctx context.Context) ([]catalog.Model, error) {
	app, err := readCopilot()
	if err != nil {
		return nil, errors.New("未登录 Copilot：在编辑器或 Copilot CLI 中登录 GitHub")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	session, base, err := copilotSession(ctx, app.OAuthToken)
	if err != nil {
		return nil, err
	}
	rows, err := copilotList(ctx, base, session)
	if err != nil {
		return nil, err
	}
	ms := FilterCopilot(rows)
	if len(ms) == 0 {
		return nil, errors.New("Copilot 账号没有可用的聊天模型")
	}
	if err := catalog.SaveLive("copilot", base, ms); err != nil {
		return nil, err
	}
	return ms, nil
}

var errReloginCopilot = errors.New("请重新登录 Copilot")

// copilotSession trades the GitHub token for a session token and the API address.
func copilotSession(ctx context.Context, oauth string) (token, base string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return "", "", errReloginCopilot
	default:
		return "", "", fmt.Errorf("Copilot 会话: %s", res.Status)
	}
	var v struct {
		Token     string `json:"token"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(b, &v); err != nil || v.Token == "" {
		return "", "", errors.New("Copilot 会话返回异常")
	}
	base = strings.TrimRight(v.Endpoints.API, "/")
	if base == "" {
		base = CopilotBase
	}
	return v.Token, base, nil
}

// copilotList fetches the raw model rows for the session.
func copilotList(ctx context.Context, base, token string) ([]CopilotModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, errReloginCopilot
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Copilot 模型列表: %s", res.Status)
	}
	var v struct {
		Data []CopilotModel `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, errors.New("Copilot 模型列表返回异常")
	}
	return v.Data, nil
}

// ---- Claude Code ------------------------------------------------------------

func claudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// claudeSignedIn only stats the credentials file; its contents are never read.
func claudeSignedIn() bool {
	_, err := os.Stat(filepath.Join(claudeDir(), ".credentials.json"))
	return err == nil
}
