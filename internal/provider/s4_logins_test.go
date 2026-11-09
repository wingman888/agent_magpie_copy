package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// s4Home isolates HOME, the magpie config dir and Claude Code's config dir in
// a temp directory, and returns the home.
func s4Home(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	return home
}

func s4WriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
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

// s4JWT is an unsigned token whose payload is the given claims.
func s4JWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// s4Codex writes a ChatGPT sign-in for email into auth.json, with access token
// tok. The file carries a field that is not a sign-in, to see it survive.
func s4Codex(t *testing.T, auth, email, tok string) {
	t.Helper()
	s4WriteJSON(t, auth, map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"custom":         "keep",
		"last_refresh":   "2026-01-01T00:00:00Z",
		"tokens": map[string]any{
			"id_token":      s4JWT(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}}),
			"access_token":  tok,
			"refresh_token": "r-" + tok,
			"account_id":    "acct-" + tok,
		},
	})
}

// s4Claude writes a Claude Code login (access token tok) into the credentials
// file, next to an MCP OAuth entry that is not part of the login.
func s4Claude(t *testing.T, cred, tok string) {
	t.Helper()
	s4WriteJSON(t, cred, map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":      tok,
			"refreshToken":     "r-" + tok,
			"expiresAt":        time.Now().Add(time.Hour).UnixMilli(),
			"subscriptionType": "pro",
		},
	})
}

// s4SignInAs makes the next agent sign-in produce the given Codex login.
func s4SignInAs(t *testing.T, auth, email, tok string) {
	t.Helper()
	old := agentLogin
	agentLogin = func(string) error {
		s4Codex(t, auth, email, tok)
		return nil
	}
	t.Cleanup(func() { agentLogin = old })
}

func s4Users(ls []Login) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.User)
	}
	return out
}

func s4Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestS4LoginAgentNames(t *testing.T) {
	for in, want := range map[string]string{"claude": "claude", "CC": "claude", "codex": "codex", " Codex ": "codex"} {
		if got, ok := LoginAgent(in); !ok || got != want {
			t.Errorf("LoginAgent(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := LoginAgent("copilot"); ok {
		t.Error("copilot is not a sign-in magpie switches")
	}
	if _, err := SwitchLogin("gemini", "x@example.com"); err == nil {
		t.Error("switching an unknown agent must fail")
	}
}

func TestS4CodexAddSwitchForget(t *testing.T) {
	home := s4Home(t)
	auth := filepath.Join(home, ".codex", "auth.json")
	s4Codex(t, auth, "a@example.com", "tok-a")
	ls := Logins("codex")
	if len(ls) != 1 || !ls[0].Current || ls[0].User != "a@example.com" {
		t.Fatalf("remembered sign-in: %+v", ls)
	}

	// a second sign-in, made through magpie, is on at the same time by default
	s4SignInAs(t, auth, "b@example.com", "tok-b")
	user, err := AddLogin("codex")
	if err != nil || user != "b@example.com" {
		t.Fatalf("AddLogin = %q, %v", user, err)
	}
	ls = Logins("codex")
	if got := s4Users(ls); len(got) != 2 || got[0] != "b@example.com" || got[1] != "a@example.com" {
		t.Fatalf("after add: %v", got)
	}
	if !ls[0].Current || !ls[0].On || ls[1].On {
		t.Fatalf("on flags after add: %+v", ls)
	}
	if !s4Exists(loginTokens("codex", "a@example.com")) {
		t.Fatal("the earlier sign-in was not kept")
	}
	if s4Exists(loginDir("codex", "b@example.com")) {
		t.Fatal("the signed-in login must have one holder: the agent's own file")
	}

	// the agent refreshes its own tokens; the newest must be what is kept
	s4Codex(t, auth, "b@example.com", "tok-b2")
	changed, err := SwitchLogin("codex", "A@Example.com")
	if err != nil || !changed {
		t.Fatalf("SwitchLogin = %v, %v", changed, err)
	}
	var raw map[string]any
	b, _ := os.ReadFile(auth)
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	tokens, _ := raw["tokens"].(map[string]any)
	if tokens["access_token"] != "tok-a" || tokens["account_id"] != "acct-tok-a" {
		t.Fatalf("agent file after switch: %v", tokens)
	}
	if raw["custom"] != "keep" || raw["last_refresh"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("switch changed other fields: %v", raw)
	}
	if _, ok := raw["OPENAI_API_KEY"]; !ok {
		t.Fatal("switch dropped OPENAI_API_KEY")
	}
	saved, _ := os.ReadFile(loginTokens("codex", "b@example.com"))
	if !strings.Contains(string(saved), "tok-b2") {
		t.Fatalf("outgoing login lost its newest tokens: %s", saved)
	}
	if s4Exists(loginDir("codex", "a@example.com")) {
		t.Fatal("the incoming login must have one holder: the agent's own file")
	}
	ls = Logins("codex")
	if got := s4Users(ls); got[0] != "a@example.com" || !ls[0].Current || ls[1].On {
		t.Fatalf("after switch: %+v", ls)
	}

	if err := ForgetLogin("codex", "a@example.com"); err == nil {
		t.Fatal("forgetting the login in use must be refused")
	}
	if err := ForgetLogin("codex", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	if s4Exists(loginDir("codex", "b@example.com")) {
		t.Fatal("forgotten login kept its tokens")
	}
	if got := s4Users(Logins("codex")); len(got) != 1 || got[0] != "a@example.com" {
		t.Fatalf("after forget: %v", got)
	}
	if _, err := SwitchLogin("codex", "nobody@example.com"); err == nil {
		t.Fatal("switching to an unknown login must fail")
	}
}

func TestS4SetLoginOnRefusesCurrentAndOrdersCandidates(t *testing.T) {
	home := s4Home(t)
	auth := filepath.Join(home, ".codex", "auth.json")
	s4Codex(t, auth, "a@example.com", "tok-a")
	Logins("codex")
	s4SignInAs(t, auth, "b@example.com", "tok-b")
	if _, err := AddLogin("codex"); err != nil {
		t.Fatal(err)
	}
	s4SignInAs(t, auth, "c@example.com", "tok-c")
	if _, err := AddLogin("codex"); err != nil {
		t.Fatal(err)
	}
	// now c is in use; saved order is a, b. Turn a on; b is already on.
	if err := SetLoginOn("codex", "c@example.com", false); err == nil {
		t.Fatal("the login in use must stay on")
	}
	if err := SetLoginOn("codex", "nobody@example.com", true); err == nil {
		t.Fatal("an unknown login must be refused")
	}
	if err := SetLoginOn("gemini", "a@example.com", true); err == nil {
		t.Fatal("an unknown agent must be refused")
	}
	if err := SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}

	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	cands := p.KeyCandidates(Responses, "gpt-5.5")
	var users []string
	for _, c := range cands {
		users = append(users, c.Account.User)
	}
	if strings.Join(users, ",") != "c@example.com,a@example.com,b@example.com" {
		t.Fatalf("candidate order: %v", users)
	}
	want := []string{"Bearer tok-c", "Bearer tok-a", "Bearer tok-b"}
	for i, c := range cands {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.com/v1", nil)
		if err := c.Sign(context.Background(), req, Responses, nil); err != nil {
			t.Fatal(err)
		}
		if got := req.Header.Get("Authorization"); got != want[i] {
			t.Errorf("candidate %d signed with %q, want %q", i, got, want[i])
		}
	}
}

func TestS4ClaudeSwitchKeepsOtherFields(t *testing.T) {
	home := s4Home(t)
	dir := filepath.Join(home, ".claude")
	cred := filepath.Join(dir, ".credentials.json")
	// `claude auth status` stands in: it reports whichever login the file holds
	script := filepath.Join(home, "claude")
	body := "#!/bin/sh\nif grep -q tok-b \"$CLAUDE_CONFIG_DIR/.credentials.json\"; then echo '{\"loggedIn\":true,\"email\":\"b@example.com\"}'; else echo '{\"loggedIn\":true,\"email\":\"a@example.com\"}'; fi\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeExecutable = func() string { return script }

	s4Claude(t, cred, "tok-a")
	if got := s4Users(Logins("claude")); len(got) != 1 || got[0] != "a@example.com" {
		t.Fatalf("remembered: %v", got)
	}
	old := agentLogin
	agentLogin = func(string) error {
		s4Claude(t, cred, "tok-b")
		return nil
	}
	t.Cleanup(func() { agentLogin = old })
	if user, err := AddLogin("claude"); err != nil || user != "b@example.com" {
		t.Fatalf("AddLogin = %q, %v", user, err)
	}
	// Claude Code rotated its token, and the agent's file also holds MCP
	// state, which a switch must leave alone
	s4Claude(t, cred, "tok-b2")
	var mine map[string]any
	b, _ := os.ReadFile(cred)
	if err := json.Unmarshal(b, &mine); err != nil {
		t.Fatal(err)
	}
	mine["mcpOAuth"] = map[string]any{"srv": map[string]any{"token": "m1"}}
	s4WriteJSON(t, cred, mine)

	if changed, err := SwitchLogin("claude", "A@example.com"); err != nil || !changed {
		t.Fatalf("SwitchLogin = %v, %v", changed, err)
	}
	var raw map[string]any
	b, _ = os.ReadFile(cred)
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	oauth, _ := raw["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "tok-a" {
		t.Fatalf("login after switch: %v", oauth)
	}
	mcp, _ := raw["mcpOAuth"].(map[string]any)
	if mcp == nil || mcp["srv"] == nil {
		t.Fatalf("switch dropped mcpOAuth: %v", raw)
	}
	saved, _ := os.ReadFile(loginTokens("claude", "b@example.com"))
	if !strings.Contains(string(saved), "tok-b2") {
		t.Fatalf("outgoing Claude login lost its newest tokens: %s", saved)
	}
	if got := s4Users(Logins("claude")); got[0] != "a@example.com" {
		t.Fatalf("after switch: %v", got)
	}

	if err := ForgetLogin("claude", "a@example.com"); err == nil {
		t.Fatal("forgetting the Claude login in use must be refused")
	}
	if err := SetLoginOn("claude", "b@example.com", true); err != nil {
		t.Fatal(err)
	}
	p, err := Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	cands := p.KeyCandidates(Anthropic, "claude-sonnet-5-5")
	if len(cands) != 2 || cands[1].Account.User != "b@example.com" {
		t.Fatalf("claude candidates: %d", len(cands))
	}
	if cands[1].Account.ConfigDir != loginDir("claude", "b@example.com") {
		t.Fatalf("saved Claude login runs from %q", cands[1].Account.ConfigDir)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.com/v1", nil)
	if err := cands[1].Sign(context.Background(), req, Anthropic, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok-b2" {
		t.Fatalf("saved Claude login signed with %q", got)
	}
	if strings.Contains(readFileOr(cred), "tok-b") {
		t.Fatal("a login magpie keeps leaked into the agent's own credentials")
	}
	if err := ForgetLogin("claude", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	if s4Exists(loginDir("claude", "b@example.com")) {
		t.Fatal("forgotten Claude login kept its tokens")
	}
}

func readFileOr(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func TestS4AddFailureRestoresEarlierLogin(t *testing.T) {
	home := s4Home(t)
	auth := filepath.Join(home, ".codex", "auth.json")
	s4Codex(t, auth, "a@example.com", "tok-a")
	Logins("codex")
	old := agentLogin
	agentLogin = func(string) error {
		os.Remove(auth)
		return errors.New("sign-in cancelled")
	}
	t.Cleanup(func() { agentLogin = old })
	if _, err := AddLogin("codex"); err == nil {
		t.Fatal("a sign-in that does not finish must report it")
	}
	var raw map[string]any
	b, _ := os.ReadFile(auth)
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("earlier login not restored: %v", err)
	}
	tokens, _ := raw["tokens"].(map[string]any)
	if tokens["access_token"] != "tok-a" {
		t.Fatalf("restored login: %v", tokens)
	}
	if s4Exists(loginDir("codex", "a@example.com")) {
		t.Fatal("restored login must have one holder: the agent's own file")
	}
}
