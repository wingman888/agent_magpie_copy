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
	Tray  string `json:"tray,omitempty"`  // panel | window: what a click on the tray icon does
	Proxy string `json:"proxy,omitempty"` // "" follow env then system; "direct"; or a proxy address
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
	if _, err := ProxyURL(s.Proxy); err != nil {
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
	s.Proxy = strings.TrimSpace(s.Proxy)
	return s
}

// ProxySchemes are the proxy address schemes magpie speaks.
var ProxySchemes = []string{"http", "https", "socks5", "socks5h"}

// ProxyURL checks a proxy setting and returns it as a URL. "" and "direct"
// are not addresses: they return nil, nil. A bare host:port is an http proxy.
// Credentials in the address are allowed but are never echoed in errors.
func ProxyURL(v string) (*url.URL, error) {
	v = strings.TrimSpace(v)
	if v == "" || strings.EqualFold(v, "direct") {
		return nil, nil
	}
	raw := v
	if !strings.Contains(v, "://") {
		raw = "http://" + v
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("proxy %q is not a valid address; use http://, https://, socks5:// or host:port", redact(v))
	}
	scheme := strings.ToLower(u.Scheme)
	if !slices.Contains(ProxySchemes, scheme) {
		return nil, fmt.Errorf("proxy scheme %q is not supported; use http://, https://, socks5:// or host:port", scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("proxy %q has no host", redact(v))
	}
	if !strings.Contains(v, "://") {
		if _, _, err := net.SplitHostPort(u.Host); err != nil {
			return nil, fmt.Errorf("proxy %q must be host:port", redact(v))
		}
	}
	if u.Port() != "" && !validPort(u.Port()) {
		return nil, fmt.Errorf("proxy %q has an invalid port", redact(v))
	}
	u.Scheme = scheme
	return u, nil
}

func validPort(p string) bool {
	n := 0
	for _, c := range p {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n > 0 && n <= 65535
}

// redact hides any user:password part of an address before it is shown.
func redact(v string) string {
	if i := strings.Index(v, "://"); i >= 0 {
		rest := v[i+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			return v[:i+3] + "***@" + rest[at+1:]
		}
	}
	return v
}
