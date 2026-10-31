package main_test

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// rawUpstream records the raw header lines of every request it receives.
type rawUpstream struct {
	mu    sync.Mutex
	lines []string
	url   string
}

func (u *rawUpstream) has(line string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, l := range u.lines {
		if l == line {
			return true
		}
	}
	return false
}

func startRaw(t *testing.T) *rawUpstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	u := &rawUpstream{url: "http://" + ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				first, _ := r.ReadString('\n')
				path := strings.Fields(first)
				cl := 0
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					l = strings.TrimRight(l, "\r\n")
					if l == "" {
						break
					}
					u.mu.Lock()
					u.lines = append(u.lines, l)
					u.mu.Unlock()
					if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(k, "content-length") {
						for _, ch := range strings.TrimSpace(v) {
							cl = cl*10 + int(ch-'0')
						}
					}
				}
				for i := 0; i < cl; i++ {
					r.ReadByte()
				}
				body := `{"id":"x","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
				if len(path) > 1 && strings.HasSuffix(path[1], "/models") {
					body = `{"object":"list","data":[{"id":"m1","object":"model"}]}`
				}
				c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nConnection: close\r\nContent-Length: " + itoa(len(body)) + "\r\n\r\n" + body))
			}()
		}
	}()
	return u
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

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
			buildErr = string(out)
		}
	})
	if buildErr != "" {
		t.Fatalf("build: %s", buildErr)
	}
	return binPath
}

type env struct {
	t    *testing.T
	home string
	bin  string
}

func newEnv(t *testing.T) *env {
	return &env{t: t, home: t.TempDir(), bin: binary(t)}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func (e *env) run(args ...string) (string, error) {
	cmd := exec.Command(e.bin, args...)
	cmd.Dir = e.home
	var envs []string
	for _, kv := range os.Environ() {
		k := strings.ToUpper(strings.SplitN(kv, "=", 2)[0])
		if strings.Contains(k, "PROXY") || k == "HOME" || strings.HasPrefix(k, "XDG_") {
			continue
		}
		envs = append(envs, kv)
	}
	envs = append(envs, "HOME="+e.home, "XDG_CONFIG_HOME="+filepath.Join(e.home, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(e.home, ".cache"), "XDG_DATA_HOME="+filepath.Join(e.home, ".local"),
		"NO_PROXY=127.0.0.1,localhost")
	cmd.Env = envs
	out, err := cmd.CombinedOutput()
	return ansi.ReplaceAllString(string(out), ""), err
}

func (e *env) mustRun(args ...string) string {
	e.t.Helper()
	out, err := e.run(args...)
	if err != nil {
		e.t.Fatalf("magpie %v: %v\n%s", args, err, out)
	}
	return out
}

func (e *env) config() []map[string]any {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.home, ".config", "magpie", "providers.json"))
	if err != nil {
		e.t.Fatal(err)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		e.t.Fatal(err)
	}
	return f.Providers
}

func (e *env) icon(id string) string {
	e.t.Helper()
	for _, p := range e.config() {
		if p["id"] == id {
			s, _ := p["icon"].(string)
			return s
		}
	}
	e.t.Fatalf("provider %s not in config", id)
	return ""
}

func (e *env) iconFiles() []string {
	ents, _ := os.ReadDir(filepath.Join(e.home, ".config", "magpie", "icons"))
	var out []string
	for _, x := range ents {
		out = append(out, x.Name())
	}
	return out
}

var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01")

func TestAcceptCLIHeadersAddShowAndSend(t *testing.T) {
	e := newEnv(t)
	up := startRaw(t)
	e.mustRun("provider", "add", "My Relay", "url="+up.url+"/v1", "key=sk-test-123456",
		"header.X-Org-Id=acme", "HEADER.X-ORG-id =  padded  ", "header.Authorization=Custom mine", "models=m1")
	out := e.mustRun("provider", "my-relay")
	re := regexp.MustCompile(`(?m)^\s*header\s+X-Org-Id: acme\s*$`)
	if !re.MatchString(out) {
		t.Fatalf("show missing X-Org-Id line:\n%s", out)
	}
	// sorted by header name
	ia, ib, ic := strings.Index(out, "Authorization: Custom mine"), strings.Index(out, "X-ORG-id"), strings.Index(out, "X-Org-Id: acme")
	if ia < 0 || ib < 0 || ic < 0 || !(ia < ib && ib < ic) {
		t.Errorf("headers not shown sorted (%d %d %d):\n%s", ia, ib, ic, out)
	}
	// persisted
	var hs map[string]any
	for _, p := range e.config() {
		if p["id"] == "my-relay" {
			hs, _ = p["headers"].(map[string]any)
		}
	}
	if hs["X-Org-Id"] != "acme" || hs["Authorization"] != "Custom mine" {
		t.Errorf("headers in config: %v", hs)
	}
	if _, ok := hs["X-ORG-id"]; !ok {
		t.Errorf("name case not kept: %v", hs)
	}

	// connectivity test and model fetch both carry the headers
	e.run("provider", "test", "my-relay")
	for _, want := range []string{"X-Org-Id: acme", "X-ORG-id: padded", "Authorization: Custom mine"} {
		if !up.has(want) {
			t.Errorf("upstream never saw %q; got %q", want, up.lines)
		}
	}
	if up.has("Authorization: Bearer sk-test-123456") {
		t.Errorf("auth header not overridden")
	}
}

func TestAcceptCLIHeadersOnlyCustom(t *testing.T) {
	e := newEnv(t)
	e.mustRun("provider", "add", "Plain", "url=https://relay.example.com/v1", "key=k")
	for _, p := range e.config() {
		if _, ok := p["headers"]; ok {
			t.Errorf("empty headers should not be written: %v", p)
		}
	}
	// header entries with empty names are dropped
	e.mustRun("provider", "add", "Odd", "url=https://odd.example.com/v1", "key=k", "header.X-A=1")
	out := e.mustRun("provider", "odd")
	if !regexp.MustCompile(`header\s+X-A: 1`).MatchString(out) {
		t.Errorf("missing header: %s", out)
	}
}

func TestAcceptCLIIconCustomProvider(t *testing.T) {
	e := newEnv(t)
	e.mustRun("provider", "add", "One", "url=https://one.example.com/v1", "key=k")
	e.mustRun("provider", "add", "Two", "url=https://two.example.com/v1", "key=k")
	pic := filepath.Join(e.home, "logo.png")
	os.WriteFile(pic, png, 0o644)

	out := e.mustRun("provider", "icon", "one", pic)
	ref := e.icon("one")
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") {
		t.Fatalf("icon = %q", ref)
	}
	if !strings.Contains(out, ref) {
		t.Errorf("output does not show icon value: %s", out)
	}
	e.mustRun("provider", "icon", "two", pic)
	if e.icon("two") != ref {
		t.Errorf("same picture, different refs: %q %q", e.icon("two"), ref)
	}
	if n := len(e.iconFiles()); n != 1 {
		t.Errorf("icon files: %v", e.iconFiles())
	}
	// source gone: still displayable
	os.Remove(pic)
	e.mustRun("provider", "one")
	b, err := os.ReadFile(filepath.Join(e.home, ".config", "magpie", "icons", strings.TrimPrefix(ref, "file:")))
	if err != nil || string(b) != string(png) {
		t.Errorf("stored picture: %v", err)
	}

	// built-in icon
	e.mustRun("provider", "icon", "one", "deepseek-color")
	if got := e.icon("one"); got != "deepseek-color" {
		t.Errorf("icon = %q", got)
	}
	// restore default
	out = e.mustRun("provider", "icon", "one", "")
	if got := e.icon("one"); got == "deepseek-color" || strings.HasPrefix(got, "file:") {
		t.Errorf("icon not reset: %q", got)
	}
	_ = out
}

func TestAcceptCLIIconRejectsBadFiles(t *testing.T) {
	e := newEnv(t)
	e.mustRun("provider", "add", "One", "url=https://one.example.com/v1", "key=k", "icon=deepseek-color")
	if e.icon("one") != "deepseek-color" {
		t.Fatalf("icon on add: %q", e.icon("one"))
	}
	text := filepath.Join(e.home, "a.png")
	os.WriteFile(text, []byte("just some text, not a picture"), 0o644)
	empty := filepath.Join(e.home, "empty.png")
	os.WriteFile(empty, nil, 0o644)
	big := filepath.Join(e.home, "big.png")
	os.WriteFile(big, append(append([]byte{}, png...), make([]byte, 1<<20)...), 0o644)
	for _, f := range []string{text, empty, big} {
		if out, err := e.run("provider", "icon", "one", f); err == nil {
			t.Errorf("%s accepted: %s", filepath.Base(f), out)
		}
		if e.icon("one") != "deepseek-color" {
			t.Errorf("icon changed after rejecting %s: %q", filepath.Base(f), e.icon("one"))
		}
	}
	svg := filepath.Join(e.home, "l.txt")
	os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"/>`), 0o644)
	e.mustRun("provider", "icon", "one", svg)
	if got := e.icon("one"); !strings.HasPrefix(got, "file:") || !strings.HasSuffix(got, ".svg") {
		t.Errorf("svg icon = %q", got)
	}
}

func TestAcceptCLIIconPresetRejected(t *testing.T) {
	e := newEnv(t)
	e.mustRun("provider", "add", "deepseek", "sk-abc")
	out, err := e.run("provider", "icon", "deepseek", "deepseek-color")
	if err == nil {
		t.Fatalf("preset accepted an icon: %s", out)
	}
	if !strings.Contains(strings.ToLower(out), "custom") {
		t.Errorf("error should explain only custom providers: %s", out)
	}
}

func TestAcceptCLIUsageMentionsFeature(t *testing.T) {
	e := newEnv(t)
	out, _ := e.run("provider")
	for _, want := range []string{"header.X-Foo", "icon", "provider icon"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage lacks %q:\n%s", want, out)
		}
	}
}
