package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateProxyAccepts(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"   ":                       "",
		"direct":                    "direct",
		"  direct  ":                "direct",
		"127.0.0.1:7890":            "127.0.0.1:7890",
		"  127.0.0.1:7890\t":        "127.0.0.1:7890",
		"http://127.0.0.1:7890":     "http://127.0.0.1:7890",
		"https://proxy.local":       "https://proxy.local",
		"socks5://127.0.0.1:1080":   "socks5://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080":  "socks5h://127.0.0.1:1080",
		" socks5://127.0.0.1:1080 ": "socks5://127.0.0.1:1080",
	}
	for in, want := range cases {
		got, err := ValidateProxy(in)
		if err != nil {
			t.Errorf("ValidateProxy(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ValidateProxy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateProxyRejects(t *testing.T) {
	for _, in := range []string{
		"ftp://x",
		"http://",
		"http://:7890",
		"socks5://",
		"127.0.0.1",
		"host:notaport",
		"host:99999",
		"foo bar",
		"://",
		":7890",
	} {
		_, err := ValidateProxy(in)
		if err == nil {
			t.Errorf("ValidateProxy(%q): expected error", in)
			continue
		}
		if !strings.Contains(err.Error(), ProxyExample) {
			t.Errorf("ValidateProxy(%q): error lacks example: %v", in, err)
		}
	}
}

func TestValidateProxyURLHost(t *testing.T) {
	for _, in := range []string{
		"http://proxy.local:8080",
		"http://[::1]:7890",
	} {
		if _, err := ValidateProxy(in); err != nil {
			t.Errorf("ValidateProxy(%q): unexpected error %v", in, err)
		}
	}
	for _, in := range []string{
		"http://a..b:7890",
		"http://under_score:7890",
	} {
		_, err := ValidateProxy(in)
		if err == nil {
			t.Errorf("ValidateProxy(%q): expected error", in)
			continue
		}
		if !strings.Contains(err.Error(), ProxyExample) {
			t.Errorf("ValidateProxy(%q): error lacks example: %v", in, err)
		}
	}
}

func TestValidateProxyHostAndPort(t *testing.T) {
	for _, in := range []string{
		"127.0.0.1:7890",
		"[::1]:7890",
		"proxy.local:8080",
	} {
		if _, err := ValidateProxy(in); err != nil {
			t.Errorf("ValidateProxy(%q): unexpected error %v", in, err)
		}
	}
	for _, in := range []string{
		"foo bar:80",
		"a/b:7890",
		"exa mple.com:7890",
		"-bad.host:7890",
		"bad-.host:7890",
		"under_score:7890",
		"http://127.0.0.1:",
		"http://[::1]:",
		"127.0.0.1:0",
		"127.0.0.1:65536",
		"127.0.0.1:+80",
	} {
		_, err := ValidateProxy(in)
		if err == nil {
			t.Errorf("ValidateProxy(%q): expected error", in)
			continue
		}
		if !strings.Contains(err.Error(), ProxyExample) {
			t.Errorf("ValidateProxy(%q): error lacks example: %v", in, err)
		}
	}
}

func TestSaveProxy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := Save(Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatalf("bare host:port rejected: %v", err)
	}
	if s := Load(); s.Proxy != "127.0.0.1:7890" {
		t.Fatalf("saved proxy = %q", s.Proxy)
	}

	if err := Save(Settings{Proxy: "  direct \n"}); err != nil {
		t.Fatalf("direct rejected: %v", err)
	}
	if s := Load(); s.Proxy != "direct" {
		t.Fatalf("trimmed proxy = %q", s.Proxy)
	}

	if err := Save(Settings{Proxy: "http://127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	err = Save(Settings{Theme: "dark", Proxy: "ftp://x"})
	if err == nil {
		t.Fatal("ftp:// accepted")
	}
	if !strings.Contains(err.Error(), ProxyExample) {
		t.Fatalf("error lacks example: %v", err)
	}
	after, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("failed save changed the file:\n%s\nvs\n%s", before, after)
	}
	if s := Load(); s.Proxy != "http://127.0.0.1:7890" || s.Theme != "system" {
		t.Fatalf("settings changed after failed save: %+v", s)
	}
}

func TestSaveProxyEmptyFileUntouchedOnFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Proxy: "http://:7890"}); err == nil {
		t.Fatal("missing host accepted")
	}
	if _, err := os.Stat(filepath.Join(Dir(), "settings.json")); err == nil {
		t.Fatal("failed save created a settings file")
	}
}

func TestProxyJSONKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"proxy": "direct"`) {
		t.Fatalf("json key missing: %s", b)
	}
}
