package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// msAcctEnv points HOME, magpie's config and Claude Code's config dir at a
// fresh temp dir. Within it tok-alice belongs to alice@x.com and tok-bob to
// bob@x.com. It returns the home dir and Claude Code's config dir.
func msAcctEnv(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "claude")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	oldKeychain, oldExe, oldIdentity, oldLogin, oldURL := claudeKeychain, claudeExecutable, claudeIdentityOf, LoginRunner, claudeUsageURL
	claudeKeychain = false
	claudeExecutable = func() string { return "" }
	claudeIdentityOf = func() (string, string, bool) {
		c, _, ok := readClaudeCredential()
		if !ok {
			return "", "", true
		}
		switch c.OAuth.AccessToken {
		case "tok-alice":
			return "alice@x.com", "max", false
		case "tok-bob":
			return "bob@x.com", "pro", false
		}
		return "", "", false
	}
	t.Cleanup(func() {
		claudeKeychain, claudeExecutable, claudeIdentityOf, LoginRunner, claudeUsageURL = oldKeychain, oldExe, oldIdentity, oldLogin, oldURL
		forgetLiveCaches()
	})
	forgetLiveCaches()
	loginUsageMu.Lock()
	loginUsageCache = map[string]loginUsageEntry{}
	loginUsageMu.Unlock()
	return home, dir
}

func msAcctCredFile(dir string) string { return filepath.Join(dir, ".credentials.json") }

// msAcctWrite writes v to path as the agent would keep it, then forgets what
// was cached about the agent's sign-in.
func msAcctWrite(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSecretFile(path, b); err != nil {
		t.Fatal(err)
	}
	forgetLiveCaches()
}

func msAcctRead(t *testing.T, path string) map[string]any {
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

func msAcctClaudeLogin(token, refresh string) map[string]any {
	return map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": token, "refreshToken": refresh,
			"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max"},
	}
}

// msAcctClaudeField reads one key of the claudeAiOauth object in a blob.
func msAcctClaudeField(m map[string]any, key string) string {
	o, _ := m["claudeAiOauth"].(map[string]any)
	s, _ := o[key].(string)
	return s
}

func msAcctJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

// msAcctCodexLogin is a whole Codex auth file for email, tagged so tokens can be told apart.
func msAcctCodexLogin(email, tag string) map[string]any {
	exp := time.Now().Add(time.Hour).Unix()
	return map[string]any{
		"auth_mode":      "chatgpt",
		"last_refresh":   "2026-01-01T00:00:00Z",
		"OPENAI_API_KEY": "keep-me",
		"tokens": map[string]any{
			"id_token":      msAcctJWT(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}}),
			"access_token":  msAcctJWT(map[string]any{"exp": exp, "sub": tag}),
			"refresh_token": "rt-" + tag,
			"account_id":    "acct-" + tag,
		},
	}
}

// msAcctNextLogin makes the agent's own sign-in, when magpie runs it, leave blob behind.
func msAcctNextLogin(t *testing.T, agent, path string, blob map[string]any) {
	t.Helper()
	LoginRunner = func(got string) error {
		if got != agent {
			return errors.New("unexpected login for " + got)
		}
		msAcctWrite(t, path, blob)
		return nil
	}
}

func msAcctFind(list []RememberedAccount, user string) (RememberedAccount, bool) {
	for _, a := range list {
		if strings.EqualFold(a.User, user) {
			return a, true
		}
	}
	return RememberedAccount{}, false
}

// msAcctTwoClaude leaves alice signed in, with bob signed in through magpie
// beside her, so alice is remembered and answers requests too.
func msAcctTwoClaude(t *testing.T, dir string) {
	t.Helper()
	alice := msAcctClaudeLogin("tok-alice", "rt-alice")
	alice["mcpOAuth"] = map[string]any{"server": "alice-mcp"}
	msAcctWrite(t, msAcctCredFile(dir), alice)
	bob := msAcctClaudeLogin("tok-bob", "rt-bob")
	bob["mcpOAuth"] = map[string]any{"server": "bob-mcp"}
	msAcctNextLogin(t, "claude", msAcctCredFile(dir), bob)
	if _, err := AddAccount("claude"); err != nil {
		t.Fatal(err)
	}
}

func TestMSAcctRememberSwitchAndForget(t *testing.T) {
	_, dir := msAcctEnv(t)
	msAcctWrite(t, msAcctCredFile(dir), msAcctClaudeLogin("tok-alice", "rt-alice"))

	// an account signed in outside magpie is remembered as soon as magpie looks at providers
	Accounts()
	idx := loadAccountIndex()
	if idx.find("claude", "alice@x.com") < 0 {
		t.Fatal("the signed-in account was not remembered")
	}

	list, err := RememberedAccounts("claude")
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := msAcctFind(list, "alice@x.com"); len(list) != 1 || !ok || !a.Active || a.Plan != "max" {
		t.Fatalf("remembered = %+v, want alice active on max", list)
	}

	msAcctTwoClaude(t, dir)
	list, _ = RememberedAccounts("claude")
	bob, _ := msAcctFind(list, "bob@x.com")
	alice, _ := msAcctFind(list, "alice@x.com")
	if !bob.Active || !bob.On || alice.Active || !alice.On {
		t.Fatalf("after add: bob=%+v alice=%+v", bob, alice)
	}
	// the account that is not signed in keeps its own tokens, in a private file
	st, err := os.Stat(secretPath("claude", "alice@x.com"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode = %v, want 0600", st.Mode().Perm())
	}
	if msAcctClaudeField(mustBlob(t, secretPath("claude", "alice@x.com")), "accessToken") != "tok-alice" {
		t.Fatal("alice's tokens were not kept")
	}

	// the agent refreshes bob's sign-in before we switch away from him
	bob2 := msAcctClaudeLogin("tok-bob", "rt-bob-2")
	bob2["mcpOAuth"] = map[string]any{"server": "bob-mcp"}
	msAcctWrite(t, msAcctCredFile(dir), bob2)

	if err := SwitchAccount("claude", "ALICE@x.com"); err != nil {
		t.Fatal(err)
	}
	live := msAcctRead(t, msAcctCredFile(dir))
	if msAcctClaudeField(live, "accessToken") != "tok-alice" {
		t.Fatalf("switch did not sign alice in: %v", live)
	}
	if o, _ := live["mcpOAuth"].(map[string]any); o["server"] != "bob-mcp" {
		t.Fatalf("switch changed other fields of the agent's file: %v", live)
	}
	if msAcctClaudeField(mustBlob(t, secretPath("claude", "bob@x.com")), "refreshToken") != "rt-bob-2" {
		t.Fatal("bob's newest tokens were not kept")
	}
	if _, err := os.Stat(secretPath("claude", "alice@x.com")); err == nil {
		t.Fatal("alice's saved copy survived signing her in")
	}

	if err := ForgetAccount("claude", "alice@x.com"); err == nil {
		t.Fatal("forgot the account that is signed in")
	}
	if err := ForgetAccount("claude", "bob@x.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(secretPath("claude", "bob@x.com")); err == nil {
		t.Fatal("forgotten account still has its tokens")
	}
	if err := ForgetAccount("claude", "bob@x.com"); err == nil {
		t.Fatal("forgetting an unknown account succeeded")
	}
	if err := SwitchAccount("claude", "carol@x.com"); err == nil {
		t.Fatal("switched to an unknown account")
	}
	if err := SwitchAccount("gemini", "alice@x.com"); err == nil || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("unknown agent error = %v, want usage", err)
	}
}

func mustBlob(t *testing.T, path string) map[string]any {
	t.Helper()
	blob, ok := readSecretBlob(path)
	if !ok {
		t.Fatalf("no saved blob at %s", path)
	}
	return blob
}

func TestMSAcctSetLoginOnRules(t *testing.T) {
	_, dir := msAcctEnv(t)
	msAcctTwoClaude(t, dir)

	if err := SetLoginOn("claude", "bob@x.com", false); err == nil {
		t.Fatal("stopped the account that is signed in")
	}
	if err := SetLoginOn("claude", "nobody@x.com", true); err == nil {
		t.Fatal("switched on an unknown account")
	}
	if err := SetLoginOn("claude", "alice@x.com", false); err != nil {
		t.Fatal(err)
	}
	list, _ := RememberedAccounts("claude")
	if a, _ := msAcctFind(list, "alice@x.com"); a.On {
		t.Fatal("alice still on after being turned off")
	}
	if err := SetLoginOn("claude", "alice@x.com", true); err != nil {
		t.Fatal(err)
	}
	// a second account that answers beside the first never enters the agent's own store
	b, _ := os.ReadFile(msAcctCredFile(dir))
	if strings.Contains(string(b), "tok-alice") {
		t.Fatal("an account switched on beside the agent was written to the agent's store")
	}
	var ids []string
	for _, p := range Accounts() {
		if p.Account != nil && p.Account.Agent == "claude" {
			ids = append(ids, p.ID)
		}
	}
	if strings.Join(ids, ",") != "claude,claude-alice-x-com" {
		t.Fatalf("claude candidates = %v, want signed-in account first, then saved order", ids)
	}
}

func TestMSAcctCodexSwitchKeepsOtherFields(t *testing.T) {
	home, _ := msAcctEnv(t)
	auth := filepath.Join(home, ".codex", "auth.json")
	msAcctWrite(t, auth, msAcctCodexLogin("alice@x.com", "alice"))
	msAcctNextLogin(t, "codex", auth, msAcctCodexLogin("bob@x.com", "bob"))
	if _, err := AddAccount("codex"); err != nil {
		t.Fatal(err)
	}
	list, _ := RememberedAccounts("codex")
	if bob, _ := msAcctFind(list, "bob@x.com"); !bob.Active {
		t.Fatalf("bob not signed in after add: %+v", list)
	}
	if alice, _ := msAcctFind(list, "alice@x.com"); !alice.On {
		t.Fatalf("alice not on beside bob: %+v", list)
	}
	var ids []string
	for _, p := range Accounts() {
		if p.Account != nil && p.Account.Agent == "codex" {
			ids = append(ids, p.ID)
		}
	}
	if strings.Join(ids, ",") != "codex,codex-alice-x-com" {
		t.Fatalf("codex candidates = %v", ids)
	}

	if err := SwitchAccount("codex", "alice@x.com"); err != nil {
		t.Fatal(err)
	}
	got := msAcctRead(t, auth)
	tokens, _ := got["tokens"].(map[string]any)
	if tokens["refresh_token"] != "rt-alice" || got["OPENAI_API_KEY"] != "keep-me" {
		t.Fatalf("auth after switch = %v", got)
	}
	if mustBlob(t, secretPath("codex", "bob@x.com"))["tokens"].(map[string]any)["refresh_token"] != "rt-bob" {
		t.Fatal("bob's tokens were not kept")
	}
}

func TestMSAcctUnknownAgent(t *testing.T) {
	msAcctEnv(t)
	for _, name := range []string{"gemini", "copilot", ""} {
		if _, ok := AccountAgent(name); ok {
			t.Errorf("AccountAgent(%q) accepted", name)
		}
	}
	if a, ok := AccountAgent("CC"); !ok || a != "claude" {
		t.Fatalf("AccountAgent(CC) = %q, %v", a, ok)
	}
	if _, err := AddAccount("gemini"); err == nil || !strings.Contains(err.Error(), "claude (cc)") {
		t.Fatalf("AddAccount(gemini) = %v, want usage text", err)
	}
	if got := LoginUsage(context.Background(), "copilot"); len(got) != 0 {
		t.Fatalf("LoginUsage(copilot) = %v, want none", got)
	}
}

func TestMSAcctLoginUsageCacheAndFailure(t *testing.T) {
	_, dir := msAcctEnv(t)
	msAcctTwoClaude(t, dir)

	var hits, failBob int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		switch r.Header.Get("Authorization") {
		case "Bearer tok-alice":
			w.Write([]byte(`{"five_hour":{"utilization":10},"seven_day":{"utilization":30}}`))
		case "Bearer tok-bob":
			if atomic.LoadInt32(&failBob) == 1 {
				http.Error(w, "busy", http.StatusInternalServerError)
				return
			}
			w.Write([]byte(`{"five_hour":{"utilization":20}}`))
		default:
			http.Error(w, "unknown token", http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	claudeUsageURL = srv.URL + "/api/oauth/usage"
	ctx := context.Background()

	out := LoginUsage(ctx, "claude")
	if len(out) != 2 || out["alice@x.com"].Windows[0].Used != 10 || out["bob@x.com"].Windows[0].Used != 20 {
		t.Fatalf("usage = %+v", out)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("first call made %d requests, want 2", n)
	}
	LoginUsage(ctx, "claude")
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("answer within a minute was not reused: %d requests", n)
	}

	// the minute is up and bob's query fails: bob keeps what was known
	loginUsageMu.Lock()
	entry := loginUsageCache["claude"]
	entry.at = time.Time{}
	loginUsageCache["claude"] = entry
	loginUsageMu.Unlock()
	atomic.StoreInt32(&failBob, 1)
	out = LoginUsage(ctx, "claude")
	bob := out["bob@x.com"]
	if bob.Error != "" || len(bob.Windows) != 1 || bob.Windows[0].Used != 20 {
		t.Fatalf("bob after failed query = %+v, want the earlier answer", bob)
	}
	if out["alice@x.com"].Windows[0].Used != 10 {
		t.Fatalf("alice = %+v", out["alice@x.com"])
	}
}

func TestMSAcctAddAccountFailureRestoresSignIn(t *testing.T) {
	_, dir := msAcctEnv(t)
	msAcctWrite(t, msAcctCredFile(dir), msAcctClaudeLogin("tok-alice", "rt-alice"))
	LoginRunner = func(string) error { return errors.New("login cancelled") }
	if _, err := AddAccount("claude"); err == nil {
		t.Fatal("a cancelled sign-in was reported as success")
	}
	if got := msAcctClaudeField(msAcctRead(t, msAcctCredFile(dir)), "accessToken"); got != "tok-alice" {
		t.Fatalf("live sign-in changed by a cancelled login: %q", got)
	}
	if _, err := os.Stat(secretPath("claude", "alice@x.com")); err == nil {
		t.Fatal("alice has a saved copy while the agent still holds her tokens")
	}
}
