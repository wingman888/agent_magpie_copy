package gateway

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// asgJWT is an unsigned token whose payload is the given claims.
func asgJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// asgAuth is what Codex CLI keeps in auth.json for one ChatGPT account.
func asgAuth(email, refresh string) map[string]any {
	return map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token": asgJWT(map[string]any{"email": email,
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}}),
			"access_token":  asgJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix()), "sub": refresh}),
			"refresh_token": refresh,
			"account_id":    "acct-" + refresh,
		},
	}
}

// asgWrite writes a JSON value to a file, creating its directory.
func asgWrite(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// asgSetup puts a Codex sign-in for a@example.com in the agent's place, and
// remembers b@example.com as an account that serves alongside it.
func asgSetup(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	asgWrite(t, filepath.Join(home, ".codex", "auth.json"), asgAuth("a@example.com", "rt-a"))
	asgWrite(t, filepath.Join(home, ".config", "magpie", "accounts.json"), map[string]any{
		"accounts": []any{map[string]any{
			"agent": "codex", "user": "b@example.com", "on": true,
			"cred": asgAuth("b@example.com", "rt-b"),
		}},
	})
}

func TestAsgSameAgentAccountsTriedInOrder(t *testing.T) {
	asgSetup(t)
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	gw := New()
	ids := func() string {
		var out []string
		for _, tg := range gw.chain(*p, provider.Responses, "gpt-5.5", nil) {
			out = append(out, tg.ID)
		}
		return strings.Join(out, ",")
	}
	// the agent's current login first, then the account that serves alongside it
	if got := ids(); got != "codex,codex-b-example-com" {
		t.Fatalf("chain = %s", got)
	}
	// once the current login ran out, the next request goes to the other account first
	gw.restKey(provider.Candidate{Provider: *p})
	if got := ids(); got != "codex-b-example-com,codex" {
		t.Fatalf("after the current login ran out, chain = %s", got)
	}
}

func TestAsgAccountRequestedFirstThenPeers(t *testing.T) {
	asgSetup(t)
	q, err := provider.Find("codex-b-example-com")
	if err != nil {
		t.Fatal(err)
	}
	gw := New()
	var out []string
	for _, tg := range gw.chain(*q, provider.Responses, "gpt-5.5", nil) {
		out = append(out, tg.ID)
	}
	if got := strings.Join(out, ","); got != "codex-b-example-com,codex" {
		t.Fatalf("chain for the remembered account = %s", got)
	}
}
