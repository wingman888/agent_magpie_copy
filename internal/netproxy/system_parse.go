package netproxy

import (
	"bufio"
	"regexp"
	"strings"
)

// parseScutilL3x reads the output of `scutil --proxy` (macOS).
func parseScutilL3x(out string) sysProxyL3x {
	var sp sysProxyL3x
	kv := map[string]string{}
	inExceptions := false
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "ExceptionsList") && strings.Contains(line, "<array>"):
			inExceptions = true
			continue
		case inExceptions && line == "}":
			inExceptions = false
			continue
		case inExceptions:
			if i := strings.Index(line, " : "); i >= 0 {
				sp.Bypass = append(sp.Bypass, strings.TrimSpace(line[i+3:]))
			}
			continue
		}
		if i := strings.Index(line, " : "); i >= 0 {
			kv[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+3:])
		}
	}
	if kv["ExcludeSimpleHostnames"] == "1" {
		sp.Bypass = append(sp.Bypass, "<local>")
	}
	addr := func(enable, host, port, scheme string) string {
		if kv[enable] != "1" || kv[host] == "" || kv[port] == "" {
			return ""
		}
		return scheme + "://" + kv[host] + ":" + kv[port]
	}
	sp.HTTPS = addr("HTTPSEnable", "HTTPSProxy", "HTTPSPort", "http")
	sp.HTTP = addr("HTTPEnable", "HTTPProxy", "HTTPPort", "http")
	sp.SOCKS = addr("SOCKSEnable", "SOCKSProxy", "SOCKSPort", "socks5")
	return sp
}

var regLineL3x = regexp.MustCompile(`^(REG_\w+)\s+(.*)$`)

// regValueL3x finds `name` in the output of `reg query` and returns its type and data.
func regValueL3x(out, name string) (string, string, bool) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.EqualFold(f[0], name) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), f[0]))
		if m := regLineL3x.FindStringSubmatch(rest); m != nil {
			return m[1], strings.TrimSpace(m[2]), true
		}
	}
	return "", "", false
}

// parseWindowsL3x turns the Internet Options registry values into a sysProxyL3x.
// enable is ProxyEnable (DWORD), server is ProxyServer, override is ProxyOverride.
func parseWindowsL3x(enable, server, override string) sysProxyL3x {
	var sp sysProxyL3x
	if enable != "0x1" && enable != "1" {
		return sp
	}
	if strings.Contains(server, "=") {
		for _, part := range strings.Split(server, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok || v == "" {
				continue
			}
			switch strings.ToLower(k) {
			case "https":
				sp.HTTPS = withSchemeL3x(v, "http")
			case "http":
				sp.HTTP = withSchemeL3x(v, "http")
			case "socks":
				sp.SOCKS = withSchemeL3x(v, "socks5")
			}
		}
	} else if server = strings.TrimSpace(server); server != "" {
		// one address for every protocol
		sp.HTTP = withSchemeL3x(server, "http")
		sp.HTTPS = sp.HTTP
	}
	for _, e := range strings.Split(override, ";") {
		if e = strings.TrimSpace(e); e != "" {
			sp.Bypass = append(sp.Bypass, e)
		}
	}
	return sp
}

func withSchemeL3x(v, scheme string) string {
	if strings.Contains(v, "://") {
		return v
	}
	return scheme + "://" + v
}
