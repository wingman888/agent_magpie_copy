package provider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// s5Status stubs `claude auth status --json` with the given output and error.
func s5Status(t *testing.T, out string, err error) {
	t.Helper()
	old := claudeAuthStatus
	claudeAuthStatus = func(context.Context) ([]byte, error) { return []byte(out), err }
	forgetClaudeCredential()
	t.Cleanup(func() {
		claudeAuthStatus = old
		forgetClaudeCredential()
	})
}

func TestS5ClaudeSignedOutIsNotProvider(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))

	// exit code 1 with loggedIn:false: the credential left behind does not count
	s5Status(t, `{"loggedIn":false}`, errors.New("exit status 1"))
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("claude listed although claude auth status says signed out")
	}

	// signed in again: listed, with the email from the status
	s5Status(t, `{"loggedIn":true,"email":"me@example.com","subscriptionType":"max"}`, nil)
	p, ok := find(All(), "claude")
	if !ok || p.Account == nil || p.Account.User != "me@example.com" {
		t.Fatalf("claude after sign-in: %+v %v", p, ok)
	}
}

func TestS5ClaudeUnknownStatusKeepsCredentialJudgement(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))

	// command missing: credential decides, as before
	s5Status(t, "", os.ErrNotExist)
	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("claude dropped when the status command is missing")
	}
	// output that does not parse: same
	s5Status(t, "not json", errors.New("exit status 1"))
	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("claude dropped on unparseable status output")
	}
	// JSON without loggedIn: same
	s5Status(t, `{"email":"x@example.com"}`, nil)
	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("claude dropped when loggedIn is absent")
	}
}

func TestS5RemoveAndRestoreClaude(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	s5Status(t, `{"loggedIn":true,"email":"me@example.com"}`, nil)

	if err := Save(Provider{ID: "claude", Models: []string{"claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("claude"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("claude listed after removal")
	}
	// the sign-in itself is untouched
	if _, err := os.Stat(filepath.Join(home, ".claude", ".credentials.json")); err != nil {
		t.Fatalf("credentials touched: %v", err)
	}
	// removing again is harmless and does not duplicate the exclusion
	if err := Delete("claude"); err != nil {
		t.Fatal(err)
	}
	if ex := Excluded(); len(ex) != 1 || ex[0].Provider != "claude" || ex[0].Why == "" {
		t.Fatalf("excluded: %+v", ex)
	}

	// adding it back keeps the model picks, and a later plain add does too
	if err := Save(Provider{ID: "claude"}); err != nil {
		t.Fatal(err)
	}
	p, ok := find(All(), "claude")
	if !ok || len(p.Exposed()) != 1 || p.Exposed()[0].ID != "claude-opus-5-5" {
		t.Fatalf("restored: %+v %v", p, ok)
	}
	if len(Excluded()) != 0 {
		t.Fatalf("still excluded: %+v", Excluded())
	}
}

func TestS5RemovalStoredInProvidersFile(t *testing.T) {
	signIn(t)
	if err := Delete("copilot"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Removed []string `json:"removed"`
	}
	if err := json.Unmarshal(b, &f); err != nil || len(f.Removed) != 1 || f.Removed[0] != "copilot" {
		t.Fatalf("file: %s (%v)", b, err)
	}
	if _, ok := find(All(), "codex"); !ok {
		t.Fatal("removing copilot hid codex too")
	}
}
