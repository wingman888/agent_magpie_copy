package provider

// Account providers serve the models of a login another agent already holds:
// Codex (a ChatGPT subscription) and Copilot (a GitHub subscription). dial
// never copies those tokens into its own store; every use reads the agent's
// file again, and a Codex token close to expiry is refreshed and written back
// into the same file.
//
// Claude Code's login is only ever checked for existence. It is never offered
// as a provider.

import (
	"bytes"
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

// Provider IDs of the account-backed vendors.
const (
	CodexID   = "codex"
	CopilotID = "copilot"
	ClaudeID  = "claude" // never offered; see ClaudeSignedIn
)

// CodexBase is the ChatGPT Codex backend. Requests for codex/<model> go to
// CodexBase + "/responses". Tests point it at a local fake server.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// Overridable by tests.
var (
	codexRefreshURL = "https://auth.openai.com/oauth/token"
	copilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
)

const (
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	copilotBase   = "https://api.githubcopilot.com"
)

var (
	errCodexOut   = errors.New("Codex 登录已失效：已登出，请运行 `codex login`")
	errCopilotOut = errors.New("Copilot 登录已失效，请重新登录 Copilot")
)

// Account describes the login behind an account provider.
type Account struct {
	ID     string // "codex" or "copilot"
	User   string // e-mail for Codex, GitHub login for Copilot
	Plan   string // subscription plan, when the login says
	Source string // the agent that holds the login
}

// IsAccountID reports whether an id names an account-backed provider.
func IsAccountID(id string) bool { return id == CodexID || id == CopilotID }

// IsAccount reports whether the provider is backed by another agent's login.
func (p Provider) IsAccount() bool { return p.Account != nil }

// ClaudeSignedIn reports whether Claude Code holds a login. It only checks
// that the credentials file exists; the contents are not read.
func ClaudeSignedIn() bool {
	st, err := os.Stat(claudeCredPath())
	return err == nil && !st.IsDir()
}

func claudeCredPath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}

// AccountOf reports whether the agent behind an account provider is signed
// in, and as whom. Nothing is cached: the agent's file is read each time.
func AccountOf(id string) (Account, bool) {
	switch id {
	case CodexID:
		return codexAccount()
	case CopilotID:
		return copilotAccount()
	}
	return Account{}, false
}

// accountProvider builds the provider view of a signed-in account. Its
// models are the user's picks, kept in the config file.
func accountProvider(id string, a Account, models []string) Provider {
	p := Provider{ID: id, Models: models, Account: &a}
	switch id {
	case CodexID:
		p.Name = "Codex"
		p.Responses = CodexBase
	case CopilotID:
		p.Name = "Copilot"
		p.Chat = copilotBase
	}
	return p
}

// accountModels is what an account can serve: the list fetched from it when
// there is one, else the built-in list.
func accountModels(id string) []catalog.Model {
	var known []catalog.Model
	switch id {
	case CodexID:
		known = catalog.Codex()
	case CopilotID:
		known = copilotBuiltinModels()
	}
	if live, _, ok := catalog.Live(id); ok {
		return catalog.Decorate(live, known)
	}
	return known
}

// copilotBuiltinModels is the built-in Copilot list without "auto" and
// embedding models, which are not chat models.
func copilotBuiltinModels() []catalog.Model {
	var out []catalog.Model
	for _, m := range catalog.Builtin("copilot") {
		if m.ID == "auto" || strings.Contains(m.ID, "embed") {
			continue
		}
		out = append(out, m)
	}
	return out
}

// Upstream returns where one request to an account provider goes, and the
// headers that prove the login. It reads the agent's login at call time.
func Upstream(ctx context.Context, p Provider, to Protocol) (string, map[string]string, error) {
	switch p.ID {
	case CodexID:
		if to != Responses {
			return "", nil, fmt.Errorf("Codex only speaks Responses, not %s", to)
		}
		access, account, err := codexToken(ctx)
		if err != nil {
			return "", nil, err
		}
		return CodexBase, map[string]string{
			"Authorization":      "Bearer " + access,
			"chatgpt-account-id": account,
		}, nil
	case CopilotID:
		_, oauth, ok := readCopilot()
		if !ok {
			return "", nil, errCopilotOut
		}
		s, err := copilotSessionOf(ctx, oauth)
		if err != nil {
			return "", nil, err
		}
		base := s.Endpoints.API
		if base == "" {
			base = copilotBase
		}
		return base, copilotHeaders(s.Token), nil
	}
	return "", nil, fmt.Errorf("%s is not an account provider", p.Name)
}

// ---- Codex -----------------------------------------------------------------

func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

type codexLogin struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

// readCodex returns the ChatGPT login in Codex's file. An API-key login is
// not an account.
func readCodex() (codexLogin, bool) {
	var f codexLogin
	b, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return f, false
	}
	if json.Unmarshal(b, &f) != nil || f.AuthMode != "chatgpt" || f.Tokens.AccessToken == "" {
		return f, false
	}
	return f, true
}

func codexAccount() (Account, bool) {
	f, ok := readCodex()
	if !ok {
		return Account{}, false
	}
	idc := jwtClaims(f.Tokens.IDToken)
	acc := jwtClaims(f.Tokens.AccessToken)
	user, _ := idc["email"].(string)
	if user == "" {
		if prof, _ := acc["https://api.openai.com/profile"].(map[string]any); prof != nil {
			user, _ = prof["email"].(string)
		}
	}
	if user == "" {
		user = "ChatGPT account"
	}
	plan := planOf(idc)
	if plan == "" {
		plan = planOf(acc)
	}
	return Account{ID: CodexID, User: user, Plan: plan, Source: "Codex"}, true
}

func planOf(claims map[string]any) string {
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	plan, _ := auth["chatgpt_plan_type"].(string)
	return plan
}

// jwtClaims decodes the payload of a JWT without verifying it. dial only
// needs the e-mail and the expiry, and the agent itself already checked the
// signature.
func jwtClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func jwtExpiry(tok string) (time.Time, bool) {
	exp, ok := jwtClaims(tok)["exp"].(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(exp), 0), true
}

// codexToken returns the access token and account id to use now. A token
// within five minutes of expiry is refreshed the way Codex does it, and the
// new tokens go back into Codex's file, other fields untouched.
func codexToken(ctx context.Context) (string, string, error) {
	f, ok := readCodex()
	if !ok {
		return "", "", errCodexOut
	}
	exp, known := jwtExpiry(f.Tokens.AccessToken)
	if !known || time.Until(exp) > 5*time.Minute {
		return f.Tokens.AccessToken, f.Tokens.AccountID, nil
	}
	fresh, err := refreshCodex(ctx, f.Tokens.RefreshToken)
	if err != nil {
		return "", "", errCodexOut
	}
	writeCodexTokens(fresh)
	return fresh["access_token"], f.Tokens.AccountID, nil
}

// refreshCodex trades a refresh token for new tokens at OpenAI's auth server.
func refreshCodex(ctx context.Context, refresh string) (map[string]string, error) {
	if refresh == "" {
		return nil, errCodexOut
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexRefreshURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh: %s", res.Status)
	}
	var out struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	if out.AccessToken == "" {
		return nil, errors.New("refresh: no access token")
	}
	return map[string]string{
		"id_token":      out.IDToken,
		"access_token":  out.AccessToken,
		"refresh_token": out.RefreshToken,
	}, nil
}

// writeCodexTokens stores refreshed tokens in Codex's own file. Fields dial
// does not know stay as they were. A failed write is not fatal: the new
// token still works for this request.
func writeCodexTokens(fresh map[string]string) {
	p := codexAuthPath()
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var raw map[string]any
	if dec.Decode(&raw) != nil || raw == nil {
		return
	}
	tok, _ := raw["tokens"].(map[string]any)
	if tok == nil {
		tok = map[string]any{}
	}
	for k, v := range fresh {
		if v != "" {
			tok[k] = v
		}
	}
	raw["tokens"] = tok
	raw["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	nb, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(p, append(nb, '\n'), 0o600)
}

// ---- Copilot ---------------------------------------------------------------

func copilotAppsPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "github-copilot", "apps.json")
}

// readCopilot finds the GitHub login the editor or Copilot CLI stored.
func readCopilot() (user, oauth string, ok bool) {
	b, err := os.ReadFile(copilotAppsPath())
	if err != nil {
		return "", "", false
	}
	var apps map[string]json.RawMessage
	if json.Unmarshal(b, &apps) != nil {
		return "", "", false
	}
	keys := make([]string, 0, len(apps))
	for k := range apps {
		if strings.HasPrefix(k, "github.com") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		var app struct {
			User  string `json:"user"`
			OAuth string `json:"oauth_token"`
		}
		if json.Unmarshal(apps[k], &app) == nil && app.OAuth != "" {
			return app.User, app.OAuth, true
		}
	}
	return "", "", false
}

func copilotAccount() (Account, bool) {
	user, _, ok := readCopilot()
	if !ok {
		return Account{}, false
	}
	if user == "" {
		user = "GitHub account"
	}
	return Account{ID: CopilotID, User: user, Source: "Copilot"}, true
}

type copilotSession struct {
	Token     string `json:"token"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
}

// copilotSessionOf trades the GitHub token for a short-lived Copilot session,
// which also says where the API lives.
func copilotSessionOf(ctx context.Context, oauth string) (copilotSession, error) {
	var s copilotSession
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotTokenURL, nil)
	if err != nil {
		return s, err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return s, errCopilotOut
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return s, errCopilotOut
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&s) != nil || s.Token == "" {
		return s, errCopilotOut
	}
	return s, nil
}
