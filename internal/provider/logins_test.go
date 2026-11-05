package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readMap(t *testing.T, path string) map[string]any {
	t.Helper()
	var m map[string]any
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// writeCodexLogin signs the Codex CLI in as email, with the given refresh token.
func writeCodexLogin(t *testing.T, home, email, refresh string) {
	t.Helper()
	exp := float64(time.Now().Add(time.Hour).Unix())
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-" + email}}),
			"access_token":  fakeJWT(map[string]any{"exp": exp}),
			"refresh_token": refresh, "account_id": "acct-" + email,
		},
	})
}

func codexRefreshOnDisk(t *testing.T, home string) string {
	t.Helper()
	m := readMap(t, filepath.Join(home, ".codex", "auth.json"))
	toks, _ := m["tokens"].(map[string]any)
	s, _ := toks["refresh_token"].(string)
	return s
}

func findLoginNamed(t *testing.T, agent, user string) Login {
	t.Helper()
	logins, err := Logins(agent)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range logins {
		if l.User == user {
			return l
		}
	}
	t.Fatalf("no remembered %s login %q in %+v", agent, user, logins)
	return Login{}
}

// addSecondCodex signs in a second ChatGPT account through AddLogin, as
// `magpie accounts add codex` does, and returns what was remembered.
func addSecondCodex(t *testing.T, home string) Login {
	t.Helper()
	got, err := AddLogin("codex", func() error {
		writeCodexLogin(t, home, "other@example.com", "r2")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSwitchMovesCredentialsAndKeepsOtherFields(t *testing.T) {
	home := signIn(t)
	path := filepath.Join(home, ".codex", "auth.json")

	added := addSecondCodex(t, home)
	if added.User != "other@example.com" || !added.On || !added.Current {
		t.Fatalf("second account = %+v, want current and on", added)
	}
	if got := codexRefreshOnDisk(t, home); got != "r2" {
		t.Fatalf("after sign-in refresh = %q, want r2", got)
	}
	if me := findLoginNamed(t, "codex", "me@example.com"); !me.On || me.Current {
		t.Fatalf("displaced account = %+v, want on and not current", me)
	}
	m := readMap(t, path)
	m["extra"] = "keep"
	writeFile(t, path, m)

	if err := SwitchLogin("codex", "ME@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := codexRefreshOnDisk(t, home); got != "r" {
		t.Fatalf("after switch refresh = %q, want the switched-to account's r", got)
	}
	if got := readMap(t, path)["extra"]; got != "keep" {
		t.Fatalf("agent's other field = %v, want keep", got)
	}
	if other := findLoginNamed(t, "codex", "other@example.com"); other.Current || !other.Held || !other.On {
		t.Fatalf("replaced account = %+v, want held and on (it inherits the target's on state)", other)
	}

	// the account switched away from kept its newest credentials
	if err := SwitchLogin("codex", "other@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := codexRefreshOnDisk(t, home); got != "r2" {
		t.Fatalf("switched back refresh = %q, want r2", got)
	}
}

func TestCurrentLoginCannotBeTurnedOffOrForgotten(t *testing.T) {
	signIn(t)
	if err := SetLoginOn("codex", "me@example.com", false); err == nil {
		t.Fatal("turning off the current login must fail")
	}
	if err := SetLoginOn("codex", "me@example.com", true); err != nil {
		t.Fatalf("turning on the current login: %v", err)
	}
	if err := ForgetLogin("codex", "me@example.com"); err == nil {
		t.Fatal("forgetting the current login must fail")
	}
	if err := SetLoginOn("codex", "nobody@example.com", true); err == nil {
		t.Fatal("an unknown account must fail")
	}
	if err := SwitchLogin("codex", "nobody@example.com"); err == nil {
		t.Fatal("switching to an unknown account must fail")
	}
	if err := SwitchLogin("gemini", "me@example.com"); err == nil {
		t.Fatal("an agent without remembered accounts must fail")
	}
	info, err := os.Stat(loginsPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("accounts file mode = %v; want 0600", info.Mode().Perm())
	}
}

func TestOtherAccountServesAlongsideCurrent(t *testing.T) {
	home := signIn(t)
	addSecondCodex(t, home)

	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	cs := p.Choices(Responses, "gpt-5.5")
	if len(cs) != 2 {
		t.Fatalf("candidates = %d, want 2 (current, then the on account)", len(cs))
	}
	if cs[0].Provider.Account.User != "other@example.com" || cs[1].Provider.Account.User != "me@example.com" {
		t.Fatalf("order = %s, %s", cs[0].Provider.Account.User, cs[1].Provider.Account.User)
	}
	if cs[0].Ref == cs[1].Ref {
		t.Fatal("each login needs its own fingerprint, or one rest would hide the other")
	}

	if err := SetLoginOn("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	if cs := p.Choices(Responses, "gpt-5.5"); len(cs) != 1 || cs[0].Provider.Account.User != "other@example.com" {
		t.Fatalf("with the displaced account off, candidates = %d, want only the current one", len(cs))
	}
}

// A login magpie holds signs requests with its own tokens, not the agent's.
func TestHeldLoginSignsWithItsOwnTokens(t *testing.T) {
	home := signIn(t)
	addSecondCodex(t, home)

	f := loadLogins()
	held := f.Logins[findLogin(f, "codex", "me@example.com")]
	toks, _ := held.Auth["tokens"].(map[string]any)
	heldAccess, _ := toks["access_token"].(string)
	if heldAccess == "" {
		t.Fatal("magpie should hold the displaced account's tokens")
	}

	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	cs := p.Choices(Responses, "gpt-5.5")
	req, _ := http.NewRequest(http.MethodPost, "https://example.invalid/responses", nil)
	if err := cs[1].Provider.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+heldAccess {
		t.Fatalf("held login signed with %q, want its own token", got)
	}
	if got := req.Header.Get("chatgpt-account-id"); got != "acct-1" { // signIn's account for me@example.com
		t.Fatalf("account id = %q", got)
	}
}

func TestLoginUsageKeepsLastKnownWhenQueryFails(t *testing.T) {
	signIn(t)
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wham/usage" {
			http.NotFound(w, r)
			return
		}
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":20,"limit_window_seconds":604800}}}`))
	}))
	defer srv.Close()
	oldBase := CodexBase
	CodexBase = srv.URL + "/codex"
	t.Cleanup(func() { CodexBase = oldBase })

	got := LoginUsage(context.Background(), "codex")
	q, ok := got["me@example.com"]
	if !ok || q.FiveHourUsed != 10 || q.WeeklyUsed != 20 {
		t.Fatalf("usage = %+v, want 5h 10%% and weekly 20%%", got)
	}

	// within a minute the result is reused, so the failing server is not asked
	fail = true
	if again := LoginUsage(context.Background(), "codex"); again["me@example.com"].FiveHourUsed != 10 {
		t.Fatalf("within a minute the result should be reused: %+v", again)
	}

	loginUsage.Lock()
	loginUsage.at = map[string]time.Time{}
	loginUsage.Unlock()
	got = LoginUsage(context.Background(), "codex")
	q = got["me@example.com"]
	if q.FiveHourUsed != 10 || q.WeeklyUsed != 20 || q.Error == "" {
		t.Fatalf("after a failed query = %+v, want the previous 10%% / 20%% kept and the error noted", q)
	}
}

func TestLoginUsageEmptyForOtherAgents(t *testing.T) {
	signIn(t)
	if got := LoginUsage(context.Background(), "copilot"); len(got) != 0 {
		t.Fatalf("copilot usage = %v, want none", got)
	}
}

func TestClaudeSwitchKeepsAgentFileIntact(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(dir, ".credentials.json")
	future := time.Now().Add(time.Hour).UnixMilli()
	writeFile(t, path, map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "a-max", "refreshToken": "r-max", "expiresAt": future, "subscriptionType": "max"},
		"mcpOAuth":      map[string]any{"server": "kept"},
	})

	if _, err := AddLogin("claude", func() error {
		writeFile(t, path, map[string]any{
			"claudeAiOauth": map[string]any{"accessToken": "a-pro", "refreshToken": "r-pro", "expiresAt": future, "subscriptionType": "pro"},
			"mcpOAuth":      map[string]any{"server": "other"},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := SwitchLogin("claude", "claude max"); err != nil {
		t.Fatal(err)
	}
	m := readMap(t, path)
	oauth, _ := m["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "a-max" {
		t.Fatalf("switched-to token = %v, want a-max", oauth["accessToken"])
	}
	if mcp, _ := m["mcpOAuth"].(map[string]any); mcp["server"] != "other" {
		t.Fatalf("agent's other field changed: %v", m["mcpOAuth"])
	}
	if pro := findLoginNamed(t, "claude", "Claude Pro"); !pro.Held || pro.Current {
		t.Fatalf("replaced account = %+v", pro)
	}

	// a held Claude login runs from a private config dir holding its tokens,
	// which is removed once the run is over
	p, err := Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	cs := p.Choices(Anthropic, "claude-sonnet-5-5")
	if len(cs) != 2 {
		t.Fatalf("claude candidates = %d, want 2", len(cs))
	}
	env, done, err := cs[1].Provider.AgentEnv(context.Background())
	if err != nil || len(env) != 1 || !strings.HasPrefix(env[0], "CLAUDE_CONFIG_DIR=") {
		t.Fatalf("held login env = %v, %v", env, err)
	}
	runDir := strings.TrimPrefix(env[0], "CLAUDE_CONFIG_DIR=")
	if oauth, _ := readMap(t, filepath.Join(runDir, ".credentials.json"))["claudeAiOauth"].(map[string]any); oauth["accessToken"] != "a-pro" {
		t.Fatalf("run dir token = %v, want a-pro", oauth["accessToken"])
	}
	done()
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("run dir should be removed, stat err = %v", err)
	}

	if err := SwitchLogin("claude", "Claude Pro"); err != nil {
		t.Fatal(err)
	}
	oauth, _ = readMap(t, path)["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "a-pro" {
		t.Fatalf("switched back token = %v, want the newest a-pro", oauth["accessToken"])
	}
}
