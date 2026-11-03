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
	Proxy string `json:"proxy,omitempty"` // "" follow env/system | direct | proxy URL (http, https, socks5, socks5h; bare host:port means http)
}

// ProxyExample is the form shown in proxy validation errors.
const ProxyExample = "http://127.0.0.1:7890"

// ValidateProxy checks a proxy setting. "" (follow environment and system)
// and "direct" (no proxy, case-insensitive) are valid. Anything else must be
// a proxy URL with scheme http, https, socks5 or socks5h, or a bare host:port
// that is taken as http. The host must not be empty, and a port, when given,
// must be a number from 1 to 65535. The value is expected to be trimmed.
func ValidateProxy(s string) error {
	_, err := normalizeProxy(s)
	return err
}

// normalizeProxy returns the stored form of a proxy setting: trimmed, with
// "direct" in lower case. It fails with an error that shows the right form.
func normalizeProxy(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if strings.EqualFold(s, "direct") {
		return "direct", nil
	}
	bad := func(why string) error {
		return fmt.Errorf("proxy %q is not valid (%s); use a form such as %s, or direct for no proxy", s, why, ProxyExample)
	}
	raw := s
	if !strings.Contains(raw, "://") {
		// a bare host:port is taken as an HTTP proxy
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", bad("cannot be parsed")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", bad("scheme must be http, https, socks5 or socks5h")
	}
	host := u.Hostname()
	if host == "" {
		return "", bad("host is missing")
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return "", bad("host is not a valid name or address")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", bad("port must be a number from 1 to 65535")
		}
	}
	return s, nil
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
// Every check runs before the file is touched, so a rejected save leaves the
// existing settings as they were.
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
	return s
}
