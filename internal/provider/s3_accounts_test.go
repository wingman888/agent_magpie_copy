package provider

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestS3ClaudeSignedOutIsNotProvider: leftover credentials do not make a
// provider once `claude auth status` says the user is signed out, even though
// it exits non-zero, and an unreadable answer keeps the old judgement.
func TestS3ClaudeSignedOutIsNotProvider(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	oldRun := claudeStatusRun
	resetStatus := func() {
		claudeStatusMu.Lock()
		claudeStatusAt = time.Time{}
		claudeStatusMu.Unlock()
	}
	t.Cleanup(func() { claudeStatusRun = oldRun; resetStatus() })

	resetStatus()
	claudeStatusRun = func() ([]byte, error) {
		return []byte(`{"loggedIn": false}`), errors.New("exit status 1")
	}
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("signed-out Claude Code listed as a provider")
	}

	resetStatus()
	claudeStatusRun = func() ([]byte, error) {
		return []byte("not json"), errors.New("exit status 2")
	}
	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("unreadable status dropped a Claude Code provider that has credentials")
	}

	resetStatus()
	claudeStatusRun = func() ([]byte, error) {
		return []byte(`{"loggedIn": true, "email": "me@example.com", "subscriptionType": "max"}`), nil
	}
	p, ok := find(All(), "claude")
	if !ok || p.Account == nil || p.Account.User != "me@example.com" {
		t.Fatalf("signed-in Claude Code: %+v %v", p, ok)
	}
}

// TestS3RemovedAccountComesBack: removing a signed-in account hides it, keeps
// its model picks, and adding it back restores them.
func TestS3RemovedAccountComesBack(t *testing.T) {
	signIn(t)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "codex"); !ok {
		t.Fatal("codex not listed after sign-in")
	}
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "codex"); ok {
		t.Fatal("removed codex still listed")
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Fatal("removed codex still resolves")
	}
	if got := Excluded(); len(got) != 1 || got[0].Agent != "codex" || got[0].Provider != "codex" {
		t.Fatalf("excluded: %+v", got)
	}
	if _, ok := find(All(), "copilot"); !ok {
		t.Fatal("removing codex hid copilot too")
	}

	if err := Save(Provider{ID: "codex"}); err != nil {
		t.Fatal(err)
	}
	p, ok := find(All(), "codex")
	if !ok || !reflect.DeepEqual(p.Models, []string{"gpt-5.5"}) {
		t.Fatalf("restored codex: %+v %v", p, ok)
	}
	if got := Excluded(); len(got) != 0 {
		t.Fatalf("excluded after restore: %+v", got)
	}
}
