package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxySaveValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := Save(Settings{Proxy: "ftp://x"}); err == nil {
		t.Fatal("ftp:// accepted")
	} else if !strings.Contains(err.Error(), "http://127.0.0.1:7890") {
		t.Fatalf("error lacks example: %v", err)
	}
	if err := Save(Settings{Proxy: "http://"}); err == nil {
		t.Fatal("missing host accepted")
	}
	if err := Save(Settings{Proxy: "socks5://:1080"}); err == nil {
		t.Fatal("empty host accepted")
	}
	if err := Save(Settings{Proxy: "http://127.0.0.1:99999"}); err == nil {
		t.Fatal("bad port accepted")
	}
	if err := Save(Settings{Proxy: "not a proxy"}); err == nil {
		t.Fatal("garbage accepted")
	}
	if err := Save(Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatalf("bare host:port rejected: %v", err)
	}
	if err := Save(Settings{Proxy: "direct"}); err != nil {
		t.Fatalf("direct rejected: %v", err)
	}
	if err := Save(Settings{Proxy: ""}); err != nil {
		t.Fatalf("empty rejected: %v", err)
	}
	for _, v := range []string{"http://h:1", "https://h", "socks5://h:1080", "socks5h://h:1080"} {
		if err := ValidateProxy(v); err != nil {
			t.Fatalf("%s rejected: %v", v, err)
		}
	}
}

func TestProxyTrimmedAndKept(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Proxy: "  socks5://127.0.0.1:1080  "}); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxy; got != "socks5://127.0.0.1:1080" {
		t.Fatalf("saved proxy = %q", got)
	}
}

func TestProxySaveFailureKeepsFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Theme: "dark", Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(Settings{Theme: "light", Proxy: "ftp://x"}); err == nil {
		t.Fatal("ftp:// accepted")
	}
	after, err := os.ReadFile(filepath.Clean(Path()))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("file changed after failed save:\n%s\nvs\n%s", before, after)
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
