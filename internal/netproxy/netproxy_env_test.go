package netproxy

import "testing"

// Only HTTP_PROXY is set: it does not apply to https, so the request must fall
// through to the system proxy rather than going direct.
func TestHTTPProxyOnlyFallsBackToSystem(t *testing.T) {
	setup(t, sysHTTP)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9001")
	if got := forString(t, "https://chatgpt.com/"); got != "http://127.0.0.1:7890" {
		t.Fatalf("For = %q, want system proxy", got)
	}
	if p, s := Describe(); p != "http://127.0.0.1:7890" || s != "system" {
		t.Fatalf("Describe = %q, %q", p, s)
	}
}

// Only HTTP_PROXY is set and there is no system proxy: https goes direct, and
// Describe says none.
func TestHTTPProxyOnlyNoSystemIsDirect(t *testing.T) {
	setup(t, sysProxy{})
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9001")
	if got := forString(t, "https://chatgpt.com/"); got != "" {
		t.Fatalf("For = %q, want direct", got)
	}
	if p, s := Describe(); p != "" || s != "none" {
		t.Fatalf("Describe = %q, %q", p, s)
	}
}

// ALL_PROXY applies to https when HTTPS_PROXY is unset, even if HTTP_PROXY is
// set; HTTP_PROXY itself is not used for https.
func TestAllProxyFallbackForHTTPS(t *testing.T) {
	setup(t, sysHTTP)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9001")
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:9002")
	if got := forString(t, "https://chatgpt.com/"); got != "socks5://127.0.0.1:9002" {
		t.Fatalf("For = %q", got)
	}
	if p, s := Describe(); p != "socks5://127.0.0.1:9002" || s != "environment" {
		t.Fatalf("Describe = %q, %q", p, s)
	}
}

// An environment value without a scheme is an HTTP proxy, and Describe reports
// the canonical URL.
func TestEnvironmentAddressCanonical(t *testing.T) {
	setup(t, sysProxy{})
	t.Setenv("HTTPS_PROXY", "127.0.0.1:8888")
	if got := forString(t, "https://api.openai.com/"); got != "http://127.0.0.1:8888" {
		t.Fatalf("For = %q", got)
	}
	if p, s := Describe(); p != "http://127.0.0.1:8888" || s != "environment" {
		t.Fatalf("Describe = %q, %q", p, s)
	}
}

// A settings address without a scheme is reported as the canonical URL.
func TestSettingsAddressCanonical(t *testing.T) {
	setup(t, sysHTTP)
	setSetting(t, "127.0.0.1:7890")
	if got := forString(t, "https://chatgpt.com/"); got != "http://127.0.0.1:7890" {
		t.Fatalf("For = %q", got)
	}
	if p, s := Describe(); p != "http://127.0.0.1:7890" || s != "settings" {
		t.Fatalf("Describe = %q, %q", p, s)
	}
}

// "direct" is case-insensitive in settings.
func TestSettingsDirectCaseInsensitive(t *testing.T) {
	setup(t, sysHTTP)
	for _, v := range []string{"Direct", "DIRECT"} {
		setSetting(t, v)
		if got := forString(t, "https://chatgpt.com/"); got != "" {
			t.Fatalf("%s: For = %q, want direct", v, got)
		}
		if p, s := Describe(); p != "" || s != "off" {
			t.Fatalf("%s: Describe = %q, %q", v, p, s)
		}
	}
}

// Loopback stays direct even when the environment has only HTTP_PROXY.
func TestLoopbackWithHTTPProxyOnly(t *testing.T) {
	setup(t, sysHTTP)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9001")
	if got := forString(t, "http://127.0.0.1:3425/x"); got != "" {
		t.Fatalf("loopback For = %q, want direct", got)
	}
}
