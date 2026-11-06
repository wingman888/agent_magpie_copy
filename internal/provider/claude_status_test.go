package provider

import (
	"testing"
	"time"
)

// resetClaudeStatus forgets what Claude Code was last asked.
func resetClaudeStatus() {
	claudeStatusMu.Lock()
	defer claudeStatusMu.Unlock()
	claudeStatusAt = time.Time{}
	claudeStatusKnown, claudeStatusIn = false, false
	claudeStatusUser, claudeStatusPlan = "", ""
}

// fakeClaudeStatus makes `claude auth status --json` print out.
func fakeClaudeStatus(t *testing.T, out string) {
	t.Helper()
	old := claudeStatusOutput
	claudeStatusOutput = func() []byte { return []byte(out) }
	resetClaudeStatus()
	t.Cleanup(func() {
		claudeStatusOutput = old
		resetClaudeStatus()
	})
}

func TestClaudeSignedOutWithLeftoverCredentialIsNotAProvider(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	forgetClaudeCredential()

	// the command exits non-zero when signed out; the JSON on stdout still says so
	fakeClaudeStatus(t, `{"loggedIn": false}`)
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("claude listed although Claude Code reports signed out")
	}
}

func TestClaudeSignedInStatusNamesTheAccount(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	forgetClaudeCredential()

	fakeClaudeStatus(t, `{"loggedIn": true, "email": "me@example.com", "subscriptionType": "pro"}`)
	p, ok := find(All(), "claude")
	if !ok || p.Account == nil || p.Account.User != "me@example.com" || p.Account.Plan != "pro" {
		t.Fatalf("claude: %+v %v", p, ok)
	}
}

func TestClaudeUnreadableStatusAfterSignedOutFallsBackToOwnView(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	forgetClaudeCredential()

	fakeClaudeStatus(t, `{"loggedIn": false}`)
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("claude listed although Claude Code reports signed out")
	}
	// the last answer must not stick once Claude Code stops giving a usable one
	for _, out := range []string{"not json", `{"email": "x"}`} {
		claudeStatusMu.Lock()
		claudeStatusAt = time.Time{}
		claudeStatusMu.Unlock()
		claudeStatusOutput = func() []byte { return []byte(out) }
		if _, ok := find(All(), "claude"); !ok {
			t.Fatalf("claude still hidden after unreadable status %q", out)
		}
	}
}

func TestRemovedAccountIsHiddenFromUsageGate(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	forgetClaudeCredential()
	fakeClaudeStatus(t, `{"loggedIn": true, "email": "me@example.com"}`)

	if hidden("claude") {
		t.Fatal("claude hidden before it was removed")
	}
	if err := Delete("claude"); err != nil {
		t.Fatal(err)
	}
	if !hidden("claude") {
		t.Fatal("removed claude not reported as hidden")
	}
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("removed claude still listed")
	}
}

func TestClaudeUnreadableStatusKeepsOwnView(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	forgetClaudeCredential()

	// neither JSON nor a loggedIn field: magpie keeps listing the credential
	for _, out := range []string{"", "not json", `{"email": "x"}`} {
		fakeClaudeStatus(t, out)
		if _, ok := find(All(), "claude"); !ok {
			t.Fatalf("claude dropped on status %q", out)
		}
	}
}
