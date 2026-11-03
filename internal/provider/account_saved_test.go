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

// savedHome points HOME and magpie's config at a temp dir; nobody is signed in.
func savedHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

// fakeClaudeWho answers "who is Claude Code signed in as" from the access token
// in the credential file, standing in for `claude auth status`.
func fakeClaudeWho(t *testing.T, emails map[string]string) {
	t.Helper()
	old := claudeWhoAmI
	claudeWhoAmI = func() (string, string, bool) {
		c, _, ok := readClaudeCredential()
		if !ok {
			return "", "", true
		}
		return emails[c.OAuth.AccessToken], "max", false
	}
	t.Cleanup(func() { claudeWhoAmI = old })
}

// writeClaudeLogin writes Claude Code's credential file as a sign-in with token tok.
func writeClaudeLogin(t *testing.T, home, tok string) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken": tok, "refreshToken": "refresh-" + tok, "subscriptionType": "max",
			"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "scopes": []string{"user:inference"},
			"futureField": "keep",
		},
		"mcpOAuth": map[string]any{"linear": map[string]any{"accessToken": "mcp"}},
	})
	forgetClaudeCredential()
}

// codexSignIn writes Codex's auth.json as a ChatGPT sign-in, with a field magpie
// must not touch.
func codexSignIn(t *testing.T, home, email, acct string) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt", "x_custom": "keep",
		"tokens": map[string]any{
			"id_token": fakeJWT(map[string]any{"email": email,
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": acct}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "refresh-" + acct, "account_id": acct,
		},
	})
}

// stubLogin runs fn in place of the agent's own sign-in.
func stubLogin(t *testing.T, fn func(agent string)) {
	t.Helper()
	old := agentLogin
	agentLogin = func(agent string) error { fn(agent); return nil }
	t.Cleanup(func() { agentLogin = old })
}

// storedAccess is the access token magpie keeps for a remembered account, or ""
// when it holds none.
func storedAccess(agent, user string) string {
	f := loadAccounts()
	i := findAccount(f, agent, user)
	if i < 0 || len(f.Accounts[i].Creds) == 0 {
		return ""
	}
	var m map[string]any
	json.Unmarshal(f.Accounts[i].Creds, &m)
	key := "accessToken"
	if agent == "codex" {
		key = "access_token"
	}
	s, _ := m[key].(string)
	return s
}

func expireStored(t *testing.T, agent, user string) {
	t.Helper()
	f := loadAccounts()
	i := findAccount(f, agent, user)
	if i < 0 {
		t.Fatalf("%s %s is not remembered", agent, user)
	}
	var m map[string]any
	json.Unmarshal(f.Accounts[i].Creds, &m)
	m["expiresAt"] = time.Now().Add(-time.Hour).UnixMilli()
	f.Accounts[i].Creds, _ = json.Marshal(m)
	if err := saveAccounts(f); err != nil {
		t.Fatal(err)
	}
}

func TestAccountAgentAliases(t *testing.T) {
	for in, want := range map[string]string{"claude": "claude", "CC": "claude", "codex": "codex"} {
		if got, err := AccountAgent(in); err != nil || got != want {
			t.Fatalf("AccountAgent(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "gemini", "copilot"} {
		if _, err := AccountAgent(in); err == nil {
			t.Fatalf("AccountAgent(%q) accepted", in)
		}
	}
}

func TestClaudeAccountsSwitchAndForget(t *testing.T) {
	home := savedHome(t)
	fakeClaudeWho(t, map[string]string{"tok-a": "a@example.com", "tok-b": "b@example.com"})
	writeClaudeLogin(t, home, "tok-a")

	// listing remembers the sign-in in use
	list, err := ListAccounts("claude")
	if err != nil || len(list) != 1 || !list[0].Current || list[0].User != "a@example.com" {
		t.Fatalf("first listing: %+v, %v", list, err)
	}

	// a second sign-in through magpie: the first keeps its tokens, and the second
	// works alongside from the start
	stubLogin(t, func(string) { writeClaudeLogin(t, home, "tok-b") })
	user, err := AddAccount("cc")
	if err != nil || user != "b@example.com" {
		t.Fatalf("AddAccount = %q, %v", user, err)
	}
	list, _ = ListAccounts("claude")
	if len(list) != 2 || list[0].User != "a@example.com" || list[0].Current || list[0].On ||
		list[1].User != "b@example.com" || !list[1].Current || !list[1].On {
		t.Fatalf("after add: %+v", list)
	}
	if got := storedAccess("claude", "a@example.com"); got != "tok-a" {
		t.Fatalf("first account's tokens not kept: %q", got)
	}

	// the replaced sign-in inherits the target's "works alongside" state
	if err := SetLoginOn("claude", "a@example.com", true); err != nil {
		t.Fatal(err)
	}
	switched, err := SwitchAccount("claude", "A@Example.COM")
	if err != nil || !switched {
		t.Fatalf("SwitchAccount = %v, %v", switched, err)
	}
	c, _, ok := readClaudeCredential()
	if !ok || c.OAuth.AccessToken != "tok-a" {
		t.Fatal("Claude Code is not signed in as A after the switch")
	}
	if c.raw["mcpOAuth"] == nil {
		t.Fatal("switch dropped the agent's other top-level fields")
	}
	if oauth, _ := c.raw["claudeAiOauth"].(map[string]any); oauth["futureField"] != "keep" {
		t.Fatal("switch dropped an unknown field of the sign-in block")
	}
	if got := storedAccess("claude", "b@example.com"); got != "tok-b" {
		t.Fatalf("replaced sign-in's latest tokens not kept: %q", got)
	}
	if got := storedAccess("claude", "a@example.com"); got != "" {
		t.Fatalf("the signed-in account's tokens are also held by magpie: %q", got)
	}
	list, _ = ListAccounts("claude")
	if len(list) != 2 || !list[0].Current || list[1].Current || !list[1].On {
		t.Fatalf("after switch: %+v", list)
	}

	// the agent's credential file and magpie's store are owner-only
	for _, p := range []string{filepath.Join(home, ".claude", ".credentials.json"), accountsPath()} {
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s is not 0600: %v %v", p, st, err)
		}
	}

	// the sign-in in use cannot be forgotten or turned off; others can
	if err := ForgetAccount("claude", "a@example.com"); err == nil {
		t.Fatal("forgot the sign-in in use")
	}
	if err := SetLoginOn("claude", "a@example.com", false); err == nil {
		t.Fatal("turned off the sign-in in use")
	}
	if err := ForgetAccount("claude", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	list, _ = ListAccounts("claude")
	if len(list) != 1 || list[0].User != "a@example.com" {
		t.Fatalf("after forget: %+v", list)
	}

	// unknown accounts are errors
	if _, err := SwitchAccount("claude", "nobody@example.com"); err == nil {
		t.Fatal("switched to an account magpie does not know")
	}
	if err := SetLoginOn("claude", "nobody@example.com", true); err == nil {
		t.Fatal("turned on an unknown account")
	}
	if err := ForgetAccount("claude", "nobody@example.com"); err == nil {
		t.Fatal("forgot an unknown account")
	}
}

func TestRememberedAccountWorksAlongside(t *testing.T) {
	home := savedHome(t)
	fakeClaudeWho(t, map[string]string{"tok-a": "a@example.com", "tok-b": "b@example.com"})
	writeClaudeLogin(t, home, "tok-a")
	stubLogin(t, func(string) { writeClaudeLogin(t, home, "tok-b") })
	if _, err := AddAccount("claude"); err != nil {
		t.Fatal(err)
	}
	if err := SetLoginOn("claude", "a@example.com", true); err != nil {
		t.Fatal(err)
	}

	// the sign-in in use is tried first, then the others in the order remembered
	p, ok := find(Accounts(), "claude")
	if !ok {
		t.Fatal("no claude provider")
	}
	cands := p.KeyCandidates()
	if len(cands) != 2 || cands[0].Account.User != "b@example.com" || cands[1].Account.User != "a@example.com" {
		t.Fatalf("candidates: %+v", cands)
	}

	// the remembered account is refreshed by magpie, with its own rotated token,
	// and the agent's file is left alone
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"tok-a2","refresh_token":"refresh-a2","expires_in":3600}`))
	}))
	defer srv.Close()
	claudeTokenURL = srv.URL
	expireStored(t, "claude", "a@example.com")

	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if err := cands[1].Sign(context.Background(), req, Anthropic, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok-a2" {
		t.Fatalf("remembered account signed with %q", got)
	}
	if got := storedAccess("claude", "a@example.com"); got != "tok-a2" {
		t.Fatalf("rotated token not kept: %q", got)
	}
	if c, _, _ := readClaudeCredential(); c.OAuth.AccessToken != "tok-b" {
		t.Fatal("a remembered account wrote into the agent's sign-in")
	}
}

func TestCodexAccountsSwitch(t *testing.T) {
	home := savedHome(t)
	codexSignIn(t, home, "a@example.com", "acct-a")
	stubLogin(t, func(string) { codexSignIn(t, home, "b@example.com", "acct-b") })
	user, err := AddAccount("codex")
	if err != nil || user != "b@example.com" {
		t.Fatalf("AddAccount = %q, %v", user, err)
	}
	if storedAccess("codex", "a@example.com") == "" {
		t.Fatal("first account's tokens not kept")
	}
	if _, err := SwitchAccount("codex", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	raw := readRawMap(codexAuthPath())
	toks, _ := raw["tokens"].(map[string]any)
	if toks["account_id"] != "acct-a" || raw["x_custom"] != "keep" || raw["auth_mode"] != "chatgpt" {
		t.Fatalf("auth.json after switch: %v", raw)
	}
	if storedAccess("codex", "b@example.com") == "" || storedAccess("codex", "a@example.com") != "" {
		t.Fatal("tokens are not held by exactly one party")
	}
	list, _ := ListAccounts("codex")
	// the replaced sign-in takes over the target's state, which was off
	if len(list) != 2 || !list[0].Current || list[1].On {
		t.Fatalf("after switch: %+v", list)
	}
	for _, p := range []string{codexAuthPath(), accountsPath()} {
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s is not 0600: %v %v", p, st, err)
		}
	}
}

func TestSetLoginOnRules(t *testing.T) {
	home := savedHome(t)
	fakeClaudeWho(t, map[string]string{"tok-a": "a@example.com"})
	writeClaudeLogin(t, home, "tok-a")
	if err := SetLoginOn("claude", "a@example.com", false); err == nil {
		t.Fatal("the sign-in in use was turned off")
	}
	if err := SetLoginOn("claude", "a@example.com", true); err != nil {
		t.Fatalf("turning the sign-in in use on: %v", err)
	}
	if err := SetLoginOn("claude", "nobody@example.com", true); err == nil {
		t.Fatal("unknown account accepted")
	}
	if err := SetLoginOn("gemini", "a@example.com", true); err == nil {
		t.Fatal("unknown agent accepted")
	}
}
