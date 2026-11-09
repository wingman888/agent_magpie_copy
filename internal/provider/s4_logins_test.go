package provider

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// s4JWT is a token whose payload is the given claims; nobody checks the signature.
func s4JWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func s4WriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func s4ReadRaw(t *testing.T, path string) (map[string]any, string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m, string(b)
}

// s4Home isolates HOME, the magpie config dir, Claude Code's config dir and
// the Keychain, and forgets what earlier tests cached.
func s4Home(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	oldKeychain, oldWho := claudeKeychain, claudeWho
	claudeKeychain = false
	forgetClaudeCredential()
	forgetClaudeStatus()
	t.Cleanup(func() {
		claudeKeychain, claudeWho = oldKeychain, oldWho
		forgetClaudeCredential()
		forgetClaudeStatus()
	})
	return home
}

// s4ClaudeEmails answers "whose account is this" from the refresh token in
// Claude Code's credential file, as `claude auth status` would.
func s4ClaudeEmails(emails map[string]string) {
	claudeWho = func() (string, string, bool) {
		c, _, ok := readClaudeCredential()
		if !ok {
			return "", "", true
		}
		return emails[c.OAuth.RefreshToken], "", false
	}
}

func s4ClaudeOAuth(refresh string) map[string]any {
	return map[string]any{
		"accessToken":      "at-" + refresh,
		"refreshToken":     refresh,
		"expiresAt":        time.Now().Add(time.Hour).UnixMilli(),
		"subscriptionType": "max",
	}
}

func s4RawMsg(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func s4LoginsOf(t *testing.T, agent string) []Login {
	t.Helper()
	ls, err := Logins(agent)
	if err != nil {
		t.Fatal(err)
	}
	return ls
}

func s4FindLogin(t *testing.T, agent, user string) loginRecord {
	t.Helper()
	f := loadLogins()
	i := findLogin(f, agent, user)
	if i < 0 {
		t.Fatalf("%s account %s not remembered", agent, user)
	}
	return f.Accounts[i]
}

func TestS4LoginSwitchClaudeKeepsFieldsAndTokens(t *testing.T) {
	s4Home(t)
	s4ClaudeEmails(map[string]string{"rA": "a@x.com", "rB": "b@x.com"})
	credPath := filepath.Join(claudeConfigDir(), ".credentials.json")
	s4WriteJSON(t, credPath, map[string]any{
		"claudeAiOauth": s4ClaudeOAuth("rA"),
		"mcpOAuth":      map[string]any{"srv": map[string]any{"token": "m"}},
	})
	if err := saveLogins(loginFile{Accounts: []loginRecord{
		{Agent: "claude", User: "b@x.com", Plan: "pro", On: true, Creds: s4RawMsg(t, s4ClaudeOAuth("rB"))},
	}}); err != nil {
		t.Fatal(err)
	}

	ls := s4LoginsOf(t, "claude")
	if len(ls) != 2 || !ls[0].Current || ls[0].User != "a@x.com" || ls[1].User != "b@x.com" || !ls[1].On {
		t.Fatalf("before switch: %+v", ls)
	}

	name, err := SwitchLogin("claude", "B@X.com")
	if err != nil {
		t.Fatal(err)
	}
	if name != "b@x.com" {
		t.Fatalf("switched to %q", name)
	}
	raw, text := s4ReadRaw(t, credPath)
	oauth, _ := raw["claudeAiOauth"].(map[string]any)
	if oauth["refreshToken"] != "rB" {
		t.Fatalf("agent refresh token = %v, want rB", oauth["refreshToken"])
	}
	if _, ok := raw["mcpOAuth"]; !ok {
		t.Fatalf("switch dropped the agent's other field: %s", text)
	}
	if strings.Contains(text, "rA") {
		t.Fatalf("the outgoing refresh token is still in the agent's file: %s", text)
	}
	// the outgoing account keeps its latest tokens, and takes over the on flag
	a := s4FindLogin(t, "claude", "a@x.com")
	if !strings.Contains(string(a.Creds), `"rA"`) || !a.On {
		t.Fatalf("outgoing account: creds=%s on=%v", a.Creds, a.On)
	}
	b := s4FindLogin(t, "claude", "b@x.com")
	if b.Creds != nil {
		t.Fatalf("target still holds a copy of its tokens: %s", b.Creds)
	}

	// and back again, with the tokens that were parked
	if _, err := SwitchLogin("claude", "a@x.com"); err != nil {
		t.Fatal(err)
	}
	raw, _ = s4ReadRaw(t, credPath)
	if raw["claudeAiOauth"].(map[string]any)["refreshToken"] != "rA" {
		t.Fatalf("switching back did not restore a's tokens")
	}
	if got := s4FindLogin(t, "claude", "b@x.com"); !strings.Contains(string(got.Creds), `"rB"`) {
		t.Fatalf("b's latest tokens were not kept: %s", got.Creds)
	}
}

func TestS4LoginSwitchCodexKeepsOtherFields(t *testing.T) {
	home := s4Home(t)
	authPath := filepath.Join(home, ".codex", "auth.json")
	exp := float64(time.Now().Add(time.Hour).Unix())
	s4WriteJSON(t, authPath, map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"keep":           "me",
		"last_refresh":   "2026-01-01T00:00:00Z",
		"tokens": map[string]any{
			"id_token":      s4JWT(map[string]any{"email": "c@x.com"}),
			"access_token":  s4JWT(map[string]any{"exp": exp}),
			"refresh_token": "rC",
			"account_id":    "acct-c",
		},
	})
	if err := saveLogins(loginFile{Accounts: []loginRecord{{Agent: "codex", User: "d@x.com", On: false,
		Creds: s4RawMsg(t, map[string]any{
			"auth_mode":    "chatgpt",
			"last_refresh": "2026-02-01T00:00:00Z",
			"tokens": map[string]any{
				"id_token":      s4JWT(map[string]any{"email": "d@x.com"}),
				"access_token":  s4JWT(map[string]any{"exp": exp}),
				"refresh_token": "rD",
				"account_id":    "acct-d",
			},
		})}}}); err != nil {
		t.Fatal(err)
	}

	if _, err := SwitchLogin("codex", "d@X.com"); err != nil {
		t.Fatal(err)
	}
	raw, text := s4ReadRaw(t, authPath)
	toks, _ := raw["tokens"].(map[string]any)
	if toks["refresh_token"] != "rD" || toks["account_id"] != "acct-d" {
		t.Fatalf("agent tokens after switch: %v", toks)
	}
	if raw["keep"] != "me" {
		t.Fatalf("switch dropped an unrelated field: %s", text)
	}
	if strings.Contains(text, `"rC"`) {
		t.Fatalf("outgoing refresh token still in auth.json")
	}
	if c := s4FindLogin(t, "codex", "c@x.com"); !strings.Contains(string(c.Creds), `"rC"`) {
		t.Fatalf("outgoing codex sign-in not kept: %s", c.Creds)
	}
	if ls := s4LoginsOf(t, "codex"); len(ls) != 2 || ls[0].User != "d@x.com" || !ls[0].Current {
		t.Fatalf("codex listing after switch: %+v", ls)
	}
}

func TestS4LoginCurrentCannotBeForgottenOthersCan(t *testing.T) {
	s4Home(t)
	s4ClaudeEmails(map[string]string{"rA": "a@x.com", "rB": "b@x.com"})
	s4WriteJSON(t, filepath.Join(claudeConfigDir(), ".credentials.json"), map[string]any{"claudeAiOauth": s4ClaudeOAuth("rA")})
	if err := saveLogins(loginFile{Accounts: []loginRecord{
		{Agent: "claude", User: "b@x.com", Creds: s4RawMsg(t, s4ClaudeOAuth("rB"))},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ForgetLogin("claude", "a@x.com"); err == nil {
		t.Fatal("forgetting the signed-in account was allowed")
	}
	if _, err := ForgetLogin("claude", "b@x.com"); err != nil {
		t.Fatal(err)
	}
	if ls := s4LoginsOf(t, "claude"); len(ls) != 1 || ls[0].User != "a@x.com" {
		t.Fatalf("after forgetting b: %+v", ls)
	}
}

func TestS4LoginUnknownAccountAndAgent(t *testing.T) {
	s4Home(t)
	s4ClaudeEmails(map[string]string{"rA": "a@x.com"})
	s4WriteJSON(t, filepath.Join(claudeConfigDir(), ".credentials.json"), map[string]any{"claudeAiOauth": s4ClaudeOAuth("rA")})
	if _, err := SwitchLogin("claude", "nobody@x.com"); err == nil {
		t.Fatal("switching to an unknown account did not fail")
	}
	if _, err := ForgetLogin("claude", "nobody@x.com"); err == nil {
		t.Fatal("forgetting an unknown account did not fail")
	}
	if err := SetLoginOn("claude", "nobody@x.com", true); err == nil {
		t.Fatal("SetLoginOn on an unknown account did not fail")
	}
	if _, err := Logins("gemini"); err == nil {
		t.Fatal("an unknown agent was accepted")
	}
	for in, want := range map[string]string{"claude": "claude", "CC": "claude", "claude-code": "claude", "Codex": "codex"} {
		got, err := CanonicalLoginAgent(in)
		if err != nil || got != want {
			t.Fatalf("CanonicalLoginAgent(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := CanonicalLoginAgent("gemini"); err == nil {
		t.Fatal("CanonicalLoginAgent accepted gemini")
	}
}

func TestS4LoginSetOnRules(t *testing.T) {
	s4Home(t)
	s4ClaudeEmails(map[string]string{"rA": "a@x.com", "rB": "b@x.com"})
	s4WriteJSON(t, filepath.Join(claudeConfigDir(), ".credentials.json"), map[string]any{"claudeAiOauth": s4ClaudeOAuth("rA")})
	if err := saveLogins(loginFile{Accounts: []loginRecord{
		{Agent: "claude", User: "b@x.com", Creds: s4RawMsg(t, s4ClaudeOAuth("rB"))},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := SetLoginOn("claude", "a@x.com", false); err == nil {
		t.Fatal("turning off the signed-in account was allowed")
	}
	if err := SetLoginOn("claude", "b@x.com", true); err != nil {
		t.Fatal(err)
	}
	if ls := s4LoginsOf(t, "claude"); len(ls) != 2 || !ls[1].On {
		t.Fatalf("b should be on: %+v", ls)
	}
	if err := SetLoginOn("claude", "b@x.com", false); err != nil {
		t.Fatal(err)
	}
	if ls := s4LoginsOf(t, "claude"); ls[1].On {
		t.Fatalf("b should be off: %+v", ls)
	}
	// the stored on flag never writes into the agent's own store
	raw, _ := s4ReadRaw(t, filepath.Join(claudeConfigDir(), ".credentials.json"))
	if _, ok := raw["on"]; ok {
		t.Fatal("on flag leaked into the agent's credentials")
	}
}

func TestS4LoginSecondAccountOnByDefault(t *testing.T) {
	s4Home(t)
	s4ClaudeEmails(map[string]string{"rA": "a@x.com", "rB": "b@x.com"})
	credPath := filepath.Join(claudeConfigDir(), ".credentials.json")
	s4WriteJSON(t, credPath, map[string]any{"claudeAiOauth": s4ClaudeOAuth("rA")})
	if err := RememberLogins(); err != nil {
		t.Fatal(err)
	}
	if a := s4FindLogin(t, "claude", "a@x.com"); a.On {
		t.Fatal("the first account should not be on alongside anything")
	}
	s4WriteJSON(t, credPath, map[string]any{"claudeAiOauth": s4ClaudeOAuth("rB")})
	if err := RememberLogins(); err != nil {
		t.Fatal(err)
	}
	if b := s4FindLogin(t, "claude", "b@x.com"); !b.On {
		t.Fatal("a second account recorded by magpie should be on")
	}
}

func TestS4LoginStorePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not meaningful on Windows")
	}
	s4Home(t)
	if err := saveLogins(loginFile{}); err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(filepath.Dir(loginPath()))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(loginPath())
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("dir %v file %v, want 0700 and 0600", di.Mode().Perm(), fi.Mode().Perm())
	}
}
