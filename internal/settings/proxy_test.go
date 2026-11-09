package settings

import (
	"os"
	"strings"
	"testing"
)

func TestProxySaveAcceptsValidValues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, v := range []string{
		"",
		"direct",
		"http://127.0.0.1:7890",
		"https://proxy.example.com:8443",
		"socks5://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080",
		"127.0.0.1:7890",
		"  socks5://127.0.0.1:1080  ",
	} {
		if err := Save(Settings{Proxy: v}); err != nil {
			t.Fatalf("Save(%q) rejected: %v", v, err)
		}
	}
	if err := Save(Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxy; got != "127.0.0.1:7890" {
		t.Fatalf("bare host:port not kept as written, got %q", got)
	}
	if err := Save(Settings{Proxy: "  socks5://127.0.0.1:1080 \n"}); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxy; got != "socks5://127.0.0.1:1080" {
		t.Fatalf("whitespace not trimmed, got %q", got)
	}
}

func TestProxySaveRejectsInvalidValues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, v := range []string{
		"ftp://x",
		"http://",
		"http://:7890",
		"socks5://",
		"127.0.0.1",
		"127.0.0.1:",
		"127.0.0.1:99999",
		"://bad",
		"http://[::1",
		"nonsense value",
	} {
		err := Save(Settings{Proxy: v})
		if err == nil {
			t.Fatalf("Save(%q) accepted an invalid proxy", v)
		}
		if !strings.Contains(err.Error(), "http://127.0.0.1:7890") {
			t.Fatalf("Save(%q) error lacks example: %v", v, err)
		}
	}
}

func TestProxyInvalidSaveKeepsOldFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Theme: "dark", Proxy: "http://127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(Settings{Theme: "light", Proxy: "ftp://x"}); err == nil {
		t.Fatal("ftp accepted")
	}
	after, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("file changed after failed save:\n%s\nvs\n%s", before, after)
	}
	if s := Load(); s.Theme != "dark" || s.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("settings changed: %+v", s)
	}
}

func TestProxyJSONKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"proxy": "socks5://127.0.0.1:1080"`) {
		t.Fatalf("proxy key missing from file:\n%s", b)
	}
	if err := Save(Settings{}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(Path())
	if strings.Contains(string(b), "proxy") {
		t.Fatalf("empty proxy should be omitted:\n%s", b)
	}
}

func TestProxyValidateProxyDirect(t *testing.T) {
	if err := ValidateProxy("direct"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProxy(""); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProxy("DIRECT"); err == nil {
		t.Fatal("only lowercase direct is the keyword")
	}
}

func TestProxyLoadErrorReportsWrongType(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(`{"proxy": 7890, "theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ProxyLoadError(); err == nil {
		t.Fatal("numeric proxy not reported")
	}
	// Load still falls back to defaults, which is why the netproxy check exists.
	if s := Load(); s.Proxy != "" {
		t.Fatalf("Load proxy = %q, want empty", s.Proxy)
	}
}

func TestProxyLoadErrorCases(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := ProxyLoadError(); err != nil {
		t.Fatalf("missing file reported: %v", err)
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(`{"proxy": "127.0.0.1:7890", "tray": 3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ProxyLoadError(); err != nil {
		t.Fatalf("string proxy with other keys reported: %v", err)
	}
	if err := os.WriteFile(Path(), []byte(`{"proxy": `), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ProxyLoadError(); err == nil {
		t.Fatal("broken JSON not reported")
	}
}
