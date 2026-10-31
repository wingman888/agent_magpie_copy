package main_test

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var origEnv = os.Environ()

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

func magpieBin(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "magpie-accept-")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "magpie")
		cmd := exec.Command("go", "build", "-tags", "nogui", "-o", binPath, "github.com/yetone/magpie")
		cmd.Env = append(append([]string{}, origEnv...), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("build: %v\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatal(binErr)
	}
	return binPath
}

type sandbox struct {
	t    *testing.T
	home string
}

func newSandbox(t *testing.T) *sandbox {
	home := t.TempDir()
	return &sandbox{t: t, home: home}
}

func (s *sandbox) run(args ...string) (string, error) {
	s.t.Helper()
	cmd := exec.Command(magpieBin(s.t), args...)
	cmd.Env = append(append([]string{}, origEnv...),
		"HOME="+s.home,
		"XDG_CONFIG_HOME="+filepath.Join(s.home, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(s.home, ".cache"),
		"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost")
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		cmd.Process.Kill()
		<-done
		s.t.Fatalf("magpie %v timed out", args)
	}
	return string(out), err
}

func (s *sandbox) must(args ...string) string {
	s.t.Helper()
	out, err := s.run(args...)
	if err != nil {
		s.t.Fatalf("magpie %v: %v\n%s", args, err, out)
	}
	return out
}

func (s *sandbox) providers() []map[string]any {
	s.t.Helper()
	b, err := os.ReadFile(filepath.Join(s.home, ".config", "magpie", "providers.json"))
	if err != nil {
		s.t.Fatal(err)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		s.t.Fatal(err)
	}
	return f.Providers
}

func (s *sandbox) provider(id string) map[string]any {
	s.t.Helper()
	for _, p := range s.providers() {
		if p["id"] == id {
			return p
		}
	}
	s.t.Fatalf("provider %q not in providers.json", id)
	return nil
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

// rawUp is an upstream that records the request head exactly as it came
// over the wire, so the case of header names can be checked.
type rawUp struct {
	URL   string
	mu    sync.Mutex
	heads []string
}

func newRawUp(t *testing.T) *rawUp {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := &rawUp{URL: "http://" + ln.Addr().String()}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				var head strings.Builder
				n := 0
				first := ""
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if first == "" {
						first = line
					}
					if l := strings.ToLower(line); strings.HasPrefix(l, "content-length:") {
						n, _ = strconv.Atoi(strings.TrimSpace(line[len("content-length:"):]))
					}
					head.WriteString(line)
					if line == "\r\n" {
						break
					}
				}
				io.CopyN(io.Discard, br, int64(n))
				u.mu.Lock()
				u.heads = append(u.heads, head.String())
				u.mu.Unlock()
				body := `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
				if strings.HasPrefix(first, "GET") {
					body = `{"data":[{"id":"m1"}]}`
				}
				fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
			}()
		}
	}()
	return u
}

func (u *rawUp) take() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	h := u.heads
	u.heads = nil
	return h
}

func TestAcceptCLICustomHeaders(t *testing.T) {
	s := newSandbox(t)
	up := newRawUp(t)
	// the prefix is case-insensitive, the header name is kept as typed
	s.must("provider", "add", "Acme", "url="+up.URL+"/v1", "key=sk-acme", "Header.x-Org-ID=acme", "header.X-Trim=  v  ", "models=m1")
	heads := up.take()
	if len(heads) == 0 {
		t.Fatal("adding the provider fetched no model list")
	}
	for _, h := range heads {
		if !strings.Contains(h, "\r\nx-Org-ID: acme\r\n") {
			t.Fatalf("model-list request lacks the header as typed:\n%s", h)
		}
	}
	hs, _ := s.provider("acme")["headers"].(map[string]any)
	if hs["x-Org-ID"] != "acme" || hs["X-Trim"] != "v" || len(hs) != 2 {
		t.Fatalf("headers in providers.json: %v", hs)
	}
	shown := s.must("provider", "acme")
	if !strings.Contains(shown, "x-Org-ID") || !strings.Contains(shown, "acme") || !strings.Contains(shown, "X-Trim") {
		t.Fatalf("provider show lacks headers:\n%s", shown)
	}

	// connectivity test
	up.take()
	s.run("provider", "test", "acme")
	heads = up.take()
	posted := false
	for _, h := range heads {
		if strings.HasPrefix(h, "POST") {
			posted = true
		}
		if !strings.Contains(h, "\r\nx-Org-ID: acme\r\n") {
			t.Fatalf("test request lacks the header:\n%s", h)
		}
	}
	if !posted {
		t.Fatalf("provider test sent no request: %v", heads)
	}

	// model list
	up.take()
	s.run("provider", "models", "acme")
	heads = up.take()
	if len(heads) == 0 {
		t.Fatal("no model-list request")
	}
	for _, h := range heads {
		if !strings.Contains(h, "\r\nx-Org-ID: acme\r\n") {
			t.Fatalf("model-list request lacks the header:\n%s", h)
		}
	}

	// a preset provider ignores headers
	s.must("provider", "add", "deepseek", "sk-ds", "url="+up.URL+"/v1", "anthropic="+up.URL, "header.X-Org-Id=acme")
	if hs, _ := s.provider("deepseek")["headers"].(map[string]any); len(hs) != 0 {
		t.Fatalf("preset kept headers: %v", hs)
	}
	if strings.Contains(strings.ToLower(s.must("provider", "deepseek")), "x-org-id") {
		t.Fatal("preset shows a header")
	}
	for _, h := range up.take() {
		if strings.Contains(strings.ToLower(h), "x-org-id") {
			t.Fatalf("preset request carried the header:\n%s", h)
		}
	}
}

func pngBytes(extra int) []byte {
	b := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00")
	return append(b, make([]byte, extra)...)
}

func TestAcceptCLIIcon(t *testing.T) {
	s := newSandbox(t)
	s.must("provider", "add", "Mine", "url=http://127.0.0.1:1/v1", "key=k")
	pics := filepath.Join(t.TempDir())
	png := filepath.Join(pics, "a.png")
	os.WriteFile(png, pngBytes(0), 0o644)
	svg := filepath.Join(pics, "a.svg")
	os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"/>`), 0o644)
	txt := filepath.Join(pics, "a.txt")
	os.WriteFile(txt, []byte("just text"), 0o644)
	big := filepath.Join(pics, "big.png")
	os.WriteFile(big, pngBytes(1<<20), 0o644)

	iconDir := filepath.Join(s.home, ".config", "magpie", "icons")
	count := func() int { es, _ := os.ReadDir(iconDir); return len(es) }

	out := s.must("provider", "icon", "mine", png)
	icon, _ := s.provider("mine")["icon"].(string)
	if !strings.HasPrefix(icon, "file:") || !strings.HasSuffix(icon, ".png") || !strings.Contains(out, icon) {
		t.Fatalf("icon %q, output %q", icon, out)
	}
	if b, err := os.ReadFile(filepath.Join(iconDir, strings.TrimPrefix(icon, "file:"))); err != nil || string(b) != string(pngBytes(0)) {
		t.Fatalf("stored picture: %v", err)
	}
	s.must("provider", "icon", "mine", png)
	if count() != 1 {
		t.Fatalf("the same picture was stored %d times", count())
	}
	s.must("provider", "icon", "mine", svg)
	if icon, _ = s.provider("mine")["icon"].(string); !strings.HasSuffix(icon, ".svg") {
		t.Fatalf("svg icon %q", icon)
	}
	before := icon
	for _, bad := range []string{txt, big} {
		if out, err := s.run("provider", "icon", "mine", bad); err == nil {
			t.Fatalf("accepted %s: %s", bad, out)
		}
	}
	if now, _ := s.provider("mine")["icon"].(string); now != before {
		t.Fatalf("a rejected picture changed the icon: %q", now)
	}
	s.must("provider", "icon", "mine", "deepseek-color")
	if now, _ := s.provider("mine")["icon"].(string); now != "deepseek-color" {
		t.Fatalf("built-in icon: %q", now)
	}
	s.must("provider", "icon", "mine", "")
	if now, _ := s.provider("mine")["icon"].(string); now != "" && now != "generic" {
		t.Fatalf("icon not reset: %q", now)
	}

	// provider add takes icon= too
	s.must("provider", "add", "Pic", "url=http://127.0.0.1:1/v1", "key=k", "icon="+png)
	if icon, _ = s.provider("pic")["icon"].(string); !strings.HasPrefix(icon, "file:") {
		t.Fatalf("icon= on add: %q", icon)
	}

	// presets can't
	s.must("provider", "add", "deepseek", "sk-ds", "url=http://127.0.0.1:1/v1", "anthropic=http://127.0.0.1:1")
	out, err := s.run("provider", "icon", "deepseek", png)
	if err == nil || !(strings.Contains(strings.ToLower(out), "custom") || strings.Contains(out, "自定义")) {
		t.Fatalf("preset icon: err=%v out=%s", err, out)
	}
	if icon, _ := s.provider("deepseek")["icon"].(string); strings.HasPrefix(icon, "file:") {
		t.Fatalf("preset got a custom icon: %q", icon)
	}
}

func TestAcceptCLIFallback(t *testing.T) {
	s := newSandbox(t)
	s.must("provider", "add", "Plan", "url=http://127.0.0.1:1/v1", "key=k", "models=m1")
	s.must("provider", "add", "Spare", "url=http://127.0.0.1:1/v1", "key=k", "models=m2")
	s.must("provider", "add", "Third", "url=http://127.0.0.1:1/v1", "key=k", "models=m3")
	out := s.must("provider", "fallback", "plan", "spare/m2", "third/m3", "spare/m2")
	if !strings.Contains(out, "spare/m2") || !strings.Contains(out, "third/m3") {
		t.Fatalf("output %q", out)
	}
	fb, _ := s.provider("plan")["fallback"].([]any)
	if len(fb) != 2 || fb[0] != "spare/m2" || fb[1] != "third/m3" {
		t.Fatalf("fallback in file: %v", fb)
	}
	if shown := s.must("provider", "fallback", "plan"); !strings.Contains(shown, "spare/m2") {
		t.Fatalf("view: %q", shown)
	}
	// custom catalog list is accepted as typed
	s.must("provider", "add", "Gw", "url=http://127.0.0.1:1/v1", "key=k", "catalog=openai,deepseek")
	if c, _ := s.provider("gw")["catalog"].(string); !strings.Contains(c, "openai") || !strings.Contains(c, "deepseek") {
		t.Fatalf("catalog %q", c)
	}
}

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func codexLogin(t *testing.T, home, email, refresh string) {
	t.Helper()
	writeJSON(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      jwt(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}}),
			"access_token":  jwt(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": refresh, "account_id": "acct-" + email,
		},
	})
}

func codexRefresh(t *testing.T, home string) string {
	var a struct {
		Tokens struct {
			Refresh string `json:"refresh_token"`
		} `json:"tokens"`
	}
	readJSON(t, filepath.Join(home, ".codex", "auth.json"), &a)
	return a.Tokens.Refresh
}

func lineOf(out, needle string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	return ""
}

func TestAcceptCLICodexAccounts(t *testing.T) {
	s := newSandbox(t)
	if out, _ := s.run("accounts"); strings.Contains(out, "panic") {
		t.Fatalf("empty list: %s", out)
	}
	codexLogin(t, s.home, "me@example.com", "r-me")
	s.must("accounts")
	// the agent signs in to another account
	codexLogin(t, s.home, "work@example.com", "r-work")
	out := s.must("accounts", "codex")
	me, work := lineOf(out, "me@example.com"), lineOf(out, "work@example.com")
	if me == "" || work == "" {
		t.Fatalf("both accounts should be listed:\n%s", out)
	}
	pre := func(l, email string) string { return strings.TrimSpace(l[:strings.Index(l, email)]) }
	if pre(me, "me@") == pre(work, "work@") {
		t.Fatalf("the current account isn't marked apart:\n%s", out)
	}
	if !strings.Contains(me, "plus") {
		t.Errorf("plan not shown: %q", me)
	}
	// the agent refreshed its login in place; a switch must keep that
	codexLogin(t, s.home, "work@example.com", "r-work-2")

	out = s.must("accounts", "switch", "codex", "ME@Example.com")
	if !strings.Contains(strings.ToLower(out), "restart") && !strings.Contains(out, "重启") {
		t.Errorf("switch should say running sessions keep the old account: %q", out)
	}
	if got := codexRefresh(t, s.home); got != "r-me" {
		t.Fatalf("auth.json after switch: %q", got)
	}
	out = s.must("accounts", "codex")
	me, work = lineOf(out, "me@example.com"), lineOf(out, "work@example.com")
	if pre(me, "me@") == pre(work, "work@") {
		t.Fatalf("marker not moved:\n%s", out)
	}
	if out, err := s.run("accounts", "switch", "codex", "nobody@example.com"); err == nil {
		t.Fatalf("switched to an unknown account: %s", out)
	}
	if out, err := s.run("accounts", "forget", "codex", "me@example.com"); err == nil {
		t.Fatalf("forgot the signed-in account: %s", out)
	}
	// the replaced account kept its newest credentials
	s.must("accounts", "switch", "codex", "work@example.com")
	if got := codexRefresh(t, s.home); got != "r-work-2" {
		t.Fatalf("the replaced login lost its refreshed token: %q", got)
	}
	s.must("accounts", "forget", "codex", "ME@example.com")
	if out := s.must("accounts", "codex"); strings.Contains(out, "me@example.com") || !strings.Contains(out, "work@example.com") {
		t.Fatalf("after forget:\n%s", out)
	}
	if fi, err := os.Stat(filepath.Join(s.home, ".config", "magpie", "logins.json")); err == nil && fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("saved logins readable by others: %v", fi.Mode())
	}
}

func TestAcceptCLIClaudeAccountsSwitchKeepsProfile(t *testing.T) {
	s := newSandbox(t)
	cred := filepath.Join(s.home, ".claude", ".credentials.json")
	profile := filepath.Join(s.home, ".claude.json")
	login := func(email, refresh string) {
		writeJSON(t, cred, map[string]any{
			"claudeAiOauth": map[string]any{"accessToken": "at-" + refresh, "refreshToken": refresh,
				"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max"},
			"mcpOAuth": map[string]any{"keep": true},
		})
		writeJSON(t, profile, map[string]any{
			"numStartups":  1234567890123,
			"oauthAccount": map[string]any{"emailAddress": email, "accountUuid": "u-" + email},
		})
	}
	login("a@example.com", "sk-ant-ort01-a")
	s.must("accounts", "claude")
	login("b@example.com", "sk-ant-ort01-b")
	out := s.must("accounts", "cc")
	if !strings.Contains(out, "a@example.com") || !strings.Contains(out, "b@example.com") {
		t.Fatalf("accounts:\n%s", out)
	}
	s.must("accounts", "switch", "cc", "a@example.com")
	var c map[string]map[string]any
	readJSON(t, cred, &c)
	if c["claudeAiOauth"]["refreshToken"] != "sk-ant-ort01-a" {
		t.Fatalf("credentials not switched: %v", c)
	}
	if _, ok := c["mcpOAuth"]; !ok {
		t.Fatalf("other credential content lost: %v", c)
	}
	b, _ := os.ReadFile(profile)
	var p map[string]json.RawMessage
	json.Unmarshal(b, &p)
	if string(p["numStartups"]) != "1234567890123" || !strings.Contains(string(p["oauthAccount"]), "a@example.com") {
		t.Fatalf(".claude.json after switch: %s", b)
	}
}

func TestAcceptCLIAccountsUsage(t *testing.T) {
	s := newSandbox(t)
	for _, args := range [][]string{{"accounts", "gemini"}, {"accounts", "switch", "gemini", "x@y.z"}, {"accounts", "forget", "gemini", "x@y.z"}, {"accounts", "add", "gemini"}} {
		out, err := s.run(args...)
		if err == nil || !strings.Contains(out, "claude") || !strings.Contains(out, "codex") {
			t.Errorf("magpie %v: err=%v, output should name the usage:\n%s", args, err, out)
		}
	}
}
