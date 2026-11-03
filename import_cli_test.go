package main

import (
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestIsImportLink(t *testing.T) {
	for _, s := range []string{
		"magpie://import?preset=deepseek",
		"magpie:import?preset=deepseek",
		"MAGPIE://import/?preset=deepseek",
		"Magpie:///import?preset=deepseek",
	} {
		if !isImportLink(s) {
			t.Errorf("isImportLink(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"import", "magpie", "providers", "https://usemagpie.ai/import?x=1", "", "magpi:x"} {
		if isImportLink(s) {
			t.Errorf("isImportLink(%q) = true, want false", s)
		}
	}
}

func TestConfirmed(t *testing.T) {
	yes := []string{"y", "Y", "yes", "YES", " yes \n", "Y\n"}
	no := []string{"", "\n", "n", "no", "ye", "sure", "yes please", "0"}
	for _, s := range yes {
		if !confirmed(s) {
			t.Errorf("confirmed(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if confirmed(s) {
			t.Errorf("confirmed(%q) = true, want false", s)
		}
	}
}

func TestImportSummaryShowsHostsAndModels(t *testing.T) {
	p, err := provider.ParseImport("magpie://import?name=Acme&chat=https://api.acme.example/v1&anthropic=https://api.acme.example&key=sk-abcdefghijklmnop&models=gpt-5.5,claude-sonnet-5")
	if err != nil {
		t.Fatal(err)
	}
	s := importSummary(p)
	for _, want := range []string{"Acme", "acme", "api.acme.example", "chat", "anthropic", "gpt-5.5, claude-sonnet-5", "sk-a…mnop"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "sk-abcdefghijklmnop") {
		t.Errorf("summary shows the full key:\n%s", s)
	}
	if strings.Contains(s, "responses") {
		t.Errorf("summary lists an endpoint the link does not have:\n%s", s)
	}
}

func TestImportSummaryWithoutKeyPointsToKeyCommand(t *testing.T) {
	p, err := provider.ParseImport("magpie://import?preset=deepseek")
	if err != nil {
		t.Fatal(err)
	}
	s := importSummary(p)
	if !strings.Contains(s, "magpie provider key deepseek <key>") {
		t.Errorf("summary does not explain how to add the key:\n%s", s)
	}
	if !strings.Contains(s, "api.deepseek.com") {
		t.Errorf("summary does not show the preset host:\n%s", s)
	}
}

func TestImportReplaceWarning(t *testing.T) {
	link, err := provider.ParseImport("magpie://import?name=Relay&chat=https://relay.example/v1&key=sk-new")
	if err != nil {
		t.Fatal(err)
	}
	existing := provider.Provider{ID: "relay", Name: "Old Relay", Key: "sk-old"}
	w := importReplaceWarning(link, existing)
	if !strings.Contains(strings.ToLower(w), "replace") || !strings.Contains(w, "This will replace the existing provider relay") {
		t.Errorf("warning = %q", w)
	}
	if strings.Contains(w, "dropped") {
		t.Errorf("link has a key, warning should not mention dropping it: %q", w)
	}

	keyless, _ := provider.ParseImport("magpie://import?name=Relay&chat=https://relay.example/v1")
	if w := importReplaceWarning(keyless, existing); !strings.Contains(w, "dropped") {
		t.Errorf("keyless link over a keyed provider should warn the key is dropped: %q", w)
	}
}

func TestImportCmdUsageErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, args := range [][]string{
		nil,
		{"-y"},
		{"--bogus", "magpie://import?preset=deepseek"},
		{"magpie://import?preset=deepseek", "magpie://import?preset=deepseek"},
	} {
		if err := importCmd(args); err == nil {
			t.Errorf("importCmd(%q) succeeded, want an error", args)
		}
	}
}

func TestImportCmdRejectsBadLinkAndSavesNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, link := range []string{
		"https://usemagpie.ai/import?preset=deepseek",
		"magpie://delete?id=x",
		"magpie://import",
		"magpie://import?chat=http://relay.example/v1&name=R",
	} {
		if err := importCmd([]string{"-y", link}); err == nil {
			t.Errorf("importCmd(-y, %q) succeeded, want rejection", link)
		}
	}
	if n := len(provider.All()); n != 0 {
		t.Errorf("%d providers saved after rejected links, want 0", n)
	}
}

// Without -y and with standard input that is not a terminal, nothing is
// confirmed, so nothing is added.
func TestImportCmdNonInteractiveRefusesWithoutYes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	w.Write([]byte("y\n")) // even a y on a pipe must not count
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	link := "magpie://import?name=Pipe&chat=http://127.0.0.1:9/v1&key=sk-pipe"
	if err := importCmd([]string{link}); err == nil {
		t.Fatal("importCmd without -y on a non-terminal succeeded, want refusal")
	}
	if n := len(provider.All()); n != 0 {
		t.Errorf("%d providers saved on refusal, want 0", n)
	}
}

// -y adds without asking, and an existing id is replaced.
func TestImportCmdYesSavesAndReplaces(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// 127.0.0.1:9 refuses connections at once, so the model fetch done after
	// saving fails fast and touches no real vendor.
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:9/v1", Key: "sk-old"}); err != nil {
		t.Fatal(err)
	}
	link := "magpie://import?name=Relay&chat=http://127.0.0.1:9/v1/&key=sk-new&models=a,%20b"
	if err := importCmd([]string{"-y", link}); err != nil {
		t.Fatal(err)
	}
	got, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "sk-new" || got.Chat != "http://127.0.0.1:9/v1" || strings.Join(got.Models, ",") != "a,b" {
		t.Errorf("provider after replace = %+v", got)
	}
	if n := len(provider.All()); n != 1 {
		t.Errorf("%d providers, want the replaced one only", n)
	}
}

// A link with no key is saved, and the provider is reported as not ready.
func TestImportCmdYesWithoutKeySaves(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := importCmd([]string{"-y", "magpie://import?name=No%20Key&chat=http://127.0.0.1:9/v1"}); err != nil {
		t.Fatal(err)
	}
	got := providerByID("no-key")
	if got == nil {
		t.Fatal("provider not saved")
	}
	if got.Ready() {
		t.Errorf("keyless provider reports ready")
	}
}
