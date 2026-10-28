package settings_test

import (
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func TestAcceptSaveProxyValid(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:7890":    "http://127.0.0.1:7890",
		"https://p.example.com:1":  "https://p.example.com:1",
		"socks5://127.0.0.1:1080":  "socks5://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080": "socks5h://127.0.0.1:1080",
		"127.0.0.1:7890":           "127.0.0.1:7890",
		"  direct  ":               "direct",
		"  socks5://h:1\t":         "socks5://h:1",
		"":                         "",
	} {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		if err := settings.Save(settings.Settings{Proxy: in}); err != nil {
			t.Errorf("Save(%q): %v", in, err)
			continue
		}
		if got := settings.Load().Proxy; got != want {
			t.Errorf("Save(%q) loaded %q, want %q", in, got, want)
		}
	}
}

func TestAcceptSaveProxyInvalid(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(settings.Path())
	for _, in := range []string{"ftp://x", "http://", "://bad", "  ftp://x  "} {
		err := settings.Save(settings.Settings{Proxy: in})
		if err == nil {
			t.Errorf("Save(%q) accepted", in)
			continue
		}
		if !strings.HasPrefix(err.Error(), "proxy must look like") {
			t.Errorf("Save(%q) error %q", in, err)
		}
	}
	err := settings.Save(settings.Settings{Proxy: "  ftp://x "})
	want := `proxy must look like http://127.0.0.1:7890 or socks5://127.0.0.1:1080, not "ftp://x"`
	if err == nil || err.Error() != want {
		t.Errorf("error text %v, want %s", err, want)
	}
	after, _ := os.ReadFile(settings.Path())
	if string(before) != string(after) || settings.Load().Proxy != "socks5://127.0.0.1:1080" {
		t.Errorf("settings changed after failed save: %s", after)
	}
}

func TestAcceptProxyJSONKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	settings.Save(settings.Settings{})
	b, _ := os.ReadFile(settings.Path())
	if strings.Contains(string(b), `"proxy"`) {
		t.Errorf("empty proxy not omitted: %s", b)
	}
	settings.Save(settings.Settings{Proxy: "direct"})
	b, _ = os.ReadFile(settings.Path())
	if !strings.Contains(string(b), `"proxy": "direct"`) && !strings.Contains(string(b), `"proxy":"direct"`) {
		t.Errorf("missing proxy key: %s", b)
	}
}
