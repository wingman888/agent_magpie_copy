package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// importTestIsolate points config and home at temp dirs so nothing real is
// read or written.
func importTestIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// A closed local port: Fetch fails fast, so saving never waits on the network.
const testRelayLink = "magpie://import?name=Test%20Relay&chat=http://127.0.0.1:1/v1"

func runImport(t *testing.T, input string, interactive bool, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := importLink(args, strings.NewReader(input), &out, interactive)
	return out.String(), err
}

func savedKey(t *testing.T, id string) (string, bool) {
	t.Helper()
	for _, p := range provider.All() {
		if p.ID == id {
			return p.Key, true
		}
	}
	return "", false
}

func TestImportLinkDetection(t *testing.T) {
	for _, s := range []string{"magpie:import?preset=deepseek", "MAGPIE://import", "Magpie:"} {
		if !isImportLink(s) {
			t.Errorf("isImportLink(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"import", "magpi:import", "https://magpie", "", "-y"} {
		if isImportLink(s) {
			t.Errorf("isImportLink(%q) = true, want false", s)
		}
	}
}

func TestImportYesSavesWithoutAsking(t *testing.T) {
	importTestIsolate(t)
	out, err := runImport(t, "", false, "-y", testRelayLink+"&key=sk-test-1234")
	if err != nil {
		t.Fatalf("import -y: %v\n%s", err, out)
	}
	if key, ok := savedKey(t, "test-relay"); !ok || key != "sk-test-1234" {
		t.Fatalf("saved = %v, key %q; want saved with sk-test-1234", ok, key)
	}
	if !strings.Contains(out, "127.0.0.1") || !strings.Contains(out, "prompts and key sent to") {
		t.Errorf("summary does not name the receiving host:\n%s", out)
	}
	if strings.Contains(out, "Add it?") {
		t.Errorf("-y should not ask:\n%s", out)
	}
}

func TestImportConfirmYes(t *testing.T) {
	for _, in := range []string{"y\n", "Y\n", "yes\n", "YES\n"} {
		importTestIsolate(t)
		out, err := runImport(t, in, true, testRelayLink+"&key=sk-a")
		if err != nil {
			t.Fatalf("input %q: %v\n%s", in, err, out)
		}
		if _, ok := savedKey(t, "test-relay"); !ok {
			t.Errorf("input %q: not saved", in)
		}
	}
}

func TestImportConfirmNoOrEnterSavesNothing(t *testing.T) {
	for _, in := range []string{"n\n", "\n", "", "nope\n"} {
		importTestIsolate(t)
		out, err := runImport(t, in, true, testRelayLink+"&key=sk-a")
		if err == nil || !strings.Contains(err.Error(), "nothing added") {
			t.Errorf("input %q: err = %v, want 'nothing added'", in, err)
		}
		if !strings.Contains(out, "Add it? [y/N]") {
			t.Errorf("input %q: no prompt shown:\n%s", in, out)
		}
		if _, ok := savedKey(t, "test-relay"); ok {
			t.Errorf("input %q: provider was saved", in)
		}
	}
}

func TestImportNonInteractiveWithoutYesRefuses(t *testing.T) {
	importTestIsolate(t)
	// Even if input is offered, a non-terminal stdin must not be read as consent.
	out, err := runImport(t, "y\n", false, testRelayLink+"&key=sk-a")
	if err == nil || !strings.Contains(err.Error(), "-y") || !strings.Contains(err.Error(), "nothing added") {
		t.Fatalf("err = %v, want a refusal that mentions -y and nothing added", err)
	}
	if strings.Contains(out, "Add it?") {
		t.Errorf("should not prompt without a terminal:\n%s", out)
	}
	if _, ok := savedKey(t, "test-relay"); ok {
		t.Fatal("provider was saved without confirmation")
	}
}

func TestImportReplaceWarning(t *testing.T) {
	importTestIsolate(t)
	if _, err := runImport(t, "", false, "-y", testRelayLink+"&key=sk-old"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Declined: warning shown before the question, original kept.
	out, err := runImport(t, "n\n", true, testRelayLink+"&key=sk-new")
	if err == nil {
		t.Fatal("declined import returned no error")
	}
	warn := strings.Index(out, "replace")
	ask := strings.Index(out, "Add it? [y/N]")
	if warn < 0 || ask < 0 || warn > ask {
		t.Fatalf("want replace warning before the question:\n%s", out)
	}
	if key, _ := savedKey(t, "test-relay"); key != "sk-old" {
		t.Errorf("declined replace changed key to %q", key)
	}

	// Confirmed with -y: warning still printed, original replaced.
	out, err = runImport(t, "", false, "-y", testRelayLink+"&key=sk-new")
	if err != nil {
		t.Fatalf("replace -y: %v\n%s", err, out)
	}
	if !strings.Contains(strings.ToLower(out), "replace") {
		t.Errorf("replace warning missing with -y:\n%s", out)
	}
	if key, _ := savedKey(t, "test-relay"); key != "sk-new" {
		t.Errorf("key after replace = %q, want sk-new", key)
	}
}

// A remote keyless link is confirmed like any other, saved without a key, and
// the output says how to add the key. The endpoint is an IPv6 loopback address
// on a closed port: it is not a keyOptional host, so Ready() is false, and the
// model fetch fails fast without touching the network.
const testKeylessRemoteLink = "magpie://import?name=Keyless&chat=https://[::1]:1/v1"

func TestImportKeylessRemoteSavesAfterConfirm(t *testing.T) {
	importTestIsolate(t)
	out, err := runImport(t, "y\n", true, testKeylessRemoteLink)
	if err != nil {
		t.Fatalf("keyless import: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Add it?") {
		t.Errorf("keyless link was not shown for confirmation:\n%s", out)
	}
	if !strings.Contains(out, "no key yet") {
		t.Errorf("summary does not say there is no key yet:\n%s", out)
	}
	if key, ok := savedKey(t, "keyless"); !ok || key != "" {
		t.Fatalf("saved = %v, key %q; want saved with no key", ok, key)
	}
	if !strings.Contains(out, "could not fetch models") {
		t.Errorf("fetch failure not reported as a warning:\n%s", out)
	}
	if !strings.Contains(out, "magpie provider key keyless <key>") {
		t.Errorf("no key hint after a keyless import:\n%s", out)
	}
}

func TestImportKeylessRemoteDeclinedSavesNothing(t *testing.T) {
	importTestIsolate(t)
	out, err := runImport(t, "n\n", true, testKeylessRemoteLink)
	if err == nil || !strings.Contains(err.Error(), "nothing added") {
		t.Fatalf("err = %v, want 'nothing added'\n%s", err, out)
	}
	if _, ok := savedKey(t, "keyless"); ok {
		t.Fatal("declined keyless link was saved")
	}
}

func TestImportKeylessLocalSavesAndNeedsNoHint(t *testing.T) {
	importTestIsolate(t)
	// Local servers are usable without a key, so no key hint is due.
	out, err := runImport(t, "", false, "-y", "magpie://import?name=Ollama&chat=http://127.0.0.1:1/v1")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if _, ok := savedKey(t, "ollama"); !ok {
		t.Fatal("local keyless provider not saved")
	}
	if strings.Contains(out, "magpie provider key") {
		t.Errorf("unexpected key hint for a usable provider:\n%s", out)
	}
}

func TestImportMissingLinkShowsUsage(t *testing.T) {
	importTestIsolate(t)
	_, err := runImport(t, "", false)
	if err == nil || !strings.Contains(err.Error(), "usage: magpie import") {
		t.Fatalf("err = %v, want usage", err)
	}
	_, err = runImport(t, "", false, "-y")
	if err == nil || !strings.Contains(err.Error(), "usage: magpie import") {
		t.Fatalf("flag only: err = %v, want usage", err)
	}
}

func TestImportRejectedLinkSavesNothing(t *testing.T) {
	importTestIsolate(t)
	for _, link := range []string{
		"https://usemagpie.ai/import?name=x&chat=https://a.example/v1",
		"magpie://import?name=magpie&chat=https://a.example/v1",
		"magpie://import?preset=nope",
	} {
		if _, err := runImport(t, "", false, "-y", link); err == nil {
			t.Errorf("%q: accepted", link)
		}
	}
	if n := len(provider.All()); n != 0 {
		t.Errorf("%d providers saved after rejected links", n)
	}
}

func TestImportRejectsExtraArgs(t *testing.T) {
	importTestIsolate(t)
	if _, err := runImport(t, "", false, "-y", testRelayLink, testRelayLink); err == nil {
		t.Error("two links accepted")
	}
	if _, err := runImport(t, "", false, "--bogus", testRelayLink); err == nil {
		t.Error("unknown flag accepted")
	}
}

// Overwriting a keyed provider with a keyless link warns about the loss of the
// old key, and the confirmed import really replaces it.
func TestImportReplaceKeylessWarnsAndOverwrites(t *testing.T) {
	importTestIsolate(t)
	if _, err := runImport(t, "", false, "-y", testRelayLink+"&key=sk-old"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	out, err := runImport(t, "y\n", true, testRelayLink)
	if err != nil {
		t.Fatalf("keyless replace: %v\n%s", err, out)
	}
	if !strings.Contains(out, "This will replace the existing provider test-relay") {
		t.Errorf("replace warning missing:\n%s", out)
	}
	if !strings.Contains(out, "will be discarded") || !strings.Contains(out, "(the new provider has no key)") {
		t.Errorf("warning does not say what is lost:\n%s", out)
	}
	if strings.Index(out, "replace") > strings.Index(out, "Add it? [y/N]") {
		t.Errorf("warning printed after the question:\n%s", out)
	}
	if key, ok := savedKey(t, "test-relay"); !ok || key != "" {
		t.Errorf("after keyless replace: saved=%v key=%q, want saved with empty key", ok, key)
	}
}

// A link whose id is owned by a signed-in agent account is refused before any
// summary or question. The Codex account is detected from ~/.codex/auth.json.
func TestImportAccountIDRefusedBeforeConfirm(t *testing.T) {
	importTestIsolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	auth := `{"auth_mode":"chatgpt","tokens":{"access_token":"tok-test","refresh_token":"r","id_token":""}}`
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runImport(t, "y\n", true, "magpie://import?name=Codex&chat=https://relay.example/v1&key=sk-x")
	if err == nil || !strings.Contains(err.Error(), "account") || !strings.Contains(err.Error(), "id=") {
		t.Fatalf("err = %v, want refusal naming the account and id=", err)
	}
	if strings.Contains(out, "Add it?") || strings.Contains(out, "prompts and key sent to") {
		t.Errorf("summary or question shown for an account id:\n%s", out)
	}
	for _, p := range provider.All() {
		if p.ID == "codex" && p.Account == nil {
			t.Errorf("a stored codex provider was created")
		}
	}
	if _, err := os.Stat(provider.Path()); err == nil {
		t.Errorf("providers file was written")
	}
}

// /dev/null is a character device but never answers a prompt.
func TestIsTerminalRejectsDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("isTerminal(os.DevNull) = true, want false")
	}
}

// Without a terminal and without -y, a keyless remote link is still refused.
func TestImportDevNullStdinNeedsYes(t *testing.T) {
	importTestIsolate(t)
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	defer f.Close()
	var out bytes.Buffer
	err = importLink([]string{testRelayLink + "&key=sk-a"}, f, &out, isTerminal(f))
	if err == nil || !strings.Contains(err.Error(), "-y") {
		t.Fatalf("err = %v, want refusal asking for -y", err)
	}
	if _, ok := savedKey(t, "test-relay"); ok {
		t.Fatal("saved without confirmation on /dev/null stdin")
	}
}
