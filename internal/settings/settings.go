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
	"os"
	"path/filepath"
	"net/url"
	"slices"
	"strings"
)

// Settings is what the user chose. "" and "system" both mean "follow the OS".
type Settings struct {
	Theme string `json:"theme,omitempty"` // system | light | dark
	Lang  string `json:"lang,omitempty"`  // system | en | zh
	Tray  string `json:"tray,omitempty"`  // panel | window: what a tray click opens
	// Proxy for magpie's own requests: "" follows the environment, then the
	// system; "direct" never proxies; else a proxy URL or host:port.
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
	px, err := NormalProxy(s.Proxy)
	if err != nil {
		return err
	}
	s.Proxy = px
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

// NormalProxy validates a proxy setting: "", "direct", a URL with an
// http, https, socks5 or socks5h scheme, or a bare host:port (an http
// proxy). It returns the value to store.
func NormalProxy(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if strings.EqualFold(v, "direct") {
		return "direct", nil
	}
	bad := fmt.Errorf("proxy must be empty, direct, or an http://, https://, socks5:// or socks5h:// address, not %q", v)
	if strings.Contains(v, "://") {
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" {
			return "", bad
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "socks5", "socks5h":
			return v, nil
		}
		return "", bad
	}
	u, err := url.Parse("http://" + v)
	if err != nil || u.Hostname() == "" || strings.ContainsAny(v, " /?#") {
		return "", bad
	}
	return v, nil
}
