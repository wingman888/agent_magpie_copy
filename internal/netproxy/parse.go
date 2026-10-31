package netproxy

import (
	"net/url"
	"strings"
)

func mk(scheme, host, port string) *url.URL {
	if host == "" {
		return nil
	}
	if port != "" {
		host += ":" + port
	}
	return &url.URL{Scheme: scheme, Host: host}
}

// ParseScutil reads the output of `scutil --proxy` (macOS network settings).
func ParseScutil(out string) *SystemConfig {
	kv := map[string]string{}
	var exc []string
	inList := false
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if inList {
			if strings.HasPrefix(ln, "}") {
				inList = false
				continue
			}
			if _, v, ok := strings.Cut(ln, ":"); ok {
				exc = append(exc, strings.TrimSpace(v))
			}
			continue
		}
		k, v, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "ExceptionsList" {
			inList = true
			continue
		}
		kv[k] = v
	}
	c := &SystemConfig{Exceptions: exc, ExcludeSimple: kv["ExcludeSimpleHostnames"] == "1"}
	if kv["HTTPSEnable"] == "1" {
		c.HTTPS = mk("http", kv["HTTPSProxy"], kv["HTTPSPort"])
	}
	if kv["HTTPEnable"] == "1" {
		c.HTTP = mk("http", kv["HTTPProxy"], kv["HTTPPort"])
	}
	if kv["SOCKSEnable"] == "1" {
		c.SOCKS = mk("socks5", kv["SOCKSProxy"], kv["SOCKSPort"])
	}
	return c
}

// ParseWinReg reads `reg query` output of the Internet Settings key.
func ParseWinReg(out string) *SystemConfig {
	kv := map[string]string{}
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(ln)
		if len(f) >= 3 && strings.HasPrefix(f[1], "REG_") {
			kv[f[0]] = strings.TrimSpace(strings.SplitN(strings.TrimSpace(ln), f[1], 2)[1])
		}
	}
	if v := kv["ProxyEnable"]; v != "0x1" && v != "1" {
		return nil
	}
	server := kv["ProxyServer"]
	if server == "" {
		return nil
	}
	c := &SystemConfig{}
	for _, e := range strings.Split(kv["ProxyOverride"], ";") {
		if e = strings.TrimSpace(e); e != "" {
			c.Exceptions = append(c.Exceptions, e)
		}
	}
	if !strings.Contains(server, "=") {
		if strings.Contains(server, "://") {
			if p, err := parseProxy(server); err == nil {
				c.HTTPS = p
			}
		} else {
			c.HTTPS = mk("http", server, "")
		}
		return c
	}
	for _, part := range strings.Split(server, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || v == "" {
			continue
		}
		switch strings.ToLower(k) {
		case "https":
			c.HTTPS = mk("http", v, "")
		case "http":
			c.HTTP = mk("http", v, "")
		case "socks":
			c.SOCKS = mk("socks5", v, "")
		}
	}
	return c
}
