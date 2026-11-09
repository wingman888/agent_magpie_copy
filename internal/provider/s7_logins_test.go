package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// s7ClaudeWorld isolates the home and points `claude auth status` at a
// stand-in that reports whichever login the credentials file holds. It returns
// the credentials path.
func s7ClaudeWorld(t *testing.T) string {
	t.Helper()
	home := s4Home(t)
	cred := filepath.Join(home, ".claude", ".credentials.json")
	script := filepath.Join(home, "claude")
	body := "#!/bin/sh\nif grep -q tok-b \"$CLAUDE_CONFIG_DIR/.credentials.json\"; then echo '{\"loggedIn\":true,\"email\":\"b@example.com\"}'; else echo '{\"loggedIn\":true,\"email\":\"a@example.com\"}'; fi\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	old := claudeExecutable
	claudeExecutable = func() string { return script }
	t.Cleanup(func() { claudeExecutable = old })
	return cred
}

// s7ClaudeElsewhere is a sign-in made outside magpie: the agent's file now
// holds tok, next to an MCP OAuth entry that is not part of the login.
func s7ClaudeElsewhere(t *testing.T, cred, tok string) {
	t.Helper()
	s4Claude(t, cred, tok)
	var raw map[string]any
	b, err := os.ReadFile(cred)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	raw["mcpOAuth"] = map[string]any{"srv": map[string]any{"token": "m1"}}
	s4WriteJSON(t, cred, raw)
	forgetClaudeCredential()
	forgetClaudeStatus()
}

func s7ReadJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	var raw map[string]any
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// A login remembered before the agent signed in elsewhere can still be
// switched back to: magpie kept a copy of its tokens while it was current.
func TestS7SwitchBackToLoginSignedInElsewhere(t *testing.T) {
	cred := s7ClaudeWorld(t)
	s4Claude(t, cred, "tok-a")
	if got := s4Users(Logins("claude")); len(got) != 1 || got[0] != "a@example.com" {
		t.Fatalf("remembered: %v", got)
	}
	// signed in as b outside magpie, and Claude Code rotates b's token
	s7ClaudeElsewhere(t, cred, "tok-b")
	s7ClaudeElsewhere(t, cred, "tok-b2")
	if got := s4Users(Logins("claude")); len(got) != 2 {
		t.Fatalf("after the outside sign-in: %v", got)
	}

	changed, err := SwitchLogin("claude", "A@example.com")
	if err != nil || !changed {
		t.Fatalf("SwitchLogin to a = %v, %v", changed, err)
	}
	raw := s7ReadJSON(t, cred)
	oauth, _ := raw["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "tok-a" {
		t.Fatalf("agent login after switch: %v", oauth)
	}
	mcp, _ := raw["mcpOAuth"].(map[string]any)
	if mcp == nil || mcp["srv"] == nil {
		t.Fatalf("switch dropped mcpOAuth: %v", raw)
	}
	if len(raw) != 2 {
		t.Fatalf("switch changed other top-level fields: %v", raw)
	}
	saved, _ := os.ReadFile(rsiLoginTokens("claude", "b@example.com"))
	if !strings.Contains(string(saved), "tok-b2") {
		t.Fatalf("b lost its newest tokens: %s", saved)
	}
	if s4Exists(rsiLoginDir("claude", "a@example.com")) {
		t.Fatal("the incoming login must have one holder: the agent's own file")
	}
}

// A login switched away from keeps its copy, and a copy turned on at the same
// time as the signed-in login is used as a candidate.
func TestS7SavedCopyServesAsAlternate(t *testing.T) {
	cred := s7ClaudeWorld(t)
	s4Claude(t, cred, "tok-a")
	Logins("claude")
	s7ClaudeElsewhere(t, cred, "tok-b")
	Logins("claude")

	if err := SetLoginOn("claude", "a@example.com", true); err != nil {
		t.Fatalf("SetLoginOn a on: %v", err)
	}
	cur, ok := claudeAccount()
	if !ok {
		t.Fatal("no Claude login in use")
	}
	if cur.Account.User != "b@example.com" {
		t.Fatalf("in use: %s", cur.Account.User)
	}
	if len(cur.Account.also) != 1 || cur.Account.also[0].Account.User != "a@example.com" {
		t.Fatalf("alternates: %+v", cur.Account.also)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.com/v1", nil)
	if err := cur.Account.also[0].Sign(context.Background(), req, Anthropic, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok-a" {
		t.Fatalf("alternate signed with %q", got)
	}
}

// Turning on a saved login that has no tokens is refused, not skipped later.
func TestS7SetLoginOnNeedsSavedSignIn(t *testing.T) {
	cred := s7ClaudeWorld(t)
	s4Claude(t, cred, "tok-a")
	Logins("claude")
	s7ClaudeElsewhere(t, cred, "tok-b")
	Logins("claude")
	if err := os.Remove(rsiLoginTokens("claude", "a@example.com")); err != nil {
		t.Fatal(err)
	}
	err := SetLoginOn("claude", "a@example.com", true)
	if err == nil || !strings.Contains(err.Error(), "no saved sign-in for a@example.com") {
		t.Fatalf("SetLoginOn without tokens = %v", err)
	}
}

// Codex: the login is remembered by SwitchLogin itself when it is the one the
// agent is signed in to, so a sign-in made outside magpie is never lost.
func TestS7CodexSwitchBackAfterSignInElsewhere(t *testing.T) {
	home := s4Home(t)
	auth := filepath.Join(home, ".codex", "auth.json")
	s4Codex(t, auth, "a@example.com", "tok-a")
	Logins("codex")
	s4Codex(t, auth, "b@example.com", "tok-b")

	changed, err := SwitchLogin("codex", "a@example.com")
	if err != nil || !changed {
		t.Fatalf("SwitchLogin = %v, %v", changed, err)
	}
	raw := s7ReadJSON(t, auth)
	tokens, _ := raw["tokens"].(map[string]any)
	if tokens["access_token"] != "tok-a" {
		t.Fatalf("agent login after switch: %v", tokens)
	}
	if raw["custom"] != "keep" {
		t.Fatalf("switch changed other fields: %v", raw)
	}
	saved, _ := os.ReadFile(rsiLoginTokens("codex", "b@example.com"))
	if !strings.Contains(string(saved), "tok-b") {
		t.Fatalf("b was not kept: %s", saved)
	}
}
