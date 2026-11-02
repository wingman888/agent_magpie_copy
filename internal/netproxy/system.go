package netproxy

import (
	"context"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func run(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, name, args...).Output()
	return string(out)
}

// platformSystem reads the OS proxy settings; Linux has only the
// environment, so it returns nil there.
func platformSystem() *sysConfig {
	switch runtime.GOOS {
	case "darwin":
		return parseScutil(run("scutil", "--proxy"))
	case "windows":
		const key = `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`
		get := func(name string) string { return regValue(run("reg", "query", key, "/v", name), name) }
		if get("ProxyEnable") != "0x1" {
			return nil
		}
		return parseWindows(get("ProxyServer"), get("ProxyOverride"))
	}
	return nil
}

// regValue pulls the data out of `reg query` output.
func regValue(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && strings.EqualFold(f[0], name) {
			return strings.Join(f[2:], " ")
		}
	}
	return ""
}

func hostPort(scheme, hp string) *url.URL {
	hp = strings.TrimSpace(hp)
	if hp == "" {
		return nil
	}
	if strings.Contains(hp, "://") {
		u, err := url.Parse(hp)
		if err != nil || u.Host == "" {
			return nil
		}
		return u
	}
	u, err := url.Parse(scheme + "://" + hp)
	if err != nil || u.Host == "" {
		return nil
	}
	return u
}

// parseWindows reads ProxyServer ("host:port" or "http=h:p;https=h:p;socks=h:p")
// and ProxyOverride ("a;b;<local>").
func parseWindows(server, override string) *sysConfig {
	server = strings.TrimSpace(server)
	if server == "" {
		return nil
	}
	c := &sysConfig{}
	if !strings.Contains(server, "=") {
		c.HTTP = hostPort("http", server)
		c.HTTPS = c.HTTP
	} else {
		for _, kv := range strings.Split(server, ";") {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "https":
				c.HTTPS = hostPort("http", v)
			case "http":
				c.HTTP = hostPort("http", v)
			case "socks":
				c.SOCKS = hostPort("socks5", v)
			}
		}
	}
	for _, e := range strings.FieldsFunc(override, func(r rune) bool { return r == ';' || r == ',' }) {
		c.Exceptions = append(c.Exceptions, strings.TrimSpace(e))
	}
	return c
}

// parseScutil reads `scutil --proxy`.
func parseScutil(out string) *sysConfig {
	kv := map[string]string{}
	var exc []string
	inExc := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExceptionsList") {
			inExc = true
			continue
		}
		if inExc {
			if strings.HasPrefix(line, "}") {
				inExc = false
				continue
			}
			if _, v, ok := strings.Cut(line, ":"); ok {
				exc = append(exc, strings.TrimSpace(v))
			}
			continue
		}
		if k, v, ok := strings.Cut(line, " : "); ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	c := &sysConfig{Exceptions: exc, ExcludeSimple: kv["ExcludeSimpleHostnames"] == "1"}
	pick := func(prefix, scheme string) *url.URL {
		if kv[prefix+"Enable"] != "1" || kv[prefix+"Proxy"] == "" {
			return nil
		}
		hp := kv[prefix+"Proxy"]
		if p := kv[prefix+"Port"]; p != "" {
			hp += ":" + p
		}
		return hostPort(scheme, hp)
	}
	c.HTTPS = pick("HTTPS", "http")
	c.HTTP = pick("HTTP", "http")
	c.SOCKS = pick("SOCKS", "socks5")
	if c.HTTPS == nil && c.HTTP == nil && c.SOCKS == nil {
		return nil
	}
	return c
}
