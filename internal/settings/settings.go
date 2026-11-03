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
	Tray  string `json:"tray,omitempty"`  // what clicking the tray icon opens: panel | window
	Proxy string `json:"proxy,omitempty"` // "" follow env/system | "direct" | proxy URL (http, https, socks5, socks5h; bare host:port means http)
}

// ProxyExample is the form shown when a proxy value is rejected.
const ProxyExample = "http://127.0.0.1:7890"

// ValidProxy reports whether v (already trimmed) is an accepted proxy value:
// "", "direct", or a proxy address with a non-empty host.
func ValidProxy(v string) error {
	if v == "" || v == "direct" {
		return nil
	}
	if strings.ContainsAny(v, " \t\r\n") {
		return fmt.Errorf("proxy %q must not contain spaces; use a form like %s, or \"direct\" to turn proxies off", v, ProxyExample)
	}
	raw := v
	if !strings.Contains(v, "://") {
		// bare host:port is an HTTP proxy
		raw = "http://" + v
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("proxy %q cannot be parsed (%v); use a form like %s, or \"direct\"", v, err, ProxyExample)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("proxy %q has unsupported scheme %q; use http://, https://, socks5:// or socks5h://, for example %s", v, u.Scheme, ProxyExample)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("proxy %q has no host; use a form like %s", v, ProxyExample)
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("proxy %q must be only scheme, host and port; use a form like %s", v, ProxyExample)
	}
	return nil
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
	if err := ValidProxy(s.Proxy); err != nil {
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
