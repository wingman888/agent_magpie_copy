package netproxy

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
)

var diffURLs = []string{
	"https://api.vendor.example/v1", "http://api.vendor.example/", "https://a.corp/x",
	"http://127.0.0.1:1/", "https://[::1]:443/", "https://localhost/", "https://intranet/",
	"https://foo.internal/", "https://x.example.com:8443/", "https://vendor.example:443/",
	"http://10.1.2.3/", "https://10.1.2.3:8080/", "https://api.vendor.example.org/", "wss://api.vendor.example/",
}

var diffEnvs = []map[string]string{
	{},
	{"HTTPS_PROXY": "http://p:1"},
	{"https_proxy": "http://p:2"},
	{"HTTP_PROXY": "http://h:3"},
	{"http_proxy": "http://h:3"},
	{"ALL_PROXY": "socks5://a:4"},
	{"all_proxy": "socks5h://a:4"},
	{"HTTPS_PROXY": "p:5"},
	{"HTTPS_PROXY": "ftp://p"},
	{"HTTPS_PROXY": "http://p:1", "NO_PROXY": "api.vendor.example"},
	{"HTTPS_PROXY": "http://p:1", "NO_PROXY": ".example.com"},
	{"HTTPS_PROXY": "http://p:1", "NO_PROXY": "*"},
	{"HTTPS_PROXY": "http://p:1", "NO_PROXY": "vendor.example:443"},
	{"HTTPS_PROXY": "http://p:1", "NO_PROXY": "10.0.0.0/8"},
	{"HTTPS_PROXY": "http://p:1", "NO_PROXY": "intranet,*.internal"},
	{"HTTP_PROXY": "http://h:3", "NO_PROXY": "a.corp"},
	{"HTTP_PROXY": "http://h:3", "NO_PROXY": "127.0.0.1"},
	{"HTTPS_PROXY": "http://p:1", "HTTP_PROXY": "http://h:3", "ALL_PROXY": "socks5://a:4"},
}

func TestDiffChild(t *testing.T) {
	if os.Getenv("DIFF_CHILD") == "" {
		t.Skip()
	}
	for _, u := range diffURLs {
		pu, _ := url.Parse(u)
		req := &http.Request{URL: pu}
		std, serr := http.ProxyFromEnvironment(req)
		ours, oerr := For(pu)
		fmt.Printf("RES %s | std=%v err=%v | ours=%v err=%v\n", u, std, serr != nil, ours, oerr != nil)
	}
}

func TestDiffAgainstStdlib(t *testing.T) {
	total := 0
	for i, env := range diffEnvs {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDiffChild$")
		cmd.Env = []string{"DIFF_CHILD=1", "XDG_CONFIG_HOME=" + t.TempDir(), "HOME=" + t.TempDir()}
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		out, _ := cmd.CombinedOutput()
		var items []string
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "RES ") {
				continue
			}
			parts := strings.SplitN(line, " | ", 3)
			if len(parts) < 3 {
				continue
			}
			total++
			std := strings.TrimPrefix(parts[1], "std=")
			ours := strings.TrimPrefix(parts[2], "ours=")
			if std != ours {
				u := strings.TrimPrefix(parts[0], "RES ")
				items = append(items, u+"["+std+"|"+ours+"]")
			}
		}
		if len(items) > 0 {
			t.Errorf("DIFF E%02d %v: %s", i, env, strings.Join(items, " ; "))
		}
	}
	t.Errorf("DIFFSUMMARY total=%d", total)
}
