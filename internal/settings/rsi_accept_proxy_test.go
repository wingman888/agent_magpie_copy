package settings_test

import (
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func TestAcceptProxyValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"ftp://x", "http://", "://", "http://%zz", "socks4://1.2.3.4:1"} {
		err := settings.Save(settings.Settings{Proxy: bad})
		if err == nil {
			t.Errorf("%q accepted", bad)
			continue
		}
		if !strings.Contains(err.Error(), "http://127.0.0.1:7890") {
			t.Errorf("%q: error lacks example: %v", bad, err)
		}
		if got := settings.Load().Proxy; got != "socks5://127.0.0.1:1080" {
			t.Errorf("after failed save of %q, proxy = %q", bad, got)
		}
	}
	for _, ok := range []string{"", "direct", "127.0.0.1:7890", "http://h:1", "https://h:1", "socks5://h:1", "socks5h://h:1"} {
		if err := settings.Save(settings.Settings{Proxy: ok}); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
}

func TestAcceptProxyTrimmedAndPersisted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	if err := settings.Save(settings.Settings{Proxy: "  127.0.0.1:7890 \n"}); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().Proxy; got != "127.0.0.1:7890" {
		t.Fatalf("got %q", got)
	}
	b, err := os.ReadFile(settings.Path())
	if err != nil || !strings.Contains(string(b), `"proxy": "127.0.0.1:7890"`) {
		t.Fatalf("%v %s", err, b)
	}
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	if settings.Load().Proxy != "direct" {
		t.Fatal("direct not stored")
	}
}
