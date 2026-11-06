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
	Proxy string `json:"proxy,omitempty"` // "" follows env then the OS; "direct" turns proxies off; else a proxy URL
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

// ProxyExample is the form the proxy error messages show as the right way to write one.
const ProxyExample = "http://127.0.0.1:7890"

// ValidateProxy accepts "" (follow the environment and the OS), "direct"
// (no proxy), a URL with scheme http, https, socks5 or socks5h and a host,
// or a bare host:port, which means an HTTP proxy. Surrounding white space
// is ignored.
func ValidateProxy(v string) error {
	v = strings.TrimSpace(v)
	if v == "" || v == "direct" {
		return nil
	}
	bad := func() error {
		return fmt.Errorf("proxy %q is not valid: use \"direct\" to turn proxies off, or a proxy address such as %s (schemes http, https, socks5, socks5h; a bare host:port means http, e.g. 127.0.0.1:7890)", v, ProxyExample)
	}
	if strings.Contains(v, "://") {
		u, err := url.Parse(v)
		if err != nil {
			return bad()
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return bad()
		}
		if u.Hostname() == "" {
			return bad()
		}
		if p := u.Port(); p != "" {
			if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
				return bad()
			}
		}
		return nil
	}
	// bare host:port, an HTTP proxy
	host, port, err := net.SplitHostPort(v)
	if err != nil || host == "" || port == "" {
		return bad()
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return bad()
	}
	if strings.ContainsAny(v, "/@?# ") {
		return bad()
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
