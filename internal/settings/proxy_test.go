package settings

import (
	"os"
	"strings"
	"testing"
)

func TestProxyAccepted(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, v := range []string{"", "direct", "Direct", "http://10.0.0.1:8080", "https://proxy.example:443",
		"socks5://127.0.0.1:1080", "socks5h://127.0.0.1:1080", "127.0.0.1:7890", "localhost:7890", " 127.0.0.1:7890 "} {
		if err := Save(Settings{Proxy: v}); err != nil {
			t.Fatalf("%q rejected: %v", v, err)
		}
	}
	if err := Save(Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxy; got != "127.0.0.1:7890" {
		t.Fatalf("saved proxy: %q", got)
	}
	if err := Save(Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxy; got != "socks5://127.0.0.1:1080" {
		t.Fatalf("saved socks: %q", got)
	}
}

func TestProxyRejected(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, v := range []string{"ftp://x", "socks4://127.0.0.1:1080", "://x", "http://", "127.0.0.1:0",
		"http://user:hunter2@", "socks://x:1080"} {
		if err := Save(Settings{Proxy: v}); err == nil {
			t.Fatalf("%q accepted", v)
		} else if strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("credentials echoed: %v", err)
		}
	}
	// a rejected value is not written
	if err := Save(Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Settings{Proxy: "ftp://x"}); err == nil {
		t.Fatal("ftp accepted")
	}
	if got := Load().Proxy; got != "127.0.0.1:7890" {
		t.Fatalf("rejected value overwrote settings: %q", got)
	}
	if _, err := os.Stat(Path()); err != nil {
		t.Fatal(err)
	}
}

func TestProxyURL(t *testing.T) {
	u, err := ProxyURL("127.0.0.1:7890")
	if err != nil || u.String() != "http://127.0.0.1:7890" {
		t.Fatalf("bare host:port: %v %v", u, err)
	}
	u, err = ProxyURL("SOCKS5://127.0.0.1:1080")
	if err != nil || u.Scheme != "socks5" {
		t.Fatalf("scheme case: %v %v", u, err)
	}
	if u, err := ProxyURL("direct"); u != nil || err != nil {
		t.Fatalf("direct: %v %v", u, err)
	}
}
