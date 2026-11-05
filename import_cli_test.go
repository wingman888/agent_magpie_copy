package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// isolate points the provider config at an empty temp dir.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

const relayLink = "magpie://import?name=My%20Relay&chat=https://relay.example/v1/&key=sk-abcdefghijkl"

func TestIsImportLink(t *testing.T) {
	for _, s := range []string{"magpie://import?preset=deepseek", "magpie:import?x=1", "MAGPIE:///import", "Magpie:"} {
		if !isImportLink(s) {
			t.Errorf("isImportLink(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"magpie", "import", "deepseek", "https://magpie.example", "claude"} {
		if isImportLink(s) {
			t.Errorf("isImportLink(%q) = true, want false", s)
		}
	}
}

func TestImportDeclinedSavesNothing(t *testing.T) {
	isolate(t)
	for _, answer := range []string{"n\n", "\n", "", "maybe\n"} {
		var out bytes.Buffer
		err := runImport(relayLink, false, strings.NewReader(answer), &out, true)
		if err == nil || !strings.Contains(err.Error(), "nothing added") {
			t.Fatalf("answer %q: err = %v, want nothing added", answer, err)
		}
		if len(provider.All()) != 0 {
			t.Fatalf("answer %q: a provider was saved", answer)
		}
	}
}

func TestImportSummaryShowsHostsAndKey(t *testing.T) {
	isolate(t)
	var out bytes.Buffer
	_ = runImport(relayLink, false, strings.NewReader("n\n"), &out, true)
	s := out.String()
	for _, want := range []string{"relay.example", "sk-a", "Add it? [y/N]"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "sk-abcdefghijkl") {
		t.Errorf("summary shows the full key:\n%s", s)
	}
	if strings.Index(s, "relay.example") > strings.Index(s, "Add it?") {
		t.Errorf("hosts should appear before the question")
	}
}

func TestImportYesOnlyAnswers(t *testing.T) {
	isolate(t)
	var out bytes.Buffer
	for _, answer := range []string{"yes\n", "Y\n", " y \n"} {
		if err := runImport(relayLink, false, strings.NewReader(answer), &out, true); err == nil {
			// accepted: a save happened, so reset for the next answer
			if err := provider.Delete("my-relay"); err != nil {
				t.Fatalf("cleanup: %v", err)
			}
			continue
		} else {
			t.Fatalf("answer %q rejected: %v", answer, err)
		}
	}
}

func TestImportNonInteractiveRefused(t *testing.T) {
	isolate(t)
	var out bytes.Buffer
	err := runImport(relayLink, false, strings.NewReader("y\n"), &out, false)
	if err == nil || !strings.Contains(err.Error(), "-y") {
		t.Fatalf("err = %v, want refusal mentioning -y", err)
	}
	if len(provider.All()) != 0 {
		t.Fatalf("a provider was saved without confirmation")
	}
}

func TestImportWithYesSavesWithoutAsking(t *testing.T) {
	isolate(t)
	var out bytes.Buffer
	link := "magpie://import?name=Local&chat=http://127.0.0.1:1/v1"
	if err := runImport(link, true, nil, &out, false); err != nil {
		t.Fatalf("runImport -y: %v", err)
	}
	p, err := provider.Find("local")
	if err != nil {
		t.Fatalf("provider not saved: %v", err)
	}
	if p.Chat != "http://127.0.0.1:1/v1" {
		t.Errorf("Chat = %q", p.Chat)
	}
	if strings.Contains(out.String(), "Add it?") {
		t.Errorf("asked a question despite -y")
	}
}

func TestImportKeylessRemoteRefused(t *testing.T) {
	isolate(t)
	var out bytes.Buffer
	link := "magpie://import?name=Remote&chat=https://relay.example/v1"
	err := runImport(link, true, nil, &out, false)
	if err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatalf("err = %v, want refusal about the missing key", err)
	}
	if strings.Contains(out.String(), "Add it?") {
		t.Errorf("asked before refusing")
	}
	if len(provider.All()) != 0 {
		t.Fatalf("a keyless remote provider was saved")
	}
}

func TestImportReplaceWarning(t *testing.T) {
	isolate(t)
	if err := provider.Save(provider.Provider{ID: "my-relay", Name: "Old", Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var out bytes.Buffer
	_ = runImport(relayLink, false, strings.NewReader("n\n"), &out, true)
	s := out.String()
	w := strings.Index(s, "replace")
	if w < 0 || !strings.Contains(strings.ToLower(s), "replace the existing provider my-relay") {
		t.Fatalf("missing replace warning:\n%s", s)
	}
	if w > strings.Index(s, "Add it?") {
		t.Errorf("replace warning must come before the question")
	}
	p, err := provider.Find("my-relay")
	if err != nil || p.Name != "Old" {
		t.Errorf("declined import changed the existing provider: %+v %v", p, err)
	}
}

func TestImportCLIUsage(t *testing.T) {
	isolate(t)
	if err := importCmd(nil); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("no link: err = %v, want usage", err)
	}
	if err := importCmd([]string{"-x", relayLink}); err == nil {
		t.Errorf("unknown flag accepted")
	}
	if err := importCmd([]string{"magpie://delete?id=x", "-y"}); err == nil {
		t.Errorf("non-import action accepted")
	}
	if len(provider.All()) != 0 {
		t.Errorf("a rejected link saved something")
	}
}
