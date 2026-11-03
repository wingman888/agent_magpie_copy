package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// forgetClaudeStatus drops the cached `claude auth status` answer.
func forgetClaudeStatus() {
	claudeStatusMu.Lock()
	claudeStatusAt = time.Time{}
	claudeStatusUser, claudeStatusPlan, claudeStatusOut = "", "", false
	claudeStatusMu.Unlock()
}

// claudeStatusSays makes `claude auth status` answer with out and exit error err.
func claudeStatusSays(out string, err error) {
	claudeAuthStatusRun = func(context.Context) ([]byte, error) { return []byte(out), err }
	forgetClaudeStatus()
}

func TestRemovedAccountIsHiddenNotSignedOut(t *testing.T) {
	home := signIn(t)
	codexAuth := filepath.Join(home, ".codex", "auth.json")
	before, _ := os.ReadFile(codexAuth)

	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}

	// the agent's own login is untouched
	if after, _ := os.ReadFile(codexAuth); string(after) != string(before) {
		t.Fatal("codex login was changed by remove")
	}
	if _, ok := find(All(), "codex"); ok {
		t.Fatal("removed codex still listed")
	}
	if _, ok := find(Accounts(), "codex"); ok {
		t.Fatal("removed codex still an account")
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Fatal("codex/gpt-5.5 still resolves")
	}
	if _, err := Find("codex"); err == nil {
		t.Fatal("Find still finds removed codex")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Fatalf("catalog still has %s", e.ID)
		}
	}
	// the other sign-ins are unaffected
	if _, ok := find(All(), "copilot"); !ok {
		t.Fatal("copilot disappeared with codex")
	}

	want := Exclusion{Agent: "codex", Provider: "codex", Why: "you removed it from magpie"}
	if x := Excluded(); len(x) != 1 || x[0] != want {
		t.Fatalf("excluded: %+v", x)
	}
}

func TestAddBringsRemovedAccountBack(t *testing.T) {
	signIn(t)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}

	// `magpie provider add codex` carries no picks; the old ones come back
	if err := Save(Provider{ID: "codex", Name: "codex"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if p.Account == nil || len(p.Exposed()) != 1 || p.Exposed()[0].ID != "gpt-5.5" {
		t.Fatalf("restored: %+v exposed %+v", p, p.Exposed())
	}
	if len(Excluded()) != 0 {
		t.Fatalf("still excluded: %+v", Excluded())
	}
	if _, model, ok := Resolve("codex/gpt-5.5"); !ok || model != "gpt-5.5" {
		t.Fatalf("resolve after restore: %q %v", model, ok)
	}
	// the stored entry is no longer hidden
	if e, ok := storedEntry(load(), "codex"); !ok || e.Hidden {
		t.Fatalf("stored after restore: %+v %v", e, ok)
	}
}

func TestClaudeRemovedKeepsCredentials(t *testing.T) {
	creds := claudeSignIn(t, claudeHome(t), time.Now().Add(time.Hour))
	claudeStatusSays(`{"loggedIn":true,"email":"me@example.com","subscriptionType":"max"}`, nil)
	before, _ := os.ReadFile(creds)

	if err := Delete("claude"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("removed claude still listed")
	}
	if after, _ := os.ReadFile(creds); string(after) != string(before) {
		t.Fatal("claude credentials changed by remove")
	}
	if x := Excluded(); len(x) != 1 || x[0].Agent != "claude" || x[0].Provider != "claude" {
		t.Fatalf("excluded: %+v", x)
	}
}

func TestClaudeSignedOutIsNotAProvider(t *testing.T) {
	creds := claudeSignIn(t, claudeHome(t), time.Now().Add(time.Hour))

	// Claude Code says it is signed out: the credential left behind is not a login,
	// whether or not the command exited non-zero
	for _, tc := range []struct {
		name string
		out  string
		err  error
	}{
		{"signed out, exit 0", `{"loggedIn":false}`, nil},
		{"signed out, exit 1", `{"loggedIn":false}`, errors.New("exit status 1")},
	} {
		claudeStatusSays(tc.out, tc.err)
		if _, ok := find(All(), "claude"); ok {
			t.Errorf("%s: claude listed", tc.name)
		}
		if _, err := os.Stat(creds); err != nil {
			t.Fatalf("%s: credential file should stay: %v", tc.name, err)
		}
	}

	// command unavailable, or output unreadable: the credential check decides, as before
	for _, tc := range []struct {
		name string
		out  string
		err  error
	}{
		{"command missing", "", errors.New("claude is not installed")},
		{"output not JSON", "not json", nil},
		{"no loggedIn field", `{"email":"me@example.com"}`, nil},
	} {
		claudeStatusSays(tc.out, tc.err)
		if _, ok := find(All(), "claude"); !ok {
			t.Errorf("%s: claude not listed although credentials exist", tc.name)
		}
	}

	// signed in again
	claudeStatusSays(`{"loggedIn":true,"email":"me@example.com","subscriptionType":"max"}`, nil)
	p, ok := find(All(), "claude")
	if !ok || p.Account == nil || p.Account.User != "me@example.com" {
		t.Fatalf("signed in: %+v %v", p, ok)
	}
}
