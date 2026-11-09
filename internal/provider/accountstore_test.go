package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// asxHome is a temp home with no Claude Code CLI, no keychain and a fresh store.
func asxHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	forgetClaudeCredential()
	forgetClaudeStatus()
	return home
}

// asxFakeLogin replaces the agent's own sign-in for the rest of the test.
func asxFakeLogin(t *testing.T, login func(agent string) error) {
	t.Helper()
	old := agentLogin
	agentLogin = login
	t.Cleanup(func() { agentLogin = old })
}

// asxClaudeLogin writes Claude Code's login for one account: the credential
// file (with an MCP block a switch must keep) and the config file naming the account.
func asxClaudeLogin(t *testing.T, home, email, access string, expiry time.Time) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken": access, "refreshToken": "rt-" + access,
			"expiresAt": expiry.UnixMilli(), "subscriptionType": "max",
		},
		"mcpOAuth": map[string]any{"slack": "keep-me"},
	})
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{
		"numStartups": 7, "theme": "dark",
		"oauthAccount": map[string]any{"emailAddress": email, "accountUuid": "uuid-" + access},
	})
	forgetClaudeCredential()
	forgetClaudeStatus()
}

// asxReadJSON reads a JSON object from a file.
func asxReadJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// asxClaudeAgentToken is the access token Claude Code itself would use now.
func asxClaudeAgentToken(t *testing.T, home string) string {
	t.Helper()
	oauth, _ := asxReadJSON(t, filepath.Join(home, ".claude", ".credentials.json"))["claudeAiOauth"].(map[string]any)
	tok, _ := oauth["accessToken"].(string)
	return tok
}

// asxCodexLogin writes Codex CLI's auth.json for one ChatGPT account.
func asxCodexLogin(t *testing.T, home, email, refresh string) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token": fakeJWT(map[string]any{"email": email,
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-" + refresh}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix()), "sub": refresh}),
			"refresh_token": refresh,
			"account_id":    "acct-" + refresh,
		},
		"last_refresh": "2026-01-01T00:00:00Z",
	})
}

func asxSignIns(t *testing.T, agent string) map[string]SignIn {
	t.Helper()
	out := map[string]SignIn{}
	for _, s := range SignIns(agent) {
		out[s.User] = s
	}
	return out
}

func TestAsxAddRemembersSecondAccountAsOnAndKeepsFirst(t *testing.T) {
	home := asxHome(t)
	asxClaudeLogin(t, home, "a@example.com", "tok-a", time.Now().Add(time.Hour))
	asxFakeLogin(t, func(string) error {
		asxClaudeLogin(t, home, "b@example.com", "tok-b", time.Now().Add(time.Hour))
		return nil
	})

	name, err := AddAccount("cc")
	if err != nil || name != "b@example.com" {
		t.Fatalf("add: %q %v", name, err)
	}
	// the agent is still signed in as a, with its own files back as they were
	if got := asxClaudeAgentToken(t, home); got != "tok-a" {
		t.Fatalf("agent token after add = %q, want tok-a", got)
	}
	if mcp := asxReadJSON(t, filepath.Join(home, ".claude", ".credentials.json"))["mcpOAuth"]; mcp == nil {
		t.Fatal("mcpOAuth lost on restore")
	}
	signs := asxSignIns(t, "claude")
	if !signs["a@example.com"].Current || !signs["a@example.com"].On {
		t.Fatalf("a: %+v", signs["a@example.com"])
	}
	if b := signs["b@example.com"]; b.Current || !b.On {
		t.Fatalf("b should be remembered and on: %+v", b)
	}
	// b serves alongside, under its own id
	if p, ok := find(All(), "claude-b-example-com"); !ok || p.Account == nil || p.Account.User != "b@example.com" {
		t.Fatalf("b provider: %+v %v", p, ok)
	}
}

func TestAsxSwitchKeepsLatestCredentialsAndOtherFields(t *testing.T) {
	home := asxHome(t)
	asxClaudeLogin(t, home, "a@example.com", "tok-a", time.Now().Add(time.Hour))
	asxFakeLogin(t, func(string) error {
		asxClaudeLogin(t, home, "b@example.com", "tok-b", time.Now().Add(time.Hour))
		return nil
	})
	if _, err := AddAccount("claude"); err != nil {
		t.Fatal(err)
	}
	// Claude Code rotates a's token while a is the login
	asxClaudeLogin(t, home, "a@example.com", "tok-a2", time.Now().Add(time.Hour))

	if err := SwitchLogin("CLAUDE", "B@Example.com"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if got := asxClaudeAgentToken(t, home); got != "tok-b" {
		t.Fatalf("agent token = %q, want tok-b", got)
	}
	creds := asxReadJSON(t, filepath.Join(home, ".claude", ".credentials.json"))
	if mcp, _ := creds["mcpOAuth"].(map[string]any); mcp["slack"] != "keep-me" {
		t.Fatalf("mcpOAuth changed: %v", creds["mcpOAuth"])
	}
	cfg := asxReadJSON(t, filepath.Join(home, ".claude.json"))
	if cfg["numStartups"] != float64(7) || cfg["theme"] != "dark" {
		t.Fatalf("config fields changed: %v", cfg)
	}
	if acct, _ := cfg["oauthAccount"].(map[string]any); acct["emailAddress"] != "b@example.com" {
		t.Fatalf("oauthAccount = %v", cfg["oauthAccount"])
	}
	// a keeps the token it had when it was switched away from
	if cred, ok := storedCred("claude", "a@example.com"); !ok {
		t.Fatal("a not remembered with its credentials")
	} else if c, ok := parseClaudeCredentials(cred); !ok || c.OAuth.AccessToken != "tok-a2" {
		t.Fatalf("a's stored token = %q, want tok-a2", c.OAuth.AccessToken)
	}
	if _, ok := storedCred("claude", "b@example.com"); ok {
		t.Fatal("b still has a copy while it is the agent's login")
	}
	if a := asxSignIns(t, "claude")["a@example.com"]; !a.On {
		t.Fatalf("a should inherit b's on state: %+v", a)
	}

	// and back again: a's latest token comes back
	if err := SwitchLogin("claude", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := asxClaudeAgentToken(t, home); got != "tok-a2" {
		t.Fatalf("switch back: token %q, want tok-a2", got)
	}
}

func TestAsxForgetRefusesCurrentAndUnknownErrors(t *testing.T) {
	home := asxHome(t)
	asxClaudeLogin(t, home, "a@example.com", "tok-a", time.Now().Add(time.Hour))
	asxFakeLogin(t, func(string) error {
		asxClaudeLogin(t, home, "b@example.com", "tok-b", time.Now().Add(time.Hour))
		return nil
	})
	if _, err := AddAccount("claude"); err != nil {
		t.Fatal(err)
	}

	if err := ForgetLogin("claude", "a@example.com"); err == nil {
		t.Fatal("forgot the current login")
	}
	if err := ForgetLogin("claude", "nobody@example.com"); err == nil {
		t.Fatal("forgot an unknown account")
	}
	if err := SwitchLogin("claude", "nobody@example.com"); err == nil {
		t.Fatal("switched to an unknown account")
	}
	if err := ForgetLogin("claude", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, ok := asxSignIns(t, "claude")["b@example.com"]; ok {
		t.Fatal("b still listed after forget")
	}
	if err := ForgetLogin("gemini", "a@example.com"); err == nil {
		t.Fatal("unknown agent accepted")
	}
}

func TestAsxSetLoginOnRules(t *testing.T) {
	home := asxHome(t)
	asxClaudeLogin(t, home, "a@example.com", "tok-a", time.Now().Add(time.Hour))
	asxFakeLogin(t, func(string) error {
		asxClaudeLogin(t, home, "b@example.com", "tok-b", time.Now().Add(time.Hour))
		return nil
	})
	if _, err := AddAccount("claude"); err != nil {
		t.Fatal(err)
	}
	if err := SetLoginOn("claude", "a@example.com", false); err == nil {
		t.Fatal("turned off the current login")
	}
	if err := SetLoginOn("claude", "nobody@example.com", true); err == nil {
		t.Fatal("unknown account accepted")
	}
	if err := SetLoginOn("claude", "b@example.com", false); err != nil {
		t.Fatal(err)
	}
	if asxSignIns(t, "claude")["b@example.com"].On {
		t.Fatal("b still on")
	}
	if err := SetLoginOn("claude", "b@example.com", true); err != nil {
		t.Fatal(err)
	}
	if !asxSignIns(t, "claude")["b@example.com"].On {
		t.Fatal("b not on")
	}
}

func TestAsxCandidateOrderCurrentFirstThenSaved(t *testing.T) {
	home := asxHome(t)
	asxClaudeLogin(t, home, "a@example.com", "tok-a", time.Now().Add(time.Hour))
	asxFakeLogin(t, func(string) error {
		asxClaudeLogin(t, home, "b@example.com", "tok-b", time.Now().Add(time.Hour))
		return nil
	})
	if _, err := AddAccount("claude"); err != nil {
		t.Fatal(err)
	}
	asxFakeLogin(t, func(string) error {
		asxClaudeLogin(t, home, "c@example.com", "tok-c", time.Now().Add(time.Hour))
		return nil
	})
	if _, err := AddAccount("claude"); err != nil {
		t.Fatal(err)
	}
	// the agent is back on a; c was added after b
	asxClaudeLogin(t, home, "a@example.com", "tok-a", time.Now().Add(time.Hour))
	forgetClaudeCredential()
	forgetClaudeStatus()

	cur, ok := find(All(), "claude")
	if !ok {
		t.Fatal("no claude provider")
	}
	var got []string
	for _, p := range AccountPeers(cur) {
		got = append(got, p.Account.User)
	}
	if len(got) != 2 || got[0] != "b@example.com" || got[1] != "c@example.com" {
		t.Fatalf("peers of the current login = %v, want b then c", got)
	}
	// a request for b puts the others after it, the current login first
	b, ok := find(All(), "claude-b-example-com")
	if !ok {
		t.Fatal("no b provider")
	}
	got = nil
	for _, p := range AccountPeers(b) {
		got = append(got, p.Account.User)
	}
	if len(got) != 2 || got[0] != "a@example.com" || got[1] != "c@example.com" {
		t.Fatalf("peers of b = %v, want a then c", got)
	}
}

func TestAsxStoredAccountRefreshWritesBackToStore(t *testing.T) {
	home := asxHome(t)
	asxClaudeLogin(t, home, "a@example.com", "tok-a", time.Now().Add(time.Hour))
	asxFakeLogin(t, func(string) error {
		// b's token is already past its expiry, so using it forces a refresh
		asxClaudeLogin(t, home, "b@example.com", "tok-b", time.Now().Add(-time.Hour))
		return nil
	})
	if _, err := AddAccount("claude"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-b-new", "refresh_token": "rt-b-new", "expires_in": 3600})
	}))
	defer srv.Close()
	claudeTokenURL = srv.URL

	tok, err := claudeTokenFrom(context.Background(), storedClaudeSource("b@example.com"))
	if err != nil || tok != "tok-b-new" {
		t.Fatalf("token = %q %v", tok, err)
	}
	cred, ok := storedCred("claude", "b@example.com")
	if !ok {
		t.Fatal("b lost its credentials")
	}
	c, _ := parseClaudeCredentials(cred)
	if c.OAuth.AccessToken != "tok-b-new" || c.OAuth.RefreshToken != "rt-b-new" {
		t.Fatalf("stored b = %+v", c.OAuth)
	}
	// the agent's own file is untouched by a refresh of a remembered account
	if got := asxClaudeAgentToken(t, home); got != "tok-a" {
		t.Fatalf("agent token = %q, want tok-a", got)
	}
}

func TestAsxCodexSwitchKeepsOtherFields(t *testing.T) {
	home := asxHome(t)
	asxCodexLogin(t, home, "a@example.com", "rt-a")
	asxFakeLogin(t, func(string) error {
		asxCodexLogin(t, home, "b@example.com", "rt-b")
		return nil
	})
	if _, err := AddAccount("codex"); err != nil {
		t.Fatal(err)
	}
	if got := asxReadJSON(t, codexAuthPath())["tokens"].(map[string]any)["refresh_token"]; got != "rt-a" {
		t.Fatalf("codex login after add = %v, want rt-a", got)
	}
	if err := SwitchLogin("codex", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	auth := asxReadJSON(t, codexAuthPath())
	if toks := auth["tokens"].(map[string]any); toks["refresh_token"] != "rt-b" {
		t.Fatalf("tokens = %v", toks)
	}
	if _, ok := auth["OPENAI_API_KEY"]; !ok {
		t.Fatal("OPENAI_API_KEY dropped by switch")
	}
	if cred, ok := storedCred("codex", "a@example.com"); !ok {
		t.Fatal("a not remembered")
	} else if m, _ := decodeRaw(cred); m["tokens"].(map[string]any)["refresh_token"] != "rt-a" {
		t.Fatalf("a's stored login = %v", m["tokens"])
	}
	// the on-state a took over from b
	if a := asxSignIns(t, "codex")["a@example.com"]; !a.On {
		t.Fatalf("a: %+v", a)
	}
}

func TestAsxStoreFileIsPrivateAndUnknownAgentRejected(t *testing.T) {
	home := asxHome(t)
	asxClaudeLogin(t, home, "a@example.com", "tok-a", time.Now().Add(time.Hour))
	asxFakeLogin(t, func(string) error {
		asxClaudeLogin(t, home, "b@example.com", "tok-b", time.Now().Add(time.Hour))
		return nil
	})
	if _, err := AddAccount("claude"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(accountsPath())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("accounts.json mode %v, want 0600", st.Mode().Perm())
	}
	if _, err := AddAccount("gemini"); err == nil {
		t.Fatal("unknown agent accepted by add")
	}
	if err := SwitchLogin("gemini", "a@example.com"); err == nil {
		t.Fatal("unknown agent accepted by switch")
	}
	if name, ok := AccountAgent("cc"); !ok || name != "claude" {
		t.Fatalf("cc = %q %v", name, ok)
	}
}
