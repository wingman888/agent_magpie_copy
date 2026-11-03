package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func codexSignIn(t *testing.T, home, email, refresh string) {
	t.Helper()
	exp := float64(time.Now().Add(time.Hour).Unix())
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}}),
			"access_token":  fakeJWT(map[string]any{"exp": exp}),
			"refresh_token": refresh, "account_id": "acct-" + email,
		},
	})
}

func loginUsers(ls []Login) (users []string, active string) {
	for _, l := range ls {
		users = append(users, l.User)
		if l.Active {
			active = l.User
		}
	}
	return
}

func TestCodexLogins(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)

	users, active := loginUsers(Logins("codex"))
	if strings.Join(users, ",") != "me@example.com,work@example.com" || active != "work@example.com" {
		t.Fatalf("logins %v, active %q", users, active)
	}
	if fi, err := os.Stat(loginsPath()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("logins.json mode: %v %v", fi, err)
	}
	// the agent refreshes the active login in place; a switch keeps that
	codexSignIn(t, home, "work@example.com", "r-work-2")

	if err := SwitchLogin("codex", "ME@example.com"); err != nil {
		t.Fatal(err)
	}
	var a codexAuth
	readJSON(filepath.Join(home, ".codex", "auth.json"), &a)
	if a.Tokens.RefreshToken != "r" {
		t.Fatalf("auth.json not switched: %q", a.Tokens.RefreshToken)
	}
	if _, active := loginUsers(Logins("codex")); active != "me@example.com" {
		t.Fatalf("active after switch: %q", active)
	}
	for _, l := range readLogins() {
		if l.User == "work@example.com" && !strings.Contains(string(l.Auth), "r-work-2") {
			t.Fatalf("the replaced login lost its refreshed token: %s", l.Auth)
		}
	}

	if err := ForgetLogin("codex", "me@example.com"); err == nil {
		t.Fatal("forgot the active login")
	}
	if err := ForgetLogin("codex", "work@example.com"); err != nil {
		t.Fatal(err)
	}
	if users, _ := loginUsers(Logins("codex")); strings.Join(users, ",") != "me@example.com" {
		t.Fatalf("after forget: %v", users)
	}
	if err := SwitchLogin("codex", "nobody@example.com"); err == nil {
		t.Fatal("switched to an unknown login")
	}
}

func TestClaudeLogins(t *testing.T) {
	home := claudeHome(t)
	cred := claudeSignIn(t, home, time.Now().Add(time.Hour))
	profile := filepath.Join(home, ".claude.json")
	writeFile(t, profile, map[string]any{
		"numStartups":  1234567890123,
		"oauthAccount": map[string]any{"emailAddress": "a@example.com", "accountUuid": "u-a"},
	})
	rememberLogins(true)

	// signing in to another account in Claude Code
	writeFile(t, cred, map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "sk-ant-oat01-b", "refreshToken": "sk-ant-ort01-b",
			"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "pro"},
		"mcpOAuth": map[string]any{"keep": true},
	})
	writeFile(t, profile, map[string]any{
		"numStartups":  1234567890123,
		"oauthAccount": map[string]any{"emailAddress": "b@example.com", "accountUuid": "u-b"},
	})
	forgetClaudeCredential()
	rememberLogins(true)

	users, active := loginUsers(Logins("claude"))
	if strings.Join(users, ",") != "a@example.com,b@example.com" || active != "b@example.com" {
		t.Fatalf("logins %v, active %q", users, active)
	}
	if err := SwitchLogin("claude", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	readJSON(cred, &c)
	if o, _ := c["claudeAiOauth"].(map[string]any); o["refreshToken"] != "sk-ant-ort01-old" {
		t.Fatalf("credentials not switched: %v", o)
	}
	b, _ := os.ReadFile(profile)
	var p map[string]json.RawMessage
	json.Unmarshal(b, &p)
	if string(p["numStartups"]) != "1234567890123" || !strings.Contains(string(p["oauthAccount"]), "a@example.com") {
		t.Fatalf(".claude.json after switch: %s", b)
	}
	if _, active := loginUsers(Logins("claude")); active != "a@example.com" {
		t.Fatalf("active after switch: %q", active)
	}
}
