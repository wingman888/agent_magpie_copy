//go:build darwin

package netproxy

import (
	"bufio"
	"bytes"
	"os/exec"
	"regexp"
	"strings"
)

var listItem = regexp.MustCompile(`^\s*\d+ : (.+?)\s*$`)

// readPlatform reads the network settings with scutil --proxy, which prints
// a dictionary like:
//
//	ExceptionsList : <array> {
//	  0 : *.local
//	}
//	HTTPSEnable : 1
//	HTTPSProxy : 127.0.0.1
//	HTTPSPort : 7890
func readPlatform() system {
	out, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		return system{}
	}
	return parseScutil(out)
}

func parseScutil(out []byte) system {
	vals := map[string]string{}
	var s system
	inList := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		trim := strings.TrimSpace(line)
		if inList {
			if trim == "}" {
				inList = false
				continue
			}
			if m := listItem.FindStringSubmatch(line); m != nil {
				s.exceptions = append(s.exceptions, m[1])
			}
			continue
		}
		key, val, ok := strings.Cut(trim, " : ")
		if !ok {
			continue
		}
		if key == "ExceptionsList" {
			inList = true
			continue
		}
		vals[key] = strings.TrimSpace(val)
	}
	s.excludeLocal = vals["ExcludeSimpleHostnames"] == "1"
	s.https = hostPort(vals, "HTTPS")
	s.http = hostPort(vals, "HTTP")
	s.socks = hostPort(vals, "SOCKS")
	return s
}

// hostPort returns host:port for a protocol when its Enable flag is on.
func hostPort(vals map[string]string, proto string) string {
	if vals[proto+"Enable"] != "1" || vals[proto+"Proxy"] == "" {
		return ""
	}
	h := vals[proto+"Proxy"]
	if p := vals[proto+"Port"]; p != "" {
		return h + ":" + p
	}
	return h
}
