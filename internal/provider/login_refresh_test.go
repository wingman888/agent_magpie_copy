package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func codexTokens(t *testing.T, path string) map[string]any {
	t.Helper()
	return readJSONFile(t, path)["tokens"].(map[string]any)
}

// The account the agent is signed in to keeps no refresh token in magpie's
// copy; switching away saves it whole, and switching back strips it again.
func TestRefreshTokenHasOneHolder(t *testing.T) {
	home := loginHome(t)
	auth := filepath.Join(home, ".codex", "auth.json")
	codexSignIn(t, home, "a@example.com", "ra")
	Accounts()
	afile := rememberedFile(t, "codex", "a@example.com")
	if _, ok := codexTokens(t, afile)["refresh_token"]; ok || codexTokens(t, afile)["access_token"] == "" {
		t.Fatalf("signed-in copy holds a refresh token: %v", codexTokens(t, afile))
	}

	keepSignIn("codex") // what magpie accounts add does before the agent signs in again
	if got := codexTokens(t, afile)["refresh_token"]; got != "ra" {
		t.Fatalf("kept copy lost its refresh token: %v", got)
	}
	codexSignIn(t, home, "b@example.com", "rb")
	Accounts()

	if err := SwitchAccount("codex", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := codexTokens(t, auth)["refresh_token"]; got != "ra" {
		t.Fatalf("agent got refresh token %v", got)
	}
	if _, ok := codexTokens(t, afile)["refresh_token"]; ok {
		t.Fatal("switched-to copy still holds a refresh token")
	}
	bfile := rememberedFile(t, "codex", "b@example.com")
	if got := codexTokens(t, bfile)["refresh_token"]; got != "rb" {
		t.Fatalf("replaced account's latest refresh token not saved: %v", got)
	}

	if err := SwitchAccount("codex", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := codexTokens(t, afile)["refresh_token"]; got != "ra" {
		t.Fatalf("switched-back-from account lost its refresh token: %v", got)
	}
	if _, ok := codexTokens(t, bfile)["refresh_token"]; ok {
		t.Fatal("switched-back-to copy still holds a refresh token")
	}
	if got := codexTokens(t, auth)["refresh_token"]; got != "rb" {
		t.Fatalf("agent got refresh token %v", got)
	}
}

// A copy whose refresh token was taken by a sign-in outside magpie cannot be
// switched to: writing it would leave the agent unable to refresh.
func TestSwitchRefusesCopyWithoutRefreshToken(t *testing.T) {
	home := loginHome(t)
	auth := filepath.Join(home, ".codex", "auth.json")
	codexSignIn(t, home, "a@example.com", "ra")
	Accounts()
	codexSignIn(t, home, "b@example.com", "rb") // signed in outside magpie
	Accounts()

	err := SwitchAccount("codex", "a@example.com")
	if err == nil || !strings.Contains(err.Error(), "no refresh token") {
		t.Fatalf("switch to a copy without a refresh token: %v", err)
	}
	if got := codexTokens(t, auth)["refresh_token"]; got != "rb" {
		t.Fatalf("agent sign-in changed by a refused switch: %v", got)
	}
}

// Two accounts that show the same stand-in name (no email) are never merged:
// the second one does not overwrite the first, and is not switched to.
func TestStandInNamesAreNotMerged(t *testing.T) {
	home := loginHome(t)
	claudeLoginFile(t, home, "max", "acc-1", "refresh-1", "mcp")
	Accounts()
	claudeLoginFile(t, home, "max", "acc-2", "refresh-2", "mcp") // another account, same name
	Accounts()

	saved := readJSONFile(t, rememberedFile(t, "claude", "Claude Max"))
	if got := saved["claudeAiOauth"].(map[string]any)["accessToken"]; got != "acc-1" {
		t.Fatalf("the first account was overwritten by a different one: %v", got)
	}
	if err := SwitchAccount("claude", "Claude Max"); err == nil {
		t.Fatal("switched on an account whose identity is ambiguous")
	}
	if got := readJSONFile(t, filepath.Join(home, ".claude", ".credentials.json")); got["claudeAiOauth"].(map[string]any)["accessToken"] != "acc-2" {
		t.Fatalf("agent sign-in changed by a refused switch: %v", got)
	}
}

// Fields the agent keeps inside claudeAiOauth that are not login fields stay.
// The copy magpie keeps for the signed-in account has no refresh token.
func TestClaudeSwitchKeepsAgentFieldsInOAuthBlock(t *testing.T) {
	home := loginHome(t)
	credPath := filepath.Join(home, ".claude", ".credentials.json")
	claudeLoginFile(t, home, "pro", "acc-pro", "refresh-pro-1", "x")
	Accounts()
	keepSignIn("claude")
	writeFile(t, credPath, map[string]any{"claudeAiOauth": map[string]any{"accessToken": "acc-max",
		"refreshToken": "refresh-max-1", "expiresAt": float64(4102444800000), "subscriptionType": "max",
		"agentPref": "keep"}})
	forgetClaudeCredential()
	Accounts()

	maxCopy := readJSONFile(t, rememberedFile(t, "claude", "Claude Max"))["claudeAiOauth"].(map[string]any)
	if _, ok := maxCopy["refreshToken"]; ok {
		t.Fatalf("signed-in copy holds a refresh token: %v", maxCopy)
	}

	if err := SwitchAccount("claude", "Claude Pro"); err != nil {
		t.Fatal(err)
	}
	oauth := readJSONFile(t, credPath)["claudeAiOauth"].(map[string]any)
	if oauth["agentPref"] != "keep" || oauth["accessToken"] != "acc-pro" || oauth["refreshToken"] != "refresh-pro-1" {
		t.Fatalf("claudeAiOauth after switch: %v", oauth)
	}
}

// A Codex sign-in without an email is keyed by its account id.
func TestCodexWithoutEmailIsKeyedByAccountID(t *testing.T) {
	home := loginHome(t)
	signIn := func(acct, refresh string) {
		writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{"auth_mode": "chatgpt",
			"tokens": map[string]any{
				"id_token":      fakeJWT(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}}),
				"access_token":  codexAccess(acct),
				"refresh_token": refresh,
				"account_id":    acct,
			}})
	}
	signIn("acct-1", "r1")
	Accounts()
	signIn("acct-2", "r2")
	Accounts()

	list := Remembered("codex")
	if len(list) != 2 || list[0].User == list[1].User {
		t.Fatalf("accounts without email merged: %+v", list)
	}
	if got := codexTokens(t, rememberedFile(t, "codex", "ChatGPT acct-1"))["access_token"]; got != codexAccess("acct-1") {
		t.Fatalf("first account's copy: %v", got)
	}
}

// A Codex auth.json that cannot be read is not overwritten by a switch.
func TestSwitchDoesNotOverwriteUnreadableCodexFile(t *testing.T) {
	home := loginHome(t)
	auth := filepath.Join(home, ".codex", "auth.json")
	codexSignIn(t, home, "a@example.com", "ra")
	Accounts()
	keepSignIn("codex")
	codexSignIn(t, home, "b@example.com", "rb")
	Accounts()
	if err := os.WriteFile(auth, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SwitchAccount("codex", "a@example.com"); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("switch over an unreadable file: %v", err)
	}
	if b, _ := os.ReadFile(auth); string(b) != "{not json" {
		t.Fatalf("unreadable agent file was overwritten: %q", b)
	}
}
