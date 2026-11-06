package main

import (
	"bytes"
	"strings"
	"testing"
)

// A link for a remote endpoint with no key: -y must save it and tell the user
// how to add the key. Fetching models fails quietly (no network in tests), so
// only the saved state and the printed hint are checked.
const testNoKeyLink = "magpie://import?name=Open%20Relay&chat=https://relay.example/v1"

func TestImportNoKeyRemoteSavesAndHints(t *testing.T) {
	isolateConfig(t)
	var out bytes.Buffer
	if err := runImport([]string{"-y", testNoKeyLink}, strings.NewReader(""), &out, false); err != nil {
		t.Fatalf("no-key import failed: %v\n%s", err, out.String())
	}
	if _, ok := savedKey(t, "open-relay"); !ok {
		t.Fatalf("no-key provider not saved:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "magpie provider key open-relay") {
		t.Fatalf("no hint to add the key:\n%s", out.String())
	}
}
