package netproxy

import (
	"net/url"
	"reflect"
	"testing"
)

const scutilSample = `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
    2 : corp.example
  }
  ExcludeSimpleHostnames : 1
  HTTPEnable : 0
  HTTPSEnable : 1
  HTTPSPort : 8443
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`

func TestParseScutil(t *testing.T) {
	s := parseScutil(scutilSample)
	want := system{
		HTTPS:       "127.0.0.1:8443",
		SOCKS:       "127.0.0.1:1080",
		Exceptions:  []string{"*.local", "169.254/16", "corp.example"},
		SimpleLocal: true,
	}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("got %+v, want %+v", s, want)
	}
	if got := parseScutil("<dictionary> {\n}"); got.HTTPS != "" || got.SOCKS != "" {
		t.Fatalf("empty settings gave %+v", got)
	}
}

func TestParseWindows(t *testing.T) {
	s := parseWindows(true, "http=p.example:8080;https=p.example:8443;socks=s.example:1080",
		"localhost;*.corp.example;<local>")
	if s.HTTPS != "p.example:8443" || s.HTTP != "p.example:8080" || s.SOCKS != "s.example:1080" {
		t.Fatalf("protocols: %+v", s)
	}
	if !s.SimpleLocal || !reflect.DeepEqual(s.Exceptions, []string{"localhost", "*.corp.example"}) {
		t.Fatalf("exceptions: %+v", s)
	}
	one := parseWindows(true, "10.0.0.1:3128", "")
	if one.HTTPS != "10.0.0.1:3128" || one.HTTP != "10.0.0.1:3128" {
		t.Fatalf("plain server: %+v", one)
	}
	if off := parseWindows(false, "10.0.0.1:3128", ""); off.HTTPS != "" {
		t.Fatalf("disabled proxy used: %+v", off)
	}
}

func TestMatchPattern(t *testing.T) {
	u := func(raw string) *url.URL { p, _ := url.Parse(raw); return p }
	cases := []struct {
		pat, raw string
		want     bool
	}{
		{"*", "https://a.example", true},
		{"a.example", "https://A.example", true},
		{"a.example", "https://b.a.example", false},
		{".example", "https://example", true},
		{".example", "https://x.example", true},
		{"*.example", "https://x.y.example", true},
		{"10.*", "http://10.2.3.4", true},
		{"10.0.0.0/8", "http://10.2.3.4", true},
		{"10.0.0.0/8", "http://11.2.3.4", false},
		{"x.example:443", "https://x.example", true},
		{"x.example:443", "http://x.example:8080", false},
		{"", "https://x.example", false},
	}
	for _, c := range cases {
		if got := matchPattern(c.pat, u(c.raw)); got != c.want {
			t.Errorf("matchPattern(%q, %q) = %v", c.pat, c.raw, got)
		}
	}
}

func TestLoopbackHosts(t *testing.T) {
	for _, h := range []string{"localhost", "127.0.0.1", "127.9.9.9", "::1", "[::1]", "a.localhost"} {
		if !loopback(h) {
			t.Errorf("%s is loopback", h)
		}
	}
	for _, h := range []string{"example.com", "10.0.0.1", "localhost.example"} {
		if loopback(h) {
			t.Errorf("%s is not loopback", h)
		}
	}
}
