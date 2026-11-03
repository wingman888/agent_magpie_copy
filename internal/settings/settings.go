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
	// Tray is what a click on the tray icon opens: panel (default) | window.
	Tray string `json:"tray,omitempty"`
	// Proxy is how magpie reaches providers: "" follows the environment and
	// the system proxy, "direct" connects without one, anything else is a
	// proxy address such as socks5://127.0.0.1:1080 or 127.0.0.1:7890.
	Proxy string `json:"proxy,omitempty"`
}

// Themes, Langs and Trays are the accepted values, in the order the UI offers them.
var (
	Themes = []string{"system", "light", "dark"}
	Langs  = []string{"system", "en", "zh"}
	Trays  = []string{"panel", "window"}
)

// ProxyDirect is the Proxy value that turns the proxy off.
const ProxyDirect = "direct"

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
	if s.Proxy != "" && s.Proxy != ProxyDirect {
		if _, err := ParseProxy(s.Proxy); err != nil {
			return err
		}
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

// ParseProxy checks a proxy address and returns it as a URL. A bare host:port
// is an HTTP proxy. Only http, https, socks5 and socks5h are accepted.
func ParseProxy(p string) (*url.URL, error) {
	raw := strings.TrimSpace(p)
	if raw == "" {
		return nil, fmt.Errorf("proxy is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("proxy %q: %v", p, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("proxy must be http://, https://, socks5:// or socks5h:// (or host:port), not %q", p)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("proxy %q has no host", p)
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("proxy %q must be only an address, without path, query or fragment", p)
	}
	return u, nil
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
	s.Proxy = strings.TrimSpace(s.Proxy)
	if strings.EqualFold(s.Proxy, ProxyDirect) {
		s.Proxy = ProxyDirect
	}
	return s
}
