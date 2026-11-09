// Package settings keeps the few preferences the desktop app has: which
// palette to paint with, which language to speak, what a tray click does,
// and whether magpie's own requests go through a proxy. Everything else magpie
// knows is derived from the agents' own files.
//
// The file is ~/.config/magpie/settings.json; a missing file means "follow
// the system" for each choice.
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
	Tray  string `json:"tray,omitempty"`  // panel | window: what a tray click opens
	// Proxy: "" follows the environment and then the system; "direct" never
	// uses a proxy; anything else is the proxy address (http, https, socks5,
	// socks5h). A bare host:port is an HTTP proxy.
	Proxy string `json:"proxy,omitempty"`
}

// Themes, Langs and Trays are the accepted values, in the order the UI offers them.
var (
	Themes = []string{"system", "light", "dark"}
	Langs  = []string{"system", "en", "zh"}
	Trays  = []string{"panel", "window"}
)

// proxySchemes are the proxy address schemes magpie accepts.
var proxySchemes = []string{"http", "https", "socks5", "socks5h"}

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
	proxy, err := normalizeProxy(s.Proxy)
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
	s.Proxy = strings.TrimSpace(s.Proxy)
	return s
}

// normalizeProxy checks a proxy setting and returns its stored form: "",
// "direct", or a URL with a scheme (a bare host:port becomes http://host:port).
func normalizeProxy(v string) (string, error) {
	v = strings.TrimSpace(v)
	switch {
	case v == "" || v == "direct":
		return v, nil
	case strings.Contains(v, "://"):
		u, err := url.Parse(v)
		if err != nil || !slices.Contains(proxySchemes, strings.ToLower(u.Scheme)) || u.Hostname() == "" {
			return "", fmt.Errorf("proxy must be direct, or an address starting with http://, https://, socks5:// or socks5h:// — not %q", v)
		}
		return v, nil
	default:
		host, port, err := net.SplitHostPort(v)
		if err != nil || host == "" || port == "" {
			return "", fmt.Errorf("proxy must be direct, a URL such as socks5://127.0.0.1:1080, or host:port — not %q", v)
		}
		return "http://" + v, nil
	}
}
