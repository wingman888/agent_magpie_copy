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
func resetClaudeStatus() {
	claudeStatusMu.Lock()
	defer claudeStatusMu.Unlock()
	claudeStatusAt = time.Time{}
	claudeStatusUser, claudeStatusPlan, claudeStatusOut = "", "", false
}

// stubClaudeStatus makes `claude auth status --json` answer with out and err.
func stubClaudeStatus(t *testing.T, out string, err error) {
	t.Helper()
	old := claudeAuthStatus
	claudeAuthStatus = func() ([]byte, error) { return []byte(out), err }
	resetClaudeStatus()
	t.Cleanup(func() {
		claudeAuthStatus = old
		resetClaudeStatus()
	})
}

func TestParseClaudeAuth(t *testing.T) {
	cases := []struct {
		name  string
		out   string
		known bool
		in    bool
		email string
		plan  string
	}{
		{"signed in", `{"loggedIn": true, "email": "me@example.com", "subscriptionType": "max"}`, true, true, "me@example.com", "max"},
		{"signed out", `{"loggedIn": false}`, true, false, "", ""},
		{"no answer", ``, false, false, "", ""},
		{"not json", `Error: something`, false, false, "", ""},
		{"no loggedIn field", `{"email": "x@y"}`, false, false, "", ""},
	}
	for _, c := range cases {
		info, known := parseClaudeAuth([]byte(c.out))
		if known != c.known || info.LoggedIn != c.in || info.Email != c.email || info.Plan != c.plan {
			t.Errorf("%s: got %+v known=%v", c.name, info, known)
		}
	}
}

func TestClaudeSignedOutIsNotAProvider(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))

	// the credential is still on disk, but Claude Code reports nobody signed in,
	// and it exits non-zero while saying so
	stubClaudeStatus(t, `{"loggedIn": false}`, errors.New("exit status 1"))
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("claude listed although claude auth status says signed out")
	}
	if !claudeSignedOut() {
		t.Fatal("signed-out state not seen")
	}

	// the CLI cannot say: the credential decides, as it did before
	stubClaudeStatus(t, `garbage`, nil)
	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("claude dropped although the status is unreadable")
	}
	if claudeSignedOut() {
		t.Fatal("unreadable status must not count as signed out")
	}

	// and a signed-in answer keeps it listed, with the CLI's own identity
	stubClaudeStatus(t, `{"loggedIn": true, "email": "me@example.com", "subscriptionType": "pro"}`, nil)
	p, ok := find(All(), "claude")
	if !ok || p.Account == nil || p.Account.User != "me@example.com" || p.Account.Plan != "pro" {
		t.Fatalf("signed in: %+v %v", p.Account, ok)
	}
}

func TestRemovedAccountIsHiddenAndComesBack(t *testing.T) {
	home := signIn(t)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}

	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "codex"); ok {
		t.Fatal("removed codex still listed")
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Fatal("codex/gpt-5.5 still resolves")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Fatalf("catalog still has %s", e.ID)
		}
	}
	ex := Excluded()
	if len(ex) != 1 || ex[0].Agent != "codex" || ex[0].Provider != "codex" || !strings.Contains(ex[0].Why, "magpie provider add codex") {
		t.Fatalf("excluded: %+v", ex)
	}
	// the agent keeps its own login, and the other accounts stay
	if _, err := os.Stat(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Fatalf("codex sign-in touched: %v", err)
	}
	if _, ok := find(All(), "copilot"); !ok {
		t.Fatal("copilot disappeared with codex")
	}

	// adding it back restores the picks it had
	if err := Save(Provider{ID: "codex"}); err != nil {
		t.Fatal(err)
	}
	p, ok := find(All(), "codex")
	if !ok || len(p.Exposed()) != 1 || p.Exposed()[0].ID != "gpt-5.5" {
		t.Fatalf("restored codex: %+v %v", p.Models, ok)
	}
	if len(Excluded()) != 0 {
		t.Fatalf("still excluded after add: %+v", Excluded())
	}
}

func TestRemovedCopilotIsHidden(t *testing.T) {
	signIn(t)
	if err := Delete("copilot"); err != nil {
		t.Fatal(err)
	}
	if _, removed := hiddenPicks("copilot"); !removed {
		t.Fatal("copilot not marked removed")
	}
	if _, ok := find(All(), "copilot"); ok {
		t.Fatal("removed copilot listed")
	}
}
