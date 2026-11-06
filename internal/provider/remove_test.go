package provider

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// resetClaudeStatus forgets the cached `claude auth status` answer.
func resetClaudeStatus(t *testing.T) {
	t.Helper()
	claudeStatusMu.Lock()
	claudeStatusAt = time.Time{}
	claudeStatusOut = false
	claudeStatusUser, claudeStatusPlan = "", ""
	claudeStatusMu.Unlock()
}

// stubClaudeStatus stands in for `claude auth status --json` for one test.
func stubClaudeStatus(t *testing.T, out string, err error) {
	t.Helper()
	old := claudeAuthStatus
	claudeAuthStatus = func() ([]byte, error) { return []byte(out), err }
	resetClaudeStatus(t)
	t.Cleanup(func() {
		claudeAuthStatus = old
		resetClaudeStatus(t)
	})
}

func TestRemoveHidesAccountAndAddBringsItBack(t *testing.T) {
	home := signIn(t)

	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Remove("codex"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "codex"); ok {
		t.Fatal("removed codex still listed")
	}
	if _, err := Find("codex"); err == nil {
		t.Fatal("removed codex still found")
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Fatal("removed codex still resolves")
	}
	// hiding never touches the agent's own sign-in
	if _, ok := find(Accounts(), "codex"); !ok {
		t.Fatal("removing codex signed the agent out of magpie's view of it")
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatal("codex credentials were touched:", err)
	}
	if _, ok := find(All(), "copilot"); !ok {
		t.Fatal("copilot should still be listed")
	}

	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "codex" || ex[0].Provider != "codex" ||
		!strings.Contains(ex[0].Why, "magpie provider add codex") {
		t.Fatalf("excluded: %+v", ex)
	}

	// the model picks were kept, and come back with the account
	if err := Save(Provider{ID: "codex", Name: "codex"}); err != nil {
		t.Fatal(err)
	}
	codex, ok := find(All(), "codex")
	if !ok || codex.Account == nil || len(codex.Models) != 1 || codex.Models[0] != "gpt-5.5" {
		t.Fatalf("restored codex: %+v %v", codex, ok)
	}
	if len(Excluded()) != 0 {
		t.Fatal("restored account still excluded")
	}
}

func TestRemoveOfPlainProviderDeletes(t *testing.T) {
	signIn(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := Remove("relay"); err != nil {
		t.Fatal(err)
	}
	if _, err := Find("relay"); err == nil {
		t.Fatal("removed relay still found")
	}
	if len(Excluded()) != 0 {
		t.Fatal("a plain provider is not an excluded account")
	}
}

func TestClaudeSignedOutIsNotProvider(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))

	stubClaudeStatus(t, `{"loggedIn": true, "email": "me@example.com", "subscriptionType": "max"}`, nil)
	if p, ok := find(All(), "claude"); !ok || p.Account == nil || p.Account.User != "me@example.com" {
		t.Fatalf("signed in: %+v %v", p, ok)
	}

	// signed out, though the credentials are still on disk: not a provider,
	// even when the command exits non-zero
	stubClaudeStatus(t, `{"loggedIn": false}`, errors.New("exit status 1"))
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("signed-out Claude Code listed")
	}

	// an answer magpie cannot read leaves the judgment to the credentials
	stubClaudeStatus(t, `not json`, nil)
	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("unreadable status dropped a signed-in Claude Code")
	}

	// so does a missing command
	stubClaudeStatus(t, "", errors.New("claude is not installed"))
	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("a missing claude command dropped a signed-in Claude Code")
	}

	// signed in again, seen on the next check
	stubClaudeStatus(t, `{"loggedIn": false}`, errors.New("exit status 1"))
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("signed-out Claude Code listed again")
	}
	stubClaudeStatus(t, `{"loggedIn": true, "email": "me@example.com"}`, nil)
	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("signed back in, but not listed")
	}
}

func TestParseClaudeAuthStatus(t *testing.T) {
	cases := []struct {
		out      string
		ok, look bool
	}{
		{`{"loggedIn": true, "email": "a@b.c"}`, true, true},
		{`{"loggedIn": false}`, true, false},
		{`{"email": "a@b.c"}`, false, false},
		{`not json`, false, false},
		{``, false, false},
	}
	for _, c := range cases {
		st, ok := parseClaudeAuthStatus([]byte(c.out))
		if ok != c.ok || (ok && *st.LoggedIn != c.look) {
			t.Fatalf("%q: ok=%v", c.out, ok)
		}
	}
}
