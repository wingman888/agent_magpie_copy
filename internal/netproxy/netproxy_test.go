package netproxy

import (
	"net/url"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// isolate points the settings at a temp folder and clears every proxy variable,
// so the test sees only what it sets.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, n := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(n, "")
	}
}

func forURL(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := For(u)
	if err != nil {
		t.Fatalf("For(%s): %v", raw, err)
	}
	if p == nil {
		return ""
	}
	return p.String()
}

func TestDirectSetting(t *testing.T) {
	isolate(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:1")
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	if got := forURL(t, "https://api.example.com/v1"); got != "" {
		t.Fatalf("direct went through %s", got)
	}
}

func TestSettingBeatsEnvironment(t *testing.T) {
	isolate(t)
	t.Setenv("HTTPS_PROXY", "http://env.example:1")
	if err := settings.Save(settings.Settings{Proxy: "socks5://10.0.0.9:1080"}); err != nil {
		t.Fatal(err)
	}
	if got := forURL(t, "https://api.example.com/v1"); got != "socks5://10.0.0.9:1080" {
		t.Fatalf("got %q", got)
	}
}

func TestEnvironmentOrder(t *testing.T) {
	isolate(t)
	t.Setenv("HTTP_PROXY", "http://http.example:2")
	if got := forURL(t, "https://api.example.com"); got != "http://http.example:2" {
		t.Fatalf("HTTP only: %q", got)
	}
	t.Setenv("HTTPS_PROXY", "http://https.example:3")
	if got := forURL(t, "https://api.example.com"); got != "http://https.example:3" {
		t.Fatalf("HTTPS should win over HTTP: %q", got)
	}
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "127.0.0.1:7890")
	if got := forURL(t, "https://api.example.com"); got != "http://127.0.0.1:7890" {
		t.Fatalf("bare lower-case https_proxy: %q", got)
	}
}

func TestNoProxy(t *testing.T) {
	isolate(t)
	t.Setenv("HTTPS_PROXY", "http://p.example:3")
	t.Setenv("NO_PROXY", "example.com, *.internal.test")
	for _, raw := range []string{"https://api.example.com", "https://example.com", "https://a.internal.test"} {
		if got := forURL(t, raw); got != "" {
			t.Errorf("%s should bypass, got %s", raw, got)
		}
	}
	if got := forURL(t, "https://other.org"); got != "http://p.example:3" {
		t.Errorf("other.org: %q", got)
	}
}

func TestLoopbackNeverProxied(t *testing.T) {
	isolate(t)
	t.Setenv("HTTPS_PROXY", "http://p.example:3")
	if err := settings.Save(settings.Settings{Proxy: "http://10.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://127.0.0.1:3425/", "http://localhost:3425", "http://[::1]:3425", "http://127.9.9.9/"} {
		if got := forURL(t, raw); got != "" {
			t.Errorf("%s went through %s", raw, got)
		}
	}
}

func TestNilURL(t *testing.T) {
	if p, err := For(nil); p != nil || err != nil {
		t.Fatalf("nil url: %v %v", p, err)
	}
}

func TestParseScutil(t *testing.T) {
	out := `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
    2 : example.com
  }
  ExcludeSimpleHostnames : 1
  HTTPEnable : 0
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	s := parseScutil(out)
	if s.HTTPS != "http://127.0.0.1:7890" || s.HTTP != "" || s.SOCKS != "socks5://127.0.0.1:1080" {
		t.Fatalf("protocols: %+v", s)
	}
	if !s.SimpleHostnames || len(s.Exceptions) != 3 {
		t.Fatalf("exceptions: %+v", s)
	}
	if s.pick().String() != "http://127.0.0.1:7890" {
		t.Fatalf("HTTPS should be picked first: %v", s.pick())
	}
	cases := map[string]bool{
		"foo.local":       true,  // *.local
		"169.254.3.4":     true,  // 169.254/16
		"example.com":     true,  // exact
		"api.example.com": true,  // subdomain of example.com
		"notexample.com":  false, // not a subdomain
		"api.openai.com":  false,
		"printer":         true, // local name
		"10.1.2.3":        false,
	}
	for host, want := range cases {
		if got := bypass(host, s.Exceptions, s.SimpleHostnames); got != want {
			t.Errorf("bypass(%s) = %v, want %v", host, got, want)
		}
	}
}

func TestParseWindows(t *testing.T) {
	s := parseWindows("0x1", "http=a.example:1;https=b.example:2;socks=c.example:3", "localhost;*.corp.test;<local>")
	if s.HTTPS != "http://b.example:2" || s.HTTP != "http://a.example:1" || s.SOCKS != "socks5://c.example:3" {
		t.Fatalf("protocols: %+v", s)
	}
	if s.pick().String() != "http://b.example:2" {
		t.Fatalf("pick: %v", s.pick())
	}
	for host, want := range map[string]bool{
		"intranet":        true, // <local>
		"wiki.corp.test":  true, // *.corp.test
		"corp.test":       true,
		"localhost":       true,
		"api.example.com": false,
	} {
		if got := bypass(host, s.Exceptions, s.SimpleHostnames); got != want {
			t.Errorf("bypass(%s) = %v, want %v", host, got, want)
		}
	}
	if off := parseWindows("0x0", "127.0.0.1:7890", ""); off.pick() != nil {
		t.Fatalf("disabled proxy used: %+v", off)
	}
	one := parseWindows("1", "127.0.0.1:7890", "")
	if one.HTTP != "http://127.0.0.1:7890" || one.HTTPS != one.HTTP {
		t.Fatalf("single server: %+v", one)
	}
}

func TestBypassRules(t *testing.T) {
	if !bypass("10.1.2.3", []string{"10.0.0.0/8"}, false) {
		t.Error("CIDR not matched")
	}
	if bypass("11.1.2.3", []string{"10.0.0.0/8"}, false) {
		t.Error("CIDR matched outside")
	}
	if !bypass("anything.example", []string{"*"}, false) {
		t.Error("* not matched")
	}
	if !bypass("api.example.com", []string{".example.com"}, false) {
		t.Error("leading dot suffix not matched")
	}
	if !bypass("example.com", []string{"example.com:443"}, false) {
		t.Error("host:port rule not matched")
	}
}

func TestPickOrder(t *testing.T) {
	s := systemProxy{SOCKS: "socks5://s:1", HTTP: "http://h:2"}
	if s.pick().String() != "http://h:2" {
		t.Fatalf("HTTP before SOCKS: %v", s.pick())
	}
	if (systemProxy{}).pick() != nil {
		t.Fatal("empty system proxy picked something")
	}
}
