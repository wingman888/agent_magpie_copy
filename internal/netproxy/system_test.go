package netproxy

import "testing"

// Acceptance 8: macOS scutil output.
const scutilAll = `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
    2 : intranet.corp
  }
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : 10.0.0.2
  HTTPSEnable : 1
  HTTPSPort : 8443
  HTTPSProxy : 10.0.0.3
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 10.0.0.4
  ExcludeSimpleHostnames : 0
}
`

func TestParseScutilPriority(t *testing.T) {
	sp := parseScutil(scutilAll)
	if sp.URL != "http://10.0.0.3:8443" {
		t.Fatalf("URL = %q, want the HTTPS proxy http://10.0.0.3:8443", sp.URL)
	}
	want := []string{"*.local", "169.254/16", "intranet.corp"}
	if len(sp.Bypass) != len(want) {
		t.Fatalf("Bypass = %v, want %v", sp.Bypass, want)
	}
	for i := range want {
		if sp.Bypass[i] != want[i] {
			t.Fatalf("Bypass[%d] = %q, want %q", i, sp.Bypass[i], want[i])
		}
	}
}

func TestParseScutilHTTPThenSocks(t *testing.T) {
	out := `<dictionary> {
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : 10.0.0.2
  HTTPSEnable : 0
  HTTPSPort : 8443
  HTTPSProxy : 10.0.0.3
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 10.0.0.4
}
`
	if sp := parseScutil(out); sp.URL != "http://10.0.0.2:8080" {
		t.Fatalf("URL = %q, want http://10.0.0.2:8080", sp.URL)
	}
}

func TestParseScutilOnlySocks(t *testing.T) {
	out := `<dictionary> {
  HTTPEnable : 0
  HTTPSEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}
`
	if sp := parseScutil(out); sp.URL != "socks5://127.0.0.1:1080" {
		t.Fatalf("URL = %q, want socks5://127.0.0.1:1080", sp.URL)
	}
}

func TestParseScutilNoneEnabled(t *testing.T) {
	out := `<dictionary> {
  HTTPEnable : 0
  HTTPPort : 8080
  HTTPProxy : 10.0.0.2
  HTTPSEnable : 0
  SOCKSEnable : 0
  ExceptionsList : <array> {
    0 : *.corp
  }
}
`
	sp := parseScutil(out)
	if sp.URL != "" {
		t.Fatalf("URL = %q, want none", sp.URL)
	}
	if len(sp.Bypass) != 1 || sp.Bypass[0] != "*.corp" {
		t.Fatalf("Bypass = %v, want [*.corp]", sp.Bypass)
	}
}

func TestParseScutilEmptyAndGarbage(t *testing.T) {
	for _, out := range []string{"", "not scutil output", "<dictionary> {\n}\n"} {
		if sp := parseScutil(out); sp.URL != "" {
			t.Errorf("parseScutil(%q).URL = %q, want none", out, sp.URL)
		}
	}
}

// Acceptance 9: Windows ProxyServer and ProxyOverride.
func TestParseWindowsProxyForms(t *testing.T) {
	cases := []struct {
		name, server, want string
	}{
		{"single host:port is HTTP", "127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"https preferred", "http=10.0.0.2:8080;https=10.0.0.3:8443", "http://10.0.0.3:8443"},
		{"http when no https", "http=10.0.0.2:8080;socks=10.0.0.4:1080", "http://10.0.0.2:8080"},
		{"socks when only socks", "socks=127.0.0.1:1080", "socks5://127.0.0.1:1080"},
		{"empty values skipped", "https=;http=10.0.0.2:8080", "http://10.0.0.2:8080"},
		{"empty server is none", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseWindowsProxy(c.server, "").URL; got != c.want {
				t.Fatalf("parseWindowsProxy(%q).URL = %q, want %q", c.server, got, c.want)
			}
		})
	}
}

func TestParseWindowsProxyOverride(t *testing.T) {
	sp := parseWindowsProxy("127.0.0.1:7890", "*.corp;<local>")
	cases := map[string]bool{
		"git.corp":     true,  // *.corp
		"intranet":     true,  // <local>
		"chatgpt.com":  false, // goes through the proxy
		"corp":         true,  // *.corp also matches the bare domain
		"a.b.intranet": false, // <local> is only for names without a dot
	}
	for host, want := range cases {
		if got := matchBypass(host, sp.Bypass); got != want {
			t.Errorf("matchBypass(%q) = %v, want %v", host, got, want)
		}
	}
	if sp.URL != "http://127.0.0.1:7890" {
		t.Fatalf("URL = %q", sp.URL)
	}
}

func TestMatchBypassRules(t *testing.T) {
	cases := []struct {
		host string
		list []string
		want bool
	}{
		{"Example.COM", []string{"example.com"}, true},
		{"example.com.evil", []string{"example.com"}, false},
		{"a.example.com", []string{"*.example.com"}, true},
		{"example.com", []string{"*.example.com"}, true},
		{"a.example.com", []string{".example.com"}, true},
		{"example.com", []string{".example.com"}, true},
		{"xexample.com", []string{"*.example.com"}, false},
		{"intranet", []string{"<local>"}, true},
		{"a.intranet", []string{"<local>"}, false},
		{"intranet", []string{"", "  ", "<local>"}, true},
		{"chatgpt.com", []string{"", "  "}, false},
		{"chatgpt.com", nil, false},
	}
	for _, c := range cases {
		if got := matchBypass(c.host, c.list); got != c.want {
			t.Errorf("matchBypass(%q, %v) = %v, want %v", c.host, c.list, got, c.want)
		}
	}
}
