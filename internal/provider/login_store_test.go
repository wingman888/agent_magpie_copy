package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loginHome points every path magpie and the agents read at a temp home.
func loginHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	return home
}

func readJSONFile(t *testing.T, path string) map[string]any {
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

// codexAccess is the access token of a Codex login for email; it does not
// expire, so no refresh is attempted.
func codexAccess(email string) string {
	return fakeJWT(map[string]any{"exp": float64(4102444800), "sub": email})
}

// codexSignIn writes the Codex CLI's auth.json for email, as `codex login` would.
func codexSignIn(t *testing.T, home, email, refresh string) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token": fakeJWT(map[string]any{"email": email,
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-" + email}}),
			"access_token":  codexAccess(email),
			"refresh_token": refresh,
			"account_id":    "acct-" + email,
		},
		"last_refresh": "2026-01-01T00:00:00Z",
		"keep":         "me",
	})
}

// claudeLoginFile writes Claude Code's credential file, with an MCP entry the
// login does not own.
func claudeLoginFile(t *testing.T, home, plan, access, refresh, mcp string) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": access, "refreshToken": refresh,
			"expiresAt": float64(4102444800000), "subscriptionType": plan},
		"mcpOAuth": map[string]any{"srv": mcp},
	})
	forgetClaudeCredential()
}

func rememberedFile(t *testing.T, agent, user string) string {
	t.Helper()
	ix := loadIndex()
	i := ix.find(agent, user)
	if i < 0 {
		t.Fatalf("%s %s not remembered: %+v", agent, user, ix.Accounts)
	}
	return ix.Accounts[i].path()
}

func TestRememberedCodexAccountsSwitch(t *testing.T) {
	home := loginHome(t)
	codexSignIn(t, home, "a@example.com", "ra")
	Accounts() // the first account is remembered, not on
	codexSignIn(t, home, "b@example.com", "rb")
	Accounts() // the second one is remembered and on by default

	list := Remembered("codex")
	if len(list) != 2 || list[0].User != "b@example.com" || !list[0].Live || !list[0].On ||
		list[1].User != "a@example.com" || list[1].Live || list[1].On {
		t.Fatalf("remembered: %+v", list)
	}
	bfile := rememberedFile(t, "codex", "b@example.com")
	if st, err := os.Stat(bfile); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file: %v %v", st, err)
	}

	// the agent refreshes b's token; the remembered copy must follow
	codexSignIn(t, home, "b@example.com", "rb2")
	Accounts()
	if err := SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := SwitchAccount("codex", "A@Example.COM"); err != nil {
		t.Fatal(err)
	}

	auth := readJSONFile(t, filepath.Join(home, ".codex", "auth.json"))
	if auth["keep"] != "me" || auth["tokens"].(map[string]any)["refresh_token"] != "ra" {
		t.Fatalf("agent file after switch: %v", auth)
	}
	saved := readJSONFile(t, bfile)
	if saved["tokens"].(map[string]any)["refresh_token"] != "rb2" {
		t.Fatalf("latest tokens of the replaced account lost: %v", saved)
	}
	list = Remembered("codex")
	if len(list) != 2 || list[0].User != "a@example.com" || !list[0].Live ||
		list[1].User != "b@example.com" || !list[1].On {
		t.Fatalf("after switch: %+v", list)
	}

	if err := SwitchAccount("codex", "nobody@example.com"); err == nil {
		t.Fatal("switched to an account magpie never saw")
	}
	if err := SetLoginOn("codex", "a@example.com", false); err == nil {
		t.Fatal("the signed-in account was turned off")
	}
	if err := ForgetAccount("codex", "a@example.com"); err == nil {
		t.Fatal("the signed-in account was forgotten")
	}
	if err := ForgetAccount("codex", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bfile); !os.IsNotExist(err) {
		t.Fatalf("forgotten credentials still on disk: %v", err)
	}
	if got := Remembered("codex"); len(got) != 1 || got[0].User != "a@example.com" {
		t.Fatalf("after forget: %+v", got)
	}
	if err := SwitchAccount("gemini", "x"); err == nil {
		t.Fatal("gemini accepted")
	}
}

func TestAlternateAccountsAreCandidatesInOrder(t *testing.T) {
	home := loginHome(t)
	codexSignIn(t, home, "a@example.com", "ra")
	Accounts()
	codexSignIn(t, home, "b@example.com", "rb")
	Accounts()
	codexSignIn(t, home, "c@example.com", "rc")
	Accounts()
	if err := SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}

	p, ok := find(Accounts(), "codex")
	if !ok {
		t.Fatal("codex not listed")
	}
	cands := p.Candidates("gpt-5.5", Responses)
	var users []string
	for _, c := range cands {
		users = append(users, c.Account.User)
	}
	if strings.Join(users, ",") != "c@example.com,a@example.com,b@example.com" {
		t.Fatalf("candidate order: %v", users)
	}
	// each candidate signs with its own credentials
	for _, c := range cands {
		req, _ := http.NewRequest(http.MethodPost, "http://example.invalid/responses", nil)
		if err := c.Sign(context.Background(), req, Responses, nil); err != nil {
			t.Fatal(err)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer "+codexAccess(c.Account.User) {
			t.Fatalf("%s signed with %q", c.Account.User, got)
		}
	}
}

func TestClaudeSwitchKeepsOtherFieldsAndOffAgentStore(t *testing.T) {
	home := loginHome(t)
	credPath := filepath.Join(home, ".claude", ".credentials.json")
	claudeLoginFile(t, home, "pro", "acc-pro", "refresh-pro-1", "pro-state")
	Accounts() // Claude Pro, remembered first
	claudeLoginFile(t, home, "max", "acc-max", "refresh-max-1", "max-state")
	Accounts() // Claude Max, the second: on by default

	if err := SetLoginOn("claude", "Claude Pro", true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(credPath)
	if strings.Contains(string(b), "refresh-pro-1") {
		t.Fatal("an account that is on alongside leaked into the agent's own credential file")
	}
	if err := SwitchAccount("claude", "claude pro"); err != nil {
		t.Fatal(err)
	}
	file := readJSONFile(t, credPath)
	oauth := file["claudeAiOauth"].(map[string]any)
	if oauth["refreshToken"] != "refresh-pro-1" || oauth["accessToken"] != "acc-pro" {
		t.Fatalf("switched login: %v", oauth)
	}
	if file["mcpOAuth"].(map[string]any)["srv"] != "max-state" {
		t.Fatalf("a field the login does not own was replaced: %v", file)
	}
	list := Remembered("claude")
	if len(list) != 2 || list[0].User != "Claude Pro" || !list[0].Live || list[1].User != "Claude Max" || !list[1].On {
		t.Fatalf("after switch: %+v", list)
	}
	saved := readJSONFile(t, rememberedFile(t, "claude", "Claude Max"))
	if saved["claudeAiOauth"].(map[string]any)["refreshToken"] != "refresh-max-1" {
		t.Fatalf("replaced account lost its sign-in: %v", saved)
	}
}

func TestLoginUsagePerAccountKeepsLastKnown(t *testing.T) {
	home := loginHome(t)
	codexSignIn(t, home, "a@example.com", "ra")
	Accounts()
	codexSignIn(t, home, "b@example.com", "rb")
	Accounts()

	fail := false
	usage := func(used int) string {
		return fmt.Sprintf(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":%d,"limit_window_seconds":18000,"reset_after_seconds":60},"secondary_window":{"used_percent":40,"limit_window_seconds":604800,"reset_after_seconds":600}}}`, used)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/wham/usage") {
			http.NotFound(w, r)
			return
		}
		switch r.Header.Get("Authorization") {
		case "Bearer " + codexAccess("a@example.com"):
			if fail {
				http.Error(w, "down", http.StatusInternalServerError)
				return
			}
			w.Write([]byte(usage(12)))
		case "Bearer " + codexAccess("b@example.com"):
			w.Write([]byte(usage(70)))
		default:
			http.Error(w, "unknown account", http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	oldBase := CodexBase
	CodexBase = srv.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase })

	got := LoginUsage(context.Background(), "codex")
	if len(got) != 2 || got["a@example.com"].Windows[0].Used != 12 || got["b@example.com"].Windows[0].Used != 70 ||
		got["a@example.com"].Windows[1].Name != "7 days" {
		t.Fatalf("usage: %+v", got)
	}
	if n := len(LoginUsage(context.Background(), "gemini")); n != 0 {
		t.Fatalf("unsupported agent gave %d entries", n)
	}

	// a failing query keeps the last known answer
	fail = true
	loginUsageCache.Lock()
	loginUsageCache.at = map[string]time.Time{}
	loginUsageCache.Unlock()
	got = LoginUsage(context.Background(), "codex")
	if got["a@example.com"].Error != "" || got["a@example.com"].Windows[0].Used != 12 {
		t.Fatalf("last known answer lost: %+v", got["a@example.com"])
	}
}
