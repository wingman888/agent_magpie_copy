package netproxy_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
)

var proxyVars = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD"}

type report struct {
	For      map[string]string
	Proxy    string
	Source   string
}

// The Go standard library caches the environment once per process, so each
// environment scenario runs in a fresh child process.
func TestAcceptHelperProcess(t *testing.T) {
	if os.Getenv("RSI_HELPER") != "1" {
		t.Skip("helper")
	}
	r := report{For: map[string]string{}}
	for _, s := range strings.Split(os.Getenv("RSI_URLS"), ",") {
		u, _ := url.Parse(s)
		p, err := netproxy.For(u)
		if err != nil {
			r.For[s] = "ERR"
		} else if p == nil {
			r.For[s] = ""
		} else {
			r.For[s] = p.String()
		}
	}
	r.Proxy, r.Source = netproxy.Describe()
	b, _ := json.Marshal(r)
	os.Stdout.WriteString("RSIREPORT" + string(b) + "\n")
}

func runChild(t *testing.T, cfg settings.Settings, env map[string]string, urls ...string) report {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAcceptHelperProcess$", "-test.v")
	var e []string
	for _, kv := range os.Environ() {
		k := strings.SplitN(kv, "=", 2)[0]
		drop := false
		for _, v := range proxyVars {
			if strings.EqualFold(k, v) {
				drop = true
			}
		}
		if !drop && k != "XDG_CONFIG_HOME" && k != "HOME" {
			e = append(e, kv)
		}
	}
	e = append(e, "XDG_CONFIG_HOME="+dir, "HOME="+dir, "RSI_HELPER=1", "RSI_URLS="+strings.Join(urls, ","))
	for k, v := range env {
		e = append(e, k+"="+v)
	}
	cmd.Env = e
	// write settings for the child via the same config dir
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	if cfg.Proxy != "" {
		if err := settings.Save(cfg); err != nil {
			t.Fatal(err)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "RSIREPORT") {
			var r report
			if err := json.Unmarshal([]byte(strings.TrimPrefix(l, "RSIREPORT")), &r); err != nil {
				t.Fatal(err)
			}
			return r
		}
	}
	t.Fatalf("no report:\n%s", out)
	return report{}
}

const vendor = "https://chatgpt.com/backend-api/codex/responses"

var loopbackURLs = []string{"http://127.0.0.1:3425/x", "http://localhost/x", "http://foo.localhost:80/", "http://[::1]:3425/"}

func TestAcceptSettingsProxyWins(t *testing.T) {
	r := runChild(t, settings.Settings{Proxy: "socks5://127.0.0.1:1080"},
		map[string]string{"HTTPS_PROXY": "http://127.0.0.1:9999"}, vendor)
	if r.For[vendor] != "socks5://127.0.0.1:1080" {
		t.Fatalf("got %q", r.For[vendor])
	}
	if r.Source != "settings" || !strings.Contains(r.Proxy, "127.0.0.1:1080") {
		t.Fatalf("describe: %q %q", r.Proxy, r.Source)
	}
}

func TestAcceptDirectDisablesProxy(t *testing.T) {
	r := runChild(t, settings.Settings{Proxy: "direct"},
		map[string]string{"HTTPS_PROXY": "http://127.0.0.1:9999", "HTTP_PROXY": "http://127.0.0.1:9999"}, vendor)
	if r.For[vendor] != "" {
		t.Fatalf("got %q", r.For[vendor])
	}
	if r.Source != "off" || r.Proxy != "" {
		t.Fatalf("describe: %q %q", r.Proxy, r.Source)
	}
}

func TestAcceptEnvironmentUsedWhenSettingsEmpty(t *testing.T) {
	r := runChild(t, settings.Settings{}, map[string]string{"HTTPS_PROXY": "http://127.0.0.1:9999"}, vendor)
	if r.For[vendor] != "http://127.0.0.1:9999" {
		t.Fatalf("got %q", r.For[vendor])
	}
	if r.Source != "environment" || !strings.Contains(r.Proxy, "127.0.0.1:9999") {
		t.Fatalf("describe: %q %q", r.Proxy, r.Source)
	}
}

func TestAcceptNoProxyEnvStillHonoured(t *testing.T) {
	r := runChild(t, settings.Settings{}, map[string]string{
		"HTTPS_PROXY": "http://127.0.0.1:9999", "NO_PROXY": "internal.example.com"},
		vendor, "https://internal.example.com/x")
	if r.For[vendor] == "" || r.For["https://internal.example.com/x"] != "" {
		t.Fatalf("%v", r.For)
	}
}

func TestAcceptLoopbackNeverProxied(t *testing.T) {
	cases := []struct {
		cfg settings.Settings
		env map[string]string
	}{
		{settings.Settings{Proxy: "http://127.0.0.1:7890"}, map[string]string{"HTTPS_PROXY": "http://127.0.0.1:9999", "HTTP_PROXY": "http://127.0.0.1:9999"}},
		{settings.Settings{}, map[string]string{"HTTPS_PROXY": "http://127.0.0.1:9999", "HTTP_PROXY": "http://127.0.0.1:9999", "ALL_PROXY": "http://127.0.0.1:9999"}},
	}
	for _, c := range cases {
		r := runChild(t, c.cfg, c.env, loopbackURLs...)
		for _, u := range loopbackURLs {
			if r.For[u] != "" {
				t.Errorf("%s proxied via %q (cfg %q)", u, r.For[u], c.cfg.Proxy)
			}
		}
	}
}

func TestAcceptNoProxyAnywhereIsNone(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("system proxy is read on this OS")
	}
	r := runChild(t, settings.Settings{}, nil, vendor)
	if r.For[vendor] != "" || r.Source != "none" || r.Proxy != "" {
		t.Fatalf("%+v", r)
	}
}

func TestAcceptSchemelessSettingIsHTTPProxy(t *testing.T) {
	r := runChild(t, settings.Settings{Proxy: "127.0.0.1:7890"}, nil, vendor)
	if r.For[vendor] != "http://127.0.0.1:7890" {
		t.Fatalf("got %q", r.For[vendor])
	}
}

func TestAcceptInstallRoutesDefaultClientThroughProxy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	got := make(chan string, 4)
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Host
		w.Write([]byte("via-proxy"))
	}))
	defer px.Close()
	if err := settings.Save(settings.Settings{Proxy: px.URL}); err != nil {
		t.Fatal(err)
	}
	netproxy.Install()
	resp, err := http.Get("http://vendor.invalid/v1/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case h := <-got:
		if h != "vendor.invalid" {
			t.Fatalf("host %q", h)
		}
	default:
		t.Fatal("request did not reach proxy")
	}
	// takes effect without restart: switch to direct, request must not hit proxy
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("direct")) }))
	defer local.Close()
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(local.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case h := <-got:
		t.Fatalf("loopback request went to proxy: %q", h)
	default:
	}
}
