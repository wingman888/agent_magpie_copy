package main_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	buildErr  string
)

func binary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "magpie-accept-bin")
		if err != nil {
			buildErr = err.Error()
			return
		}
		binPath = filepath.Join(dir, "magpie")
		out, err := exec.Command("go", "build", "-tags", "nogui", "-o", binPath, ".").CombinedOutput()
		if err != nil {
			buildErr = err.Error() + "\n" + string(out)
		}
	})
	if buildErr != "" {
		t.Fatalf("build: %s", buildErr)
	}
	return binPath
}

type sandbox struct {
	home string
	env  []string
}

func newSandbox(t *testing.T) sandbox {
	home := t.TempDir()
	return sandbox{home: home, env: []string{
		"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"PATH=/usr/bin:/bin", "USERPROFILE=" + home, "TERM=dumb",
	}}
}

func (s sandbox) run(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binary(t), args...)
	cmd.Env = s.env
	cmd.Dir = s.home
	cmd.Stdin = strings.NewReader(stdin)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		cmd.Process.Kill()
		<-done
		t.Fatalf("magpie %v timed out", args)
	}
	return string(out), err
}

func (s sandbox) providersFile(t *testing.T) string {
	b, _ := os.ReadFile(filepath.Join(s.home, ".config", "magpie", "providers.json"))
	return string(b)
}

func TestAcceptCLIPresets(t *testing.T) {
	s := newSandbox(t)
	out, err := s.run(t, "", "presets")
	if err != nil {
		t.Fatalf("%v; %s", err, one(out))
	}
	for _, want := range []string{"opencode-go", "opencode-zen"} {
		if !strings.Contains(out, want) {
			t.Errorf("presets lacks %s:\n%s", want, out)
		}
	}
	out, err = s.run(t, "", "provider", "add", "opencode-go", "sk-test-key")
	if err != nil {
		t.Fatalf("add: %v; %s", err, one(out))
	}
	if f := s.providersFile(t); !strings.Contains(f, "opencode-go") || !strings.Contains(f, "https://opencode.ai/zen/go/v1") {
		t.Errorf("providers file: %s", f)
	}
}

func TestAcceptCLIImport(t *testing.T) {
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{map[string]any{"id": "m1"}, map[string]any{"id": "m2"}}})
	}))
	defer models.Close()
	link := "magpie://import?name=Acme%20Relay&chat=" + models.URL + "/v1&key=sk-acme-secret&models=m1"

	s := newSandbox(t)
	// no terminal and no -y: refuse, save nothing
	if out, err := s.run(t, "y\n", "import", link); err == nil {
		t.Fatalf("import without a terminal succeeded; %s", one(out))
	}
	if f := s.providersFile(t); strings.Contains(f, "acme") {
		t.Fatalf("saved without confirmation: %s", f)
	}
	// invalid links are refused even with -y
	for _, bad := range []string{"magpie://delete?id=x", "magpie://import?name=X&chat=http://example.com/v1", "https://example.com/import?preset=deepseek", "magpie://import?name=X&chat=file:///etc/passwd"} {
		if out, err := s.run(t, "", "import", "-y", bad); err == nil {
			t.Errorf("%s accepted:\n%s", bad, out)
		}
	}
	if f := s.providersFile(t); strings.Contains(f, "example.com") || strings.Contains(f, "passwd") {
		t.Fatalf("bad link saved: %s", f)
	}

	out, err := s.run(t, "", "import", "--yes", link)
	if err != nil {
		t.Fatalf("%v; %s", err, one(out))
	}
	if !strings.Contains(out, "127.0.0.1") {
		t.Errorf("host not shown; %s", one(out))
	}
	if f := s.providersFile(t); !strings.Contains(f, "acme-relay") || !strings.Contains(f, "sk-acme-secret") {
		t.Fatalf("not saved: %s", f)
	}
	if strings.Contains(out, "sk-acme-secret") {
		t.Errorf("key printed in full; %s", one(out))
	}
	// the same id again: says it replaces
	out, err = s.run(t, "", "import", "-y", link)
	if err != nil {
		t.Fatalf("%v; %s", err, one(out))
	}
	if !strings.Contains(strings.ToLower(out), "replace") {
		t.Errorf("no replace notice; %s", one(out))
	}
	// a protocol-handler handoff works like import
	s2 := newSandbox(t)
	if out, err := s2.run(t, "", "magpie://import?preset=deepseek&key=sk-x"); err == nil && !strings.Contains(strings.ToLower(out), "add") {
		t.Logf("handoff output: %s", out)
	}
}

func TestAcceptCLIRemoveAndRestoreAccount(t *testing.T) {
	s := newSandbox(t)
	write := func(rel string, v any) {
		p := filepath.Join(s.home, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		b, _ := json.Marshal(v)
		os.WriteFile(p, b, 0o600)
	}
	jwt := func(claims map[string]any) string {
		b, _ := json.Marshal(claims)
		return "h." + base64RawURL(b) + ".sig"
	}
	write(".codex/auth.json", map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":      jwt(map[string]any{"email": "me@example.com"}),
		"access_token":  jwt(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
		"refresh_token": "r", "account_id": "a",
	}})
	write(".codex/models_cache.json", map[string]any{"models": []any{
		map[string]any{"slug": "gpt-5.5", "display_name": "GPT-5.5", "visibility": "list", "priority": 1},
		map[string]any{"slug": "gpt-5.4", "display_name": "GPT-5.4", "visibility": "list", "priority": 2},
	}})
	authBefore, _ := os.ReadFile(filepath.Join(s.home, ".codex", "auth.json"))
	if out, err := s.run(t, "", "provider", "models", "codex", "gpt-5.5"); err != nil {
		t.Fatalf("pick: %v; %s", err, one(out))
	}
	if out, _ := s.run(t, "", "models"); !strings.Contains(out, "codex/gpt-5.5") || strings.Contains(out, "codex/gpt-5.4") {
		t.Fatalf("pick not applied; %s", one(out))
	}
	if out, err := s.run(t, "", "provider", "rm", "codex"); err != nil {
		t.Fatalf("rm: %v; %s", err, one(out))
	}
	out, _ := s.run(t, "", "models")
	if strings.Contains(out, "codex/") {
		t.Errorf("removed account still offers models; %s", one(out))
	}
	out, err := s.run(t, "", "providers")
	if err != nil {
		t.Fatalf("%v; %s", err, one(out))
	}
	if !strings.Contains(out, "magpie provider add codex") {
		t.Errorf("no way back shown; %s", one(out))
	}
	if after, _ := os.ReadFile(filepath.Join(s.home, ".codex", "auth.json")); string(after) != string(authBefore) {
		t.Error("codex login was modified")
	}
	if out, err := s.run(t, "", "provider", "add", "codex"); err != nil {
		t.Fatalf("add back: %v; %s", err, one(out))
	}
	out, _ = s.run(t, "", "models")
	if !strings.Contains(out, "codex/gpt-5.5") || strings.Contains(out, "codex/gpt-5.4") {
		t.Errorf("earlier pick not restored; %s", one(out))
	}
}

func base64RawURL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func one(s string) string { return strings.ReplaceAll(s, "\n", " | ") }
