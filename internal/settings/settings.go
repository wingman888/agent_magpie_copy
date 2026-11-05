// Package settings keeps the few preferences the desktop app has: which
// palette to paint with and which language to speak, where the tray icon
// opens and how outbound requests reach their providers. Everything else
// magpie knows is derived from the agents' own files.
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
	"strings"
)

// Settings is what the user chose. "" and "system" both mean "follow the OS".
type Settings struct {
	Theme string `json:"theme,omitempty"` // system | light | dark
	Lang  string `json:"lang,omitempty"`  // system | en | zh
	Tray  string `json:"tray,omitempty"`  // panel | window: what a click on the tray icon opens
	// Proxy is "" (follow $HTTPS_PROXY and the others, then the system),
	// "direct" (never use a proxy), or the proxy address itself:
	// http://, https://, socks5:// or socks5h:// (a bare host:port is http).
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
	s = s.normal()
	// A hand-edited file may hold a value Save would refuse; fall back to
	// the default rather than failing every caller.
	if !slices.Contains(Trays, s.Tray) {
		s.Tray = "panel"
	}
	if _, err := ParseProxy(s.Proxy); err != nil && s.Proxy != "direct" {
		s.Proxy = ""
	}
	return s
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
	proxy, err := CanonicalProxy(s.Proxy)
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

// CanonicalProxy checks a Settings.Proxy value and returns the form it is
// saved in: "" and "direct" unchanged, a bare host:port turned into
// http://host:port, anything else returned as given after trimming.
func CanonicalProxy(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "direct" {
		return p, nil
	}
	if !strings.Contains(p, "://") {
		if _, _, err := net.SplitHostPort(p); err != nil {
			return "", fmt.Errorf("proxy must be direct, a host:port, or a http, https, socks5 or socks5h URL, not %q", p)
		}
		p = "http://" + p
	}
	if _, err := ParseProxy(p); err != nil {
		return "", err
	}
	return p, nil
}

// ParseProxy turns a Settings.Proxy value into the proxy URL. It returns
// nil, nil for "" and "direct" (no proxy from settings).
func ParseProxy(p string) (*url.URL, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "direct" {
		return nil, nil
	}
	if !strings.Contains(p, "://") {
		p = "http://" + p
	}
	u, err := url.Parse(p)
	if err != nil {
		return nil, fmt.Errorf("proxy %q: %v", p, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("proxy scheme must be http, https, socks5 or socks5h, not %q", u.Scheme)
	}
	if u.Hostname() == "" || u.Port() == "" {
		return nil, fmt.Errorf("proxy %q needs a host and a port", p)
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("proxy %q must be just scheme://host:port", p)
	}
	return u, nil
}
