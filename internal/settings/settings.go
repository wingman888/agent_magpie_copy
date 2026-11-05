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
	"io"
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

	// Tray is what a click on the tray icon does: panel | window.
	Tray string `json:"tray,omitempty"`
	// Proxy is how magpie reaches providers: "" follows the environment and
	// then the system proxy, "direct" never proxies, anything else is a proxy
	// URL (http, https, socks5, socks5h; a bare host:port is http).
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
	proxy, err := NormalizeProxy(s.Proxy)
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

// Apply decodes a settings document on top of base. Fields the document does
// not mention keep their value in base, so a save that only carries theme and
// lang leaves proxy and tray as they were.
func Apply(base Settings, r io.Reader) (Settings, error) {
	s := base
	if err := json.NewDecoder(r).Decode(&s); err != nil {
		return base, err
	}
	return s, nil
}

// NormalizeProxy checks a proxy setting and returns it in the form to store:
// "" and "direct" as they are, a bare host:port as http://host:port, and any
// other accepted URL unchanged. Other schemes are an error.
func NormalizeProxy(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || v == "direct" {
		return v, nil
	}
	raw, bare := v, !strings.Contains(v, "://")
	if bare {
		raw = "http://" + v
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("proxy %q must be direct, or a host:port, or a URL like socks5://127.0.0.1:1080", v)
	}
	if !slices.Contains([]string{"http", "https", "socks5", "socks5h"}, u.Scheme) {
		return "", fmt.Errorf("proxy scheme must be http, https, socks5 or socks5h, not %q", u.Scheme)
	}
	if bare {
		if u.Port() == "" {
			return "", fmt.Errorf("proxy %q needs a port, like 127.0.0.1:7890", v)
		}
		return raw, nil
	}
	return v, nil
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
