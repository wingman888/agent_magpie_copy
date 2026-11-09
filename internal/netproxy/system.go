package netproxy

import (
	"os/exec"
	"runtime"
	"strings"
)

// readSystem asks the operating system for its proxy. A failure means there
// is no system proxy.
func readSystem() System {
	switch runtime.GOOS {
	case "darwin":
		out, err := runQuiet("scutil", "--proxy")
		if err != nil {
			return System{}
		}
		return parseScutil(out)
	case "windows":
		out, err := runQuiet("reg", "query", `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`)
		if err != nil {
			return System{}
		}
		return parseRegistry(out)
	}
	return System{}
}

func runQuiet(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	hideWindow(cmd)
	out, err := cmd.Output()
	return string(out), err
}

// parseScutil reads the output of `scutil --proxy`, a plist-like dictionary.
func parseScutil(out string) System {
	var sys System
	vals := map[string]string{}
	inExceptions := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExceptionsList") {
			inExceptions = true
			continue
		}
		if inExceptions {
			if line == "}" {
				inExceptions = false
				continue
			}
			if _, v, ok := strings.Cut(line, " : "); ok {
				sys.Exceptions = append(sys.Exceptions, strings.TrimSpace(v))
			}
			continue
		}
		if k, v, ok := strings.Cut(line, " : "); ok {
			vals[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	sys.ExcludePlain = vals["ExcludeSimpleHostnames"] == "1"
	sys.HTTPS = scutilProxy(vals, "HTTPS", "http")
	sys.HTTP = scutilProxy(vals, "HTTP", "http")
	sys.SOCKS = scutilProxy(vals, "SOCKS", "socks5")
	return sys
}

func scutilProxy(vals map[string]string, proto, scheme string) string {
	if vals[proto+"Enable"] != "1" || vals[proto+"Proxy"] == "" {
		return ""
	}
	return schemeURL(scheme, vals[proto+"Proxy"], vals[proto+"Port"])
}

// parseRegistry reads the output of `reg query` on the Internet Settings key.
func parseRegistry(out string) System {
	vals := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, " REG_")
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		_, rest, _ := strings.Cut(strings.TrimSpace(line[i+1:]), " ")
		vals[name] = strings.TrimSpace(rest)
	}
	var sys System
	if vals["ProxyEnable"] != "0x1" {
		return sys
	}
	server := vals["ProxyServer"]
	if strings.Contains(server, "=") {
		for _, part := range strings.Split(server, ";") {
			proto, addr, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok {
				continue
			}
			switch strings.ToLower(proto) {
			case "https":
				sys.HTTPS = schemeURL("http", addr, "")
			case "http":
				sys.HTTP = schemeURL("http", addr, "")
			case "socks":
				sys.SOCKS = schemeURL("socks5", addr, "")
			}
		}
	} else if server != "" {
		// one address for every protocol
		sys.HTTPS = schemeURL("http", server, "")
		sys.HTTP = sys.HTTPS
	}
	for _, e := range strings.Split(vals["ProxyOverride"], ";") {
		e = strings.TrimSpace(e)
		if e == "<local>" {
			sys.ExcludePlain = true
			continue
		}
		if e != "" {
			sys.Exceptions = append(sys.Exceptions, e)
		}
	}
	return sys
}

// schemeURL builds scheme://host:port; an address that already has a scheme
// is kept as it is.
func schemeURL(scheme, host, port string) string {
	if strings.Contains(host, "://") {
		return host
	}
	if port != "" {
		host = host + ":" + port
	}
	return scheme + "://" + host
}
