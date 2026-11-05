// Package settings keeps the few preferences the desktop app has: which
// palette to paint with and which language to speak. Everything else magpie
// knows is derived from the agents' own files.
//
// The file is ~/.config/magpie/settings.json; a missing file means "follow
// the system" for both.
package settings

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Settings is what the user chose. "" and "system" both mean "follow the OS".
type Settings struct {
	Theme string `json:"theme,omitempty"` // system | light | dark
	Lang  string `json:"lang,omitempty"`  // system | en | zh
	Tray  string `json:"tray,omitempty"`  // what clicking the tray icon opens: panel | window
	// Proxy is the network proxy for requests to model vendors: "" follows
	// the environment and then the system, "direct" turns proxies off, and
	// anything else is a proxy URL (see ValidateProxy).
	Proxy string `json:"proxy,omitempty"`
}

// ProxyExample is the form shown when a proxy value is refused.
const ProxyExample = "http://127.0.0.1:7890"

// ValidateProxy checks a proxy value as the user typed it. It returns the
// trimmed value to store: "" (follow the environment and system), "direct",
// or a proxy address. A proxy address is either a URL with scheme http://,
// https://, socks5:// or socks5h:// and a non-empty host, or a bare host:port,
// which means an HTTP proxy.
func ValidateProxy(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "direct" {
		return s, nil
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", proxyError(s, "it cannot be parsed")
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "socks5", "socks5h":
		default:
			return "", proxyError(s, "the scheme must be http, https, socks5 or socks5h")
		}
		if u.Hostname() == "" {
			return "", proxyError(s, "the host is missing")
		}
		// url.Parse accepts "http://host:" with an empty port; refuse it.
		if strings.HasSuffix(u.Host, ":") {
			return "", proxyError(s, "the port is empty")
		}
		if p := u.Port(); p != "" && !validPort(p) {
			return "", proxyError(s, "the port is not a number from 1 to 65535")
		}
		return s, nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil || host == "" {
		return "", proxyError(s, "write it as host:port or as a URL")
	}
	if !validHost(host) {
		return "", proxyError(s, "the host is neither an IP address nor a host name")
	}
	if !validPort(port) {
		return "", proxyError(s, "the port is not a number from 1 to 65535")
	}
	return s, nil
}

// validPort reports whether p is a decimal number from 1 to 65535.
func validPort(p string) bool {
	if p == "" {
		return false
	}
	for _, c := range p {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
}

// validHost accepts an IP literal (brackets already removed) or a host name
// made of letters, digits and hyphens, separated by dots, where no label
// starts or ends with a hyphen.
func validHost(h string) bool {
	if net.ParseIP(h) != nil {
		return true
	}
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			isAlnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
			if !isAlnum && c != '-' {
				return false
			}
		}
	}
	return true
}

func proxyError(s, why string) error {
	return fmt.Errorf("proxy %q is not valid: %s; use a form such as %s, https://host:port, socks5://127.0.0.1:1080, or direct to turn proxies off", s, why, ProxyExample)
}

// Themes and Langs are the accepted values, in the order the UI offers them.
var (
	Themes = []string{"system", "light", "dark"}
	Langs  = []string{"system", "en", "zh"}
	Trays  = []string{"panel", "window"}
)

// Path is the settings file.
func Path() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "magpie", "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "magpie", "settings.json")
}

// Dir is the folder every magpie file lives in.
func Dir() string { return filepath.Dir(Path()) }

// Load reads the settings; anything missing or unreadable is the default.
func Load() Settings {
	var s Settings
	if b, err := os.ReadFile(Path()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s.normal()
}

// Save validates and writes the settings.
func Save(s Settings) error {
	s = s.normal()
	if !slices.Contains(Themes, s.Theme) {
		return fmt.Errorf("theme must be one of %v, not %q", Themes, s.Theme)
	}
	if !slices.Contains(Langs, s.Lang) {
		return fmt.Errorf("language must be one of %v, not %q", Langs, s.Lang)
	}
	if !slices.Contains(Trays, s.Tray) {
		return fmt.Errorf("tray must be one of %v, not %q", Trays, s.Tray)
	}
	proxy, err := ValidateProxy(s.Proxy)
	if err != nil {
		return err
	}
	s.Proxy = proxy
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), append(b, '\n'), 0o644)
}

func (s Settings) normal() Settings {
	if s.Theme == "" {
		s.Theme = "system"
	}
	if s.Lang == "" {
		s.Lang = "system"
	}
	if s.Tray == "" {
		s.Tray = "panel"
	}
	return s
}
