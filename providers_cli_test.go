package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// ansi strips colour codes, so assertions hold on a colour terminal too.
var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

// cliIsolate points HOME, XDG_CONFIG_HOME and CLAUDE_CONFIG_DIR at fresh
// temp directories, so no real login on the machine leaks into the output.
func cliIsolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "no-claude"))
	return home
}

// cliCapture runs fn and returns what it printed to stdout, colour removed.
func cliCapture(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	ferr := fn()
	os.Stdout = old
	w.Close()
	out := <-done
	r.Close()
	return ansi.ReplaceAllString(out, ""), ferr
}

func cliWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// cliJWT builds an unsigned JWT carrying the given payload.
func cliJWT(t *testing.T, claims any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(b) + ".x"
}

// cliCodexLogin writes a ChatGPT-mode Codex login under home.
func cliCodexLogin(t *testing.T, home string) {
	t.Helper()
	idTok := cliJWT(t, map[string]any{
		"email":                       "user@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
	})
	auth := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":     idTok,
			"access_token": "secret-access-token",
			"account_id":   "acct-1",
		},
	}
	b, _ := json.Marshal(auth)
	cliWrite(t, filepath.Join(home, ".codex", "auth.json"), string(b))
}

// cliCopilotLogin writes a GitHub Copilot login under XDG_CONFIG_HOME.
func cliCopilotLogin(t *testing.T) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot")
	cliWrite(t, filepath.Join(dir, "hosts.json"),
		`{"github.com:octocat":{"user":"octocat","oauth_token":"gho_secret"}}`)
}

func TestProvidersListsAccountsWithoutCustomMark(t *testing.T) {
	home := cliIsolate(t)
	cliCodexLogin(t, home)
	cliCopilotLogin(t)

	out, err := cliCapture(t, providers)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"signed in as user@example.com", "signed in as octocat"} {
		if !strings.Contains(out, want) {
			t.Errorf("provider list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "custom") {
		t.Errorf("account providers must not carry the custom mark:\n%s", out)
	}
	if strings.Contains(out, "is signed in but not offered") {
		t.Errorf("no Claude login, so no exclusion line expected:\n%s", out)
	}
}

func TestProvidersNoClaudeLineWithoutClaudeLogin(t *testing.T) {
	cliIsolate(t)

	out, err := cliCapture(t, providers)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "is signed in but not offered") {
		t.Fatalf("exclusion line without a Claude login:\n%s", out)
	}
}

func TestProvidersClaudeExclusionClosesList(t *testing.T) {
	cliIsolate(t)
	// Written before the first call: the exclusion cache is keyed by the
	// environment, so a fresh environment gives a fresh answer.
	cliWrite(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json"), `{"claudeAiOauth":{"accessToken":"do-not-read"}}`)

	out, err := cliCapture(t, providers)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "Claude Code is signed in but not offered: ") {
		t.Errorf("exclusion line should close the list, got last line %q:\n%s", last, out)
	}
	if !strings.Contains(last, "Anthropic") || !strings.Contains(last, "API key") {
		t.Errorf("exclusion line should give the reason and the alternative: %q", last)
	}
	for _, p := range provider.All() {
		if strings.Contains(p.ID, "claude") {
			t.Errorf("Claude Code must never be offered as a provider, found %q", p.ID)
		}
	}
}

func TestProvidersExclusionShownWithNoProviders(t *testing.T) {
	cliIsolate(t)
	cliWrite(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json"), "{}")

	out, err := cliCapture(t, providers)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Claude Code is signed in but not offered: ") {
		t.Errorf("exclusion line missing when there are no providers:\n%s", out)
	}
}

func TestProviderDetailShowsAccountLine(t *testing.T) {
	home := cliIsolate(t)
	cliCodexLogin(t, home)

	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	out, err := cliCapture(t, func() error { return showProvider(*p) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "account") || !strings.Contains(out, "user@example.com (plan: pro, from codex)") {
		t.Errorf("detail lacks the account line:\n%s", out)
	}
	if strings.Contains(out, "custom") {
		t.Errorf("account detail must not carry the custom mark:\n%s", out)
	}
	if strings.Contains(out, "secret-access-token") {
		t.Errorf("detail leaked the access token:\n%s", out)
	}
}

func TestProviderDetailAccountWithoutPlan(t *testing.T) {
	cliIsolate(t)
	cliCopilotLogin(t)

	p, err := provider.Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	out, err := cliCapture(t, func() error { return showProvider(*p) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "octocat (from copilot)") {
		t.Errorf("detail lacks the account line without a plan:\n%s", out)
	}
}

func TestAccountProviderJSONCarriesNoToken(t *testing.T) {
	home := cliIsolate(t)
	cliCodexLogin(t, home)
	cliCopilotLogin(t)

	for _, id := range []string{"codex", "copilot"} {
		p, err := provider.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.Contains(s, `"account":{"agent":"`+id+`"`) {
			t.Errorf("%s JSON lacks the account object: %s", id, s)
		}
		for _, secret := range []string{"secret-access-token", "gho_secret", "acct-1"} {
			if strings.Contains(s, secret) {
				t.Errorf("%s JSON leaked a credential %q: %s", id, secret, s)
			}
		}
	}

	if ex := provider.Excluded(); len(ex) != 0 {
		t.Errorf("no Claude login should give no exclusions, got %+v", ex)
	}
}

func TestProviderRmOnAccountClearsSelectionOnly(t *testing.T) {
	home := cliIsolate(t)
	cliCodexLogin(t, home)

	out, err := cliCapture(t, func() error { return providerCmd([]string{"provider", "rm", "codex"}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "removed") {
		t.Errorf("rm on an account must not claim it removed the provider:\n%s", out)
	}
	if !strings.Contains(out, "sign out in Codex") {
		t.Errorf("rm should say how to remove the provider:\n%s", out)
	}
	if _, err := provider.Find("codex"); err != nil {
		t.Errorf("account provider should survive rm while signed in: %v", err)
	}
}

func TestProviderKeyRefusesAccount(t *testing.T) {
	home := cliIsolate(t)
	cliCodexLogin(t, home)

	_, err := cliCapture(t, func() error { return providerCmd([]string{"provider", "key", "codex", "sk-typed"}) })
	if err == nil || !strings.Contains(err.Error(), "needs no key") {
		t.Fatalf("key on an account should be refused, got %v", err)
	}
}
