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
	"strings"
)

// Settings is what the user chose. "" and "system" both mean "follow the OS".
type Settings struct {
	Theme string `json:"theme,omitempty"` // system | light | dark
	Lang  string `json:"lang,omitempty"`  // system | en | zh
	Tray  string `json:"tray,omitempty"`  // what clicking the tray icon opens: panel | window
	// Proxy is the outbound proxy for requests to vendors: "" follows the
	// environment and then the OS, "direct" turns proxies off, anything else
	// is a proxy address such as socks5://127.0.0.1:1080 or 127.0.0.1:7890.
	Proxy string `json:"proxy,omitempty"`
}

// proxyExample is quoted in the error for an invalid proxy value.
const proxyExample = "http://127.0.0.1:7890"

// ValidateProxy reports whether s is an accepted proxy setting. "" and
// "direct" are always accepted. Otherwise s must be a URL with scheme http,
// https, socks5 or socks5h and a host, or a bare host:port (taken as HTTP).
// s is expected to be already trimmed.
func ValidateProxy(s string) error {
	if s == "" || s == "direct" {
		return nil
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return fmt.Errorf("proxy %q is not a valid address, for example %s", s, proxyExample)
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return fmt.Errorf("proxy %q must start with http://, https://, socks5:// or socks5h://, for example %s", s, proxyExample)
		}
		if u.Hostname() == "" {
			return fmt.Errorf("proxy %q has no host, for example %s", s, proxyExample)
		}
		if p := u.Port(); p != "" {
			if !validPort(p) {
				return fmt.Errorf("proxy %q has an invalid port, for example %s", s, proxyExample)
			}
		}
		return nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return fmt.Errorf("proxy %q is not a valid address, for example %s (or \"direct\" to turn proxies off)", s, proxyExample)
	}
	if host == "" {
		return fmt.Errorf("proxy %q has no host, for example %s", s, proxyExample)
	}
	if !validPort(port) {
		return fmt.Errorf("proxy %q has an invalid port, for example %s", s, proxyExample)
	}
	return nil
}

func validPort(p string) bool {
	n := 0
	for _, c := range p {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
		if n > 65535 {
			return false
		}
	}
	return p != ""
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

// ProxyLoadError reports a "proxy" key in the settings file that is present
// but is not a string, such as a number. A missing or unreadable file, and a
// file that is not a JSON object at all, are not errors here: they match Load,
// which treats them as defaults. Load itself still ignores these errors and
// returns defaults.
func ProxyLoadError() error {
	b, err := os.ReadFile(Path())
	if err != nil {
		return nil
	}
	// Decode only the top-level keys. A file that is not a JSON object fails
	// here and is deliberately not reported.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return nil
	}
	raw, ok := top["proxy"]
	if !ok {
		return nil
	}
	var p string
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("proxy in %s must be a string such as %s: %w", Path(), proxyExample, err)
	}
	return nil
}

// Save validates and writes the settings.
func Save(s Settings) error {
	s = s.normal()
	s.Proxy = strings.TrimSpace(s.Proxy)
	if !slices.Contains(Themes, s.Theme) {
		return fmt.Errorf("theme must be one of %v, not %q", Themes, s.Theme)
	}
	if !slices.Contains(Langs, s.Lang) {
		return fmt.Errorf("language must be one of %v, not %q", Langs, s.Lang)
	}
	if !slices.Contains(Trays, s.Tray) {
		return fmt.Errorf("tray must be one of %v, not %q", Trays, s.Tray)
	}
	if err := ValidateProxy(s.Proxy); err != nil {
		return err
	}
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
