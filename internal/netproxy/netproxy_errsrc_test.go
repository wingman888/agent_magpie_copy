package netproxy

import (
	"testing"
)

// A broken HTTPS_PROXY must be reported as coming from the environment, not
// from settings.
func TestDescribeInvalidEnvironmentSource(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy", "https_proxy"} {
		t.Setenv(k, "")
	}
	t.Setenv("HTTPS_PROXY", ":8080")
	p, src := Describe()
	if p != "" || src != "environment" {
		t.Fatalf("Describe() = (%q, %q), want (\"\", \"environment\")", p, src)
	}
	if _, err := For(mustParse(t, "https://chatgpt.com/")); err == nil {
		t.Fatal("For accepted an unusable HTTPS_PROXY; want error")
	}
}

// NO_PROXY entries may carry a port; the port is ignored for matching.
func TestNoProxyEntryWithPort(t *testing.T) {
	env := map[string]string{"HTTPS_PROXY": "http://10.0.0.1:8080", "NO_PROXY": "chatgpt.com:443"}
	check(t, "https://chatgpt.com/", "", env, sysWith("http://127.0.0.1:7890"), "", "environment")
	check(t, "https://other.net/", "", env, noSys, "http://10.0.0.1:8080", "environment")
}

func TestNoProxyEntryWithPortForms(t *testing.T) {
	for entry, host := range map[string]string{
		"10.1.2.3:8080":     "10.1.2.3",
		"[2001:db8::1]:443": "2001:db8::1",
		"Example.org:80":    "example.org",
	} {
		if !noProxyMatch(host, entry) {
			t.Errorf("noProxyMatch(%q, %q) = false, want true", host, entry)
		}
	}
	if noProxyMatch("other.net", "chatgpt.com:443") {
		t.Error("unrelated host matched NO_PROXY entry with port")
	}
}
