package provider

import (
	"runtime"
	"strings"
	"testing"
)

// stubKeychain replaces the keychain probe for the duration of the test.
func stubKeychain(t *testing.T, has bool) {
	t.Helper()
	old := claudeKeychainHas
	claudeKeychainHas = func() bool { return has }
	t.Cleanup(func() { claudeKeychainHas = old })
	resetExcluded()
}

func TestClaudeKeychainItemIsExcludedWithoutReadingIt(t *testing.T) {
	isolate(t)
	stubKeychain(t, true)

	ex := Excluded()
	if len(ex) != 1 {
		t.Fatalf("want one exclusion for a keychain login, got %+v", ex)
	}
	if ex[0].Agent != "claude" {
		t.Errorf("exclusion agent = %q, want claude", ex[0].Agent)
	}
	if ex[0].Why == "" {
		t.Error("exclusion lacks a reason")
	}
	if !strings.Contains(ex[0].Why, "Anthropic") || !strings.Contains(ex[0].Why, "API key") {
		t.Errorf("reason should name the terms and the alternative: %q", ex[0].Why)
	}
	for _, s := range []string{"access", "token", "secret"} {
		if strings.Contains(strings.ToLower(ex[0].Why), s) {
			t.Errorf("reason mentions credentials (%q): %q", s, ex[0].Why)
		}
	}
	for _, p := range Accounts() {
		if p.ID == "claude" {
			t.Fatal("Claude Code must never be offered as a provider")
		}
	}
}

func TestClaudeKeychainAbsentGivesNoExclusion(t *testing.T) {
	isolate(t)
	stubKeychain(t, false)

	if ex := Excluded(); len(ex) != 0 {
		t.Fatalf("no Claude login should give no exclusions, got %+v", ex)
	}
}

// Off macOS the probe never runs `security` and reports false.
func TestKeychainProbeOffDarwinIsFalse(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("the probe really queries the keychain on darwin")
	}
	if keychainClaudeItem() {
		t.Fatal("keychain probe reported an item off darwin")
	}
}
