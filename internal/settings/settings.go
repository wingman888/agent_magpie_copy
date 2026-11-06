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
	"strconv"
	"strings"
)

// Settings is what the user chose. "" and "system" both mean "follow the OS".
type Settings struct {
	Theme string `json:"theme,omitempty"` // system | light | dark
	Lang  string `json:"lang,omitempty"`  // system | en | zh
	Tray  string `json:"tray,omitempty"`  // what clicking the tray icon opens: panel | window
	Proxy string `json:"proxy,omitempty"` // "" follow env/system | direct | proxy URL (http, https, socks5, socks5h; bare host:port is http)
}

// ValidateProxy checks a proxy setting after trimming surrounding space.
// "" (follow the environment and system) and "direct" are valid. Anything
// else must be a proxy URL with scheme http, https, socks5 or socks5h and a
// host; a bare host:port is read as http.
func ValidateProxy(s string) error {
	s = strings.TrimSpace(s)
	if s == "" || s == "direct" {
		return nil
	}
	bad := fmt.Errorf("proxy 值无效：%q，正确写法示例：http://127.0.0.1:7890、socks5://127.0.0.1:1080 或 direct", s)
	raw := s
	if !strings.Contains(s, "://") {
		raw = "http://" + s
	}
	u, err := url.Parse(raw)
	if err != nil {
		return bad
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return bad
	}
	if u.Hostname() == "" || strings.HasSuffix(u.Host, ":") {
		return bad
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return bad
		}
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
	if err := ValidateProxy(s.Proxy); err != nil {
		return err
	}
	if !slices.Contains(Themes, s.Theme) {
		return fmt.Errorf("theme must be one of %v, not %q", Themes, s.Theme)
	}
	if !slices.Contains(Langs, s.Lang) {
		return fmt.Errorf("language must be one of %v, not %q", Langs, s.Lang)
	}
	if !slices.Contains(Trays, s.Tray) {
		return fmt.Errorf("tray must be one of %v, not %q", Trays, s.Tray)
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
