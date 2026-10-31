package netproxy

import (
	"os/exec"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// systemTTL bounds how long a read of the OS proxy settings is reused.
const systemTTL = 5 * time.Second

// goos is the OS to read proxies for; a variable for tests.
var goos = runtime.GOOS

// readSystem reads the OS proxy settings: a normalized proxy URL ("" for
// none) and the exception list. It is a variable so tests can replace it.
// Failures mean "no system proxy".
var readSystem = defaultReadSystem

var sys struct {
	sync.Mutex
	at    time.Time
	proxy string
	exc   []string
	ok    bool
	goos  string
	fn    uintptr
}

// ResetCache drops the cached system proxy.
func ResetCache() {
	sys.Lock()
	sys.ok = false
	sys.Unlock()
}

func systemProxy() (string, []string) {
	sys.Lock()
	defer sys.Unlock()
	fn := reflect.ValueOf(readSystem).Pointer()
	if sys.ok && sys.goos == goos && sys.fn == fn && time.Since(sys.at) < systemTTL {
		return sys.proxy, sys.exc
	}
	p, e := readSystem()
	sys.proxy, sys.exc, sys.at, sys.ok, sys.goos, sys.fn = p, e, time.Now(), true, goos, fn
	return p, e
}

func defaultReadSystem() (string, []string) {
	switch goos {
	case "darwin":
		out, err := exec.Command("scutil", "--proxy").Output()
		if err != nil {
			return "", nil
		}
		return ParseScutil(string(out))
	case "windows":
		out, err := exec.Command("reg", "query", `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`).Output()
		if err != nil {
			return "", nil
		}
		return ParseWindowsReg(string(out))
	}
	return "", nil
}

var scutilLine = regexp.MustCompile(`^\s*([^\s:]+)\s*:\s*(.*?)\s*$`)

// ParseScutil parses `scutil --proxy` output. Enabled proxies are chosen
// HTTPS > HTTP > SOCKS.
func ParseScutil(out string) (string, []string) {
	kv := map[string]string{}
	var exc []string
	inExc := false
	for _, line := range strings.Split(out, "\n") {
		m := scutilLine.FindStringSubmatch(line)
		if m == nil {
			if strings.Contains(line, "}") {
				inExc = false
			}
			continue
		}
		k, v := m[1], m[2]
		if k == "ExceptionsList" {
			inExc = true
			continue
		}
		if inExc {
			// array item: "0 : *.local"
			if isDigits(k) {
				exc = append(exc, v)
				continue
			}
			inExc = false
		}
		kv[k] = v
	}
	for _, c := range []struct{ name, scheme string }{{"HTTPS", "http"}, {"HTTP", "http"}, {"SOCKS", "socks5"}} {
		if kv[c.name+"Enable"] != "1" {
			continue
		}
		host := kv[c.name+"Proxy"]
		if host == "" {
			continue
		}
		addr := host
		if p := kv[c.name+"Port"]; p != "" {
			addr = joinHostPort(host, p)
		}
		if n, err := Normalize(c.scheme + "://" + addr); err == nil {
			return n, exc
		}
	}
	return "", nil
}

func joinHostPort(h, p string) string {
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		return "[" + h + "]:" + p
	}
	return h + ":" + p
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

var regLine = regexp.MustCompile(`^\s*(\w+)\s+(REG_\w+)\s*(.*?)\s*$`)

// ParseWindowsReg parses `reg query` output of the Internet Settings key.
func ParseWindowsReg(out string) (string, []string) {
	vals := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if m := regLine.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			vals[m[1]] = m[3]
		}
	}
	en := strings.ToLower(vals["ProxyEnable"])
	if en != "0x1" && en != "1" {
		return "", nil
	}
	server := strings.TrimSpace(vals["ProxyServer"])
	if server == "" {
		return "", nil
	}
	var exc []string
	for _, e := range strings.Split(vals["ProxyOverride"], ";") {
		if e = strings.TrimSpace(e); e != "" {
			exc = append(exc, e)
		}
	}
	pick := func(scheme, v string) (string, bool) {
		v = strings.TrimSpace(v)
		if v == "" {
			return "", false
		}
		if !strings.Contains(v, "://") {
			v = scheme + "://" + v
		}
		n, err := Normalize(v)
		return n, err == nil
	}
	if !strings.Contains(server, "=") {
		if n, ok := pick("http", server); ok {
			return n, exc
		}
		return "", nil
	}
	m := map[string]string{}
	for _, part := range strings.Split(server, ";") {
		if k, v, ok := strings.Cut(part, "="); ok {
			m[strings.ToLower(strings.TrimSpace(k))] = v
		}
	}
	for _, c := range []struct{ key, scheme string }{{"https", "http"}, {"http", "http"}, {"socks", "socks5"}} {
		if n, ok := pick(c.scheme, m[c.key]); ok {
			return n, exc
		}
	}
	return "", nil
}

// matchExceptions applies a system exception list to host.
func matchExceptions(host string, list []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
			continue
		case e == "<local>":
			if !strings.Contains(host, ".") {
				return true
			}
		case strings.HasPrefix(e, "*."), strings.HasPrefix(e, "."):
			d := strings.TrimPrefix(strings.TrimPrefix(e, "*"), ".")
			if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
				return true
			}
		case host == e:
			return true
		}
	}
	return false
}
