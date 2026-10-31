package main_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

func dialBin(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "dialbin")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "dial")
		out, err := exec.Command("go", "build", "-tags", "nogui", "-o", binPath, ".").CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("%v: %s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binPath
}

func jwt(m map[string]any) string {
	b, _ := json.Marshal(m)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

func env(t *testing.T, claude bool) (string, []string) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	auth := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":"r","account_id":"acct-1"}}`,
		jwt(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}}),
		jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}))
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(auth), 0o600)
	os.MkdirAll(filepath.Join(home, ".config", "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(home, ".config", "github-copilot", "apps.json"), []byte(`{"github.com:x":{"user":"octocat","oauth_token":"gho_x"}}`), 0o600)
	if claude {
		os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
		os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"x"}}`), 0o600)
	}
	e := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "USERPROFILE=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"), "NO_COLOR=1", "TERM=dumb"}
	return home, e
}

func runDial(t *testing.T, e []string, args ...string) string {
	t.Helper()
	c := exec.Command(dialBin(t), args...)
	c.Env = e
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("dial %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func lineWith(out, needle string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	return ""
}

func TestAcceptCLIListsAccountsAndClaudeNotice(t *testing.T) {
	_, e := env(t, true)
	out := runDial(t, e, "providers")
	cl := lineWith(out, "me@example.com")
	if cl == "" || !strings.Contains(cl, "signed in as me@example.com") || strings.Contains(cl, "custom") {
		t.Fatalf("codex row: %q\n%s", cl, out)
	}
	gl := lineWith(out, "octocat")
	if gl == "" || !strings.Contains(gl, "signed in as octocat") || strings.Contains(gl, "custom") {
		t.Fatalf("copilot row: %q\n%s", gl, out)
	}
	if !strings.Contains(out, "Claude Code is signed in but not offered: ") {
		t.Fatalf("missing claude notice:\n%s", out)
	}
	last := lineWith(out, "Claude Code is signed in")
	if !strings.Contains(last, "API") && !strings.Contains(strings.ToLower(last), "api key") {
		t.Fatalf("notice lacks the alternative: %q", last)
	}
	if lineWith(out, "signed in as claude") != "" {
		t.Fatalf("claude offered:\n%s", out)
	}
}

func TestAcceptCLINoClaudeNoNotice(t *testing.T) {
	_, e := env(t, false)
	out := runDial(t, e, "providers")
	if strings.Contains(out, "not offered") {
		t.Fatalf("unexpected notice:\n%s", out)
	}
	if !strings.Contains(out, "signed in as me@example.com") {
		t.Fatalf("no codex:\n%s", out)
	}
}

func TestAcceptCLIProviderDetail(t *testing.T) {
	_, e := env(t, false)
	out := runDial(t, e, "provider", "codex")
	l := lineWith(out, "account")
	if l == "" || !strings.Contains(l, "me@example.com") || !strings.Contains(l, "pro") || !strings.Contains(l, "codex") {
		t.Fatalf("account line: %q\n%s", l, out)
	}
	if strings.Contains(out, "custom") {
		t.Fatalf("custom marker:\n%s", out)
	}
	// choosing models stores only the picks
	runDial(t, e, "provider", "models", "codex", "gpt-5.5")
	models := runDial(t, e, "models")
	if !strings.Contains(models, "codex/gpt-5.5") && !strings.Contains(models, "gpt-5.5") {
		t.Fatalf("models:\n%s", models)
	}
}
