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
	// Tray says what a click on the tray icon does: panel | window.
	Tray string `json:"tray,omitempty"`
	// Proxy is the proxy magpie uses for its own outside requests: "" follows
	// the environment and the system, "direct" bypasses proxies, anything else
	// is a proxy URL (http, https, socks5, socks5h; a bare host:port is http).
	Proxy string `json:"proxy,omitempty"`
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
	proxy, err := NormalProxy(s.Proxy)
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

// NormalProxy validates a proxy setting and returns its canonical form: ""
// (follow the environment and system), "direct", or a proxy URL. A bare
// host:port becomes http://host:port.
func NormalProxy(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "direct" {
		return p, nil
	}
	if !strings.Contains(p, "://") {
		host, port, err := net.SplitHostPort(p)
		if err != nil || host == "" || port == "" {
			return "", fmt.Errorf("proxy must be direct, a URL (http, https, socks5, socks5h), or host:port, not %q", p)
		}
		return "http://" + net.JoinHostPort(host, port), nil
	}
	u, err := url.Parse(p)
	if err != nil {
		return "", fmt.Errorf("proxy %q: %v", p, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", fmt.Errorf("proxy scheme must be http, https, socks5 or socks5h, not %q", u.Scheme)
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("proxy %q has no host", p)
	}
	return p, nil
}
