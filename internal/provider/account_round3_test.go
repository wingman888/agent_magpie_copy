package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A top-level "null" in auth.json unmarshals to a nil map; writing the tokens
// must start from an empty map rather than panic.
func TestWriteCodexTokensNilMapsDoNotPanic(t *testing.T) {
	home := acctIsolate(t)
	for _, body := range []string{"null", `{"tokens":null}`} {
		dir := filepath.Join(home, ".codex")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "auth.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeCodexTokens(codexCreds{access: "a1", refresh: "r1", idToken: "i1"}); err != nil {
			t.Fatalf("writeCodexTokens(%s) error = %v", body, err)
		}
		raw, err := os.ReadFile(path)
		acctMustNoErr(t, err)
		var top map[string]map[string]any
		acctMustNoErr(t, json.Unmarshal(raw, &top))
		if top["tokens"]["access_token"] != "a1" || top["tokens"]["refresh_token"] != "r1" {
			t.Fatalf("tokens not written for %s: %s", body, raw)
		}
	}
}

// With no home directory there is nowhere to look, so nothing is signed in and
// no path is made relative.
func TestEmptyHomeMeansNotSignedIn(t *testing.T) {
	acctIsolate(t)
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	if p := codexAuthPath(); p != "" {
		t.Fatalf("codexAuthPath() = %q, want empty", p)
	}
	if _, ok := readCodex(); ok {
		t.Fatal("readCodex() signed in without a home directory")
	}
	if _, ok := readCopilot(); ok {
		t.Fatal("readCopilot() signed in without a home directory")
	}
	if d := claudeDir(); d != "" {
		t.Fatalf("claudeDir() = %q, want empty", d)
	}
	if claudeSignedIn() {
		t.Fatal("claudeSignedIn() true without a home directory")
	}
	if got := Accounts(); len(got) != 0 {
		t.Fatalf("Accounts() = %+v, want none", got)
	}
	if err := writeCodexTokens(codexCreds{access: "a"}); err == nil {
		t.Fatal("writeCodexTokens() wrote without a home directory")
	}
}
