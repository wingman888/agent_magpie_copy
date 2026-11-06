package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// codexFile is a Codex auth.json for one ChatGPT account.
func codexFile(email, acct, access string) map[string]any {
	return map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token": fakeJWT(map[string]any{"email": email,
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": acct}}),
			"access_token": access, "refresh_token": "r-" + acct, "account_id": acct,
		},
	}
}

// freshAccess is an access token valid for an hour, unique per account.
func freshAccess(acct string) string {
	return fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix()), "sub": acct})
}

func readMap(t *testing.T, path string) map[string]any {
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

func tokenOf(m map[string]any, key string) string {
	t, _ := m["tokens"].(map[string]any)
	s, _ := t[key].(string)
	return s
}

// resetLoginUsage forgets cached quotas; tests share the process.
func resetLoginUsage() {
	loginUsage.Lock()
	loginUsage.at, loginUsage.data = nil, nil
	loginUsage.Unlock()
}

// rememberAlt signs the codex agent in to a second account through the
// (stubbed) login command, so that magpie remembers it. The first account
// goes into the store with its tokens. It returns the second account's
// access token.
func rememberAlt(t *testing.T, home string) string {
	t.Helper()
	authPath := filepath.Join(home, ".codex", "auth.json")
	alt := filepath.Join(home, "alt-auth.json")
	access := freshAccess("acct-2")
	writeFile(t, alt, codexFile("alt@example.com", "acct-2", access))
	old := loginCommand
	loginCommand = func(string) (*exec.Cmd, error) { return exec.Command("cp", alt, authPath), nil }
	t.Cleanup(func() { loginCommand = old })
	user, err := AddLogin("codex")
	if err != nil || user != "alt@example.com" {
		t.Fatalf("AddLogin = %q, %v", user, err)
	}
	return access
}

func TestRememberedCodexSwitchAndForget(t *testing.T) {
	home := signIn(t)
	authPath := filepath.Join(home, ".codex", "auth.json")
	meAccess := tokenOf(readMap(t, authPath), "access_token")

	altAccess := rememberAlt(t, home)

	logins := Logins("codex")
	if len(logins) != 2 {
		t.Fatalf("logins = %+v, want two", logins)
	}
	if logins[0].User != "me@example.com" || logins[0].Current || logins[0].On || logins[0].NeedsLogin {
		t.Fatalf("outgoing account = %+v", logins[0])
	}
	if logins[1].User != "alt@example.com" || !logins[1].Current || !logins[1].On {
		t.Fatalf("signed-in account = %+v", logins[1])
	}
	// the outgoing tokens were moved out of the agent's file into the store
	if tokenOf(readMap(t, authPath), "access_token") != altAccess {
		t.Fatal("agent does not hold the signed-in account's tokens")
	}
	if blob, ok := storedBlob("codex", "me@example.com"); !ok || !strings.Contains(string(blob), meAccess) {
		t.Fatalf("outgoing tokens not kept: %s", blob)
	}

	// a field magpie does not own must survive the switch
	raw := readMap(t, authPath)
	raw["extra"] = "keep"
	writeFile(t, authPath, raw)

	// switching back: the agent gets me's tokens, the replaced account keeps alt's
	if err := SwitchLogin("codex", "ME@example.com"); err != nil {
		t.Fatal(err)
	}
	after := readMap(t, authPath)
	if tokenOf(after, "access_token") != meAccess || after["extra"] != "keep" || after["auth_mode"] != "chatgpt" {
		t.Fatalf("auth.json after switch: %v", after)
	}
	if blob, ok := storedBlob("codex", "alt@example.com"); !ok || !strings.Contains(string(blob), altAccess) {
		t.Fatalf("replaced account lost its tokens: %s", blob)
	}
	logins = Logins("codex")
	if !logins[0].Current || logins[1].Current || logins[1].NeedsLogin || logins[1].On {
		t.Fatalf("after switch: %+v", logins)
	}
	// the replaced account inherited me's state (off)

	// on/off rules
	if err := SetLoginOn("codex", "me@example.com", false); err == nil {
		t.Fatal("turning off the signed-in account was allowed")
	}
	if err := SetLoginOn("codex", "nobody@example.com", true); err == nil {
		t.Fatal("unknown account accepted")
	}
	if err := SetLoginOn("codex", "alt@example.com", true); err != nil {
		t.Fatal(err)
	}

	// candidates: the signed-in account first, then the other one that is on
	p, ok := find(All(), "codex")
	if !ok {
		t.Fatal("codex not listed")
	}
	cands := p.Candidates(Responses, "gpt-5.5")
	if len(cands) != 2 || cands[0].ID != "codex" || cands[1].Account == nil || cands[1].Account.User != "alt@example.com" {
		t.Fatalf("candidates = %+v", cands)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://example.test/responses", nil)
	if err := cands[1].Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer "+altAccess || req.Header.Get("chatgpt-account-id") != "acct-2" {
		t.Fatalf("second candidate signed with %v", req.Header)
	}

	// refusals
	if err := ForgetLogin("codex", "me@example.com"); err == nil {
		t.Fatal("forgot the account signed in to")
	}
	if err := SwitchLogin("codex", "nobody@example.com"); err == nil {
		t.Fatal("switched to an unknown account")
	}
	if _, ok := AgentID("gemini"); ok {
		t.Fatal("unknown agent accepted")
	}
	if id, ok := AgentID("cc"); !ok || id != "claude" {
		t.Fatalf("cc alias = %q, %v", id, ok)
	}
	if err := ForgetLogin("codex", "alt@example.com"); err != nil {
		t.Fatal(err)
	}
	if n := len(Logins("codex")); n != 1 {
		t.Fatalf("after forget: %d logins", n)
	}
	info, err := os.Stat(accountsPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("accounts.json mode %v", info.Mode().Perm())
	}
}

func TestRememberedCodexUsagePerAccount(t *testing.T) {
	home := signIn(t)
	rememberAlt(t, home)
	resetLoginUsage()
	t.Cleanup(resetLoginUsage)

	var mu sync.Mutex
	hits := map[string]int{}
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acct := r.Header.Get("chatgpt-account-id")
		mu.Lock()
		hits[acct]++
		failing := fail && acct == "acct-2"
		mu.Unlock()
		if failing {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		used := map[string]int{"acct-1": 42, "acct-2": 7}[acct]
		w.Header().Set("Content-Type", "application/json")
		body := `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":` + strconv.Itoa(used) +
			`,"limit_window_seconds":18000,"reset_after_seconds":100},"secondary_window":{"used_percent":` + strconv.Itoa(used/2) +
			`,"limit_window_seconds":604800,"reset_after_seconds":100}}}`
		w.Write([]byte(body))
	}))
	defer srv.Close()
	oldBase := CodexBase
	CodexBase = srv.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase })

	ctx := context.Background()
	got := LoginUsage(ctx, "codex")
	if len(got) != 2 {
		t.Fatalf("usage = %+v", got)
	}
	if q := got["me@example.com"]; q.Error != "" || len(q.Windows) != 2 || q.Windows[0].Used != 42 || q.Windows[0].Name != "5 hours" {
		t.Fatalf("me = %+v", q)
	}
	if q := got["alt@example.com"]; q.Error != "" || q.Windows[0].Used != 7 {
		t.Fatalf("alt = %+v", q)
	}
	total := hits["acct-1"] + hits["acct-2"]

	// reused within a minute
	LoginUsage(ctx, "codex")
	if n := hits["acct-1"] + hits["acct-2"]; n != total {
		t.Fatalf("cached call asked the vendor again: %d -> %d", total, n)
	}

	// after the minute, a failing account keeps its last known result
	loginUsage.Lock()
	loginUsage.at["codex"] = time.Now().Add(-2 * time.Minute)
	loginUsage.Unlock()
	mu.Lock()
	fail = true
	mu.Unlock()
	got = LoginUsage(ctx, "codex")
	if q := got["alt@example.com"]; q.Error != "" || len(q.Windows) != 2 || q.Windows[0].Used != 7 {
		t.Fatalf("failed query lost the known result: %+v", q)
	}
	if q := got["me@example.com"]; q.Windows[0].Used != 42 {
		t.Fatalf("me = %+v", q)
	}

	if n := len(LoginUsage(ctx, "copilot")); n != 0 {
		t.Fatalf("unsupported agent returned %d entries", n)
	}
}

func TestRememberedClaudeSwitchKeepsConfig(t *testing.T) {
	home := claudeHome(t)
	credsPath := claudeSignIn(t, home, time.Now().Add(time.Hour))
	statusPath := filepath.Join(home, "status.json")
	writeFile(t, statusPath, map[string]any{"loggedIn": true, "email": "me@example.com", "subscriptionType": "max"})
	exe := filepath.Join(home, "claude")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\ncat '"+statusPath+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeExecutable = func() string { return exe }
	forgetClaudeStatus()

	// the signed-in account is remembered first, so it is listed first
	rememberLogins()
	// a second account magpie holds the tokens of
	alt := map[string]any{"accessToken": "sk-ant-oat01-alt", "refreshToken": "sk-ant-ort01-alt",
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "pro"}
	ab, _ := json.Marshal(alt)
	if err := editAccounts(func(f *accountFile) (bool, error) {
		f.Accounts = append(f.Accounts, storedAccount{Agent: "claude", User: "alt@example.com", Plan: "pro", Creds: ab})
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	if logins := Logins("claude"); len(logins) != 2 || !logins[0].Current {
		t.Fatalf("logins = %+v", logins)
	}
	if err := SwitchLogin("claude", "alt@example.com"); err != nil {
		t.Fatal(err)
	}
	raw := readMap(t, credsPath)
	oauth, _ := raw["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "sk-ant-oat01-alt" {
		t.Fatalf("credentials not switched: %v", oauth)
	}
	if raw["mcpOAuth"] == nil {
		t.Fatal("switch dropped the MCP OAuth state")
	}
	if blob, ok := storedBlob("claude", "me@example.com"); !ok || !strings.Contains(string(blob), "sk-ant-oat01-old") {
		t.Fatalf("outgoing tokens not kept: %s", blob)
	}

	// Claude Code now reports the new account
	writeFile(t, statusPath, map[string]any{"loggedIn": true, "email": "alt@example.com", "subscriptionType": "pro"})
	forgetClaudeStatus()
	logins := Logins("claude")
	if len(logins) != 2 || logins[0].Current || !logins[1].Current {
		t.Fatalf("after switch: %+v", logins)
	}
	if err := ForgetLogin("claude", "alt@example.com"); err == nil {
		t.Fatal("forgot the signed-in Claude account")
	}
	if err := ForgetLogin("claude", "me@example.com"); err != nil {
		t.Fatal(err)
	}
}
