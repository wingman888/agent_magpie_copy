package provider

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// signedInCodex makes HOME hold a Codex sign-in and nothing else, so the
// test never sees the machine's real Claude Code or Copilot logins.
func signedInCodex(t *testing.T) (auth string) {
	t.Helper()
	home, cfg := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "no-claude"))
	t.Setenv("PATH", t.TempDir()) // no `claude` binary to ask
	old := claudeKeychain
	claudeKeychain = false
	t.Cleanup(func() { claudeKeychain = old })

	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"me@example.com"}`))
	body := `{"auth_mode":"chatgpt","tokens":{"id_token":"h.` + payload + `.s","access_token":"tok","refresh_token":"r","account_id":"acc"}}`
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	auth = filepath.Join(dir, "auth.json")
	if err := os.WriteFile(auth, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return auth
}

func TestRemoveHidesAccountAndKeepsPicksAndCredentials(t *testing.T) {
	auth := signedInCodex(t)
	before, err := os.ReadFile(auth)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Find("codex"); err != nil {
		t.Fatal("codex should be listed before removal:", err)
	}

	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := Find("codex"); err == nil {
		t.Fatal("removed account still resolves")
	}
	for _, p := range All() {
		if p.ID == "codex" {
			t.Fatal("removed account still in All()")
		}
	}
	if _, _, ok := Resolve("codex/gpt-test"); ok {
		t.Fatal("codex/<model> still resolves after removal")
	}
	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "codex" || ex[0].Provider != "codex" || ex[0].Why == "" {
		t.Fatalf("Excluded() = %+v", ex)
	}
	if after, _ := os.ReadFile(auth); !bytes.Equal(before, after) {
		t.Fatal("agent's own credentials were modified")
	}
	if got := picksIn(load().Providers, "codex"); len(got) != 1 || got[0] != "gpt-test" {
		t.Fatalf("model picks lost on removal: %v", got)
	}

	// adding it back restores the picks, and the exclusion goes away
	if err := Save(Provider{ID: "codex"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal("re-added account not found:", err)
	}
	if len(p.Models) != 1 || p.Models[0] != "gpt-test" {
		t.Fatalf("picks not restored: %v", p.Models)
	}
	if len(Excluded()) != 0 {
		t.Fatal("re-added account still excluded")
	}
}

func TestRemoveThenAddWithExplicitModelsUsesThem(t *testing.T) {
	signedInCodex(t)
	if err := Save(Provider{ID: "codex", Models: []string{"old"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "codex", Models: []string{"new"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil || len(p.Models) != 1 || p.Models[0] != "new" {
		t.Fatalf("explicit picks: %+v %v", p, err)
	}
}

func TestParseClaudeStatus(t *testing.T) {
	cases := []struct {
		in           string
		ok, loggedIn bool
		email        string
		plan         string
	}{
		{`{"loggedIn": false}`, true, false, "", ""},
		{"  {\"loggedIn\":false,\"email\":\"x@y\"}\n", true, false, "x@y", ""},
		{`{"loggedIn": true, "email": " me@example.com ", "subscriptionType": "max"}`, true, true, "me@example.com", "max"},
		{`{}`, false, false, "", ""},
		{`Not logged in · Please run /login`, false, false, "", ""},
		{``, false, false, "", ""},
	}
	for _, c := range cases {
		st, ok := parseClaudeStatus([]byte(c.in))
		if ok != c.ok {
			t.Fatalf("%q: ok=%v want %v", c.in, ok, c.ok)
		}
		if !ok {
			continue
		}
		if st.LoggedIn != c.loggedIn || st.Email != c.email || st.Plan != c.plan {
			t.Fatalf("%q: got %+v", c.in, st)
		}
	}
}
