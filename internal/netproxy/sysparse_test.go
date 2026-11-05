package netproxy

import "testing"

const scutilAll = `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
    2 : *.corp
  }
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : 10.0.0.2
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`

func TestParseScutilPriority(t *testing.T) {
	cfg := parseScutil(scutilAll)
	if cfg.proxy != "http://127.0.0.1:7890" {
		t.Fatalf("proxy = %q, want HTTPS one", cfg.proxy)
	}
	if want := []string{"*.local", "169.254/16", "*.corp"}; len(cfg.bypass) != 3 ||
		cfg.bypass[0] != want[0] || cfg.bypass[2] != want[2] {
		t.Fatalf("bypass = %v, want %v", cfg.bypass, want)
	}
}

func TestParseScutilHTTPBeforeSOCKS(t *testing.T) {
	out := `<dictionary> {
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : 10.0.0.2
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	if got := parseScutil(out).proxy; got != "http://10.0.0.2:8080" {
		t.Fatalf("proxy = %q", got)
	}
}

func TestParseScutilOnlySOCKS(t *testing.T) {
	out := `<dictionary> {
  HTTPSEnable : 0
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`
	if got := parseScutil(out).proxy; got != "socks5://127.0.0.1:1080" {
		t.Fatalf("proxy = %q, want socks5://127.0.0.1:1080", got)
	}
}

func TestParseScutilNothingEnabled(t *testing.T) {
	out := `<dictionary> {
  HTTPEnable : 0
  HTTPPort : 8080
  HTTPProxy : 10.0.0.2
  HTTPSEnable : 0
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 0
}`
	if got := parseScutil(out); got.proxy != "" {
		t.Fatalf("proxy = %q, want none", got.proxy)
	}
	if got := parseScutil(""); got.proxy != "" {
		t.Fatalf("empty output proxy = %q, want none", got.proxy)
	}
}

func TestParseWindowsProxyServer(t *testing.T) {
	cases := map[string]string{
		"":                              "",
		"127.0.0.1:7890":                "http://127.0.0.1:7890",
		"http=a:1;https=b:2":            "http://b:2",
		"http=a:1":                      "http://a:1",
		"https=b:2;socks=c:3":           "http://b:2",
		"socks=127.0.0.1:1080":          "socks5://127.0.0.1:1080",
		"http=a:1;socks=127.0.0.1:1080": "http://a:1",
		"ftp=x:21":                      "",
	}
	for in, want := range cases {
		if got := parseWindowsProxyServer(in); got != want {
			t.Errorf("parseWindowsProxyServer(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseBypassList(t *testing.T) {
	got := parseBypassList("*.corp; <local> ;;intranet")
	want := []string{"*.corp", "<local>", "intranet"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestMatchBypass(t *testing.T) {
	list := []string{"*.corp", "EXACT.example.org", "", "<local>", ".dotted.net"}
	cases := map[string]bool{
		"git.corp":              true,
		"a.b.corp":              true,
		"corp":                  true,
		"notcorp":               false,
		"chatgpt.com":           false,
		"GIT.CORP":              true,
		"exact.example.org":     true,
		"sub.exact.example.org": false,
		"intranet":              true,
		"a.dotted.net":          true,
		"dotted.net":            true,
		"xdotted.net":           false,
	}
	for host, want := range cases {
		if got := matchBypass(host, list); got != want {
			t.Errorf("matchBypass(%q) = %v, want %v", host, got, want)
		}
	}
	if matchBypass("anything.example", []string{"", "  "}) {
		t.Error("empty items must not match")
	}
}
