// Package settings keeps the few preferences the desktop app has: which
// palette to paint with, which language to speak, what the tray icon does
// on click, and which proxy magpie's own requests go through. Everything
// else magpie knows is derived from the agents' own files.
//
// The file is ~/.config/magpie/settings.json; a missing file means "follow
// the system" for each of them.
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
	// Tray is what a click on the tray icon does: panel (default) or window.
	Tray string `json:"tray,omitempty"` // panel | window
	// Proxy is how magpie reaches providers. "" follows the environment and
	// then the system proxy, "direct" never uses a proxy, anything else is a
	// proxy address (see ProxyURL).
	Proxy string `json:"proxy,omitempty"`
}

// Themes, Langs and Trays are the accepted values, in the order the UI offers them.
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
	if s.Proxy != "" && s.Proxy != "direct" {
		if _, err := ProxyURL(s.Proxy); err != nil {
			return fmt.Errorf("proxy: %w", err)
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

// ProxyURL parses a proxy address: scheme://host:port with http, https,
// socks5 or socks5h, or a bare host:port that means an http proxy. Anything
// else (ftp://…, a path, a query, a missing port) is an error. The caller
// handles "" and "direct" before calling it.
func ProxyURL(s string) (*url.URL, error) {
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("%q is not a proxy address", s)
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, fmt.Errorf("%q: scheme must be http, https, socks5 or socks5h", s)
		}
		if u.Hostname() == "" {
			return nil, fmt.Errorf("%q has no host", s)
		}
		if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("%q must be just scheme://host:port", s)
		}
		return u, nil
	}
	if strings.ContainsAny(s, " /?#@") {
		return nil, fmt.Errorf("%q is not host:port", s)
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil || host == "" {
		return nil, fmt.Errorf("%q is not host:port", s)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("%q has a bad port", s)
	}
	return &url.URL{Scheme: "http", Host: s}, nil
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
	return s
}
